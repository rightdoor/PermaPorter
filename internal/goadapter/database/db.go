// Package database 提供 go 生态的查询层：挂载在单库 Store 上的各表 CRUD。
// 单库中 go 生态只触达 go_* 前缀表与共享配置表 ecosystem_settings（本生态行），
// 查询层入口带表隔离断言，越界即 panic（评审期暴露，防跨生态误删）。
package database

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"permaporter/internal/core/database"
	"permaporter/internal/goadapter/models"
)

// slowQueryThreshold 超过该时长的 SQL（含连接池排队等待时间）打印慢查询日志。
const slowQueryThreshold = 100 * time.Millisecond

// allowTables go 生态允许触达的表集合（表隔离断言白名单）。
// ecosystem_settings 是各生态共享的配置表，各自只读写自己 ecosystem='go' 的行。
var allowTables = map[string]bool{
	"go_modules":         true,
	"go_versions":        true,
	"go_status":          true,
	"go_daily_stats":     true,
	"ecosystem_settings": true,
}

// tableRefRe 提取 SQL 中 FROM/INTO/UPDATE/JOIN 后引用的表名。
// 本查询层的 SQL 不含含关键字的字符串字面量，正则提取足够可靠。
var tableRefRe = regexp.MustCompile(`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)

// assertTables 表隔离断言：SQL 只允许触达 allowTables 白名单内的表。
// "ON CONFLICT … DO UPDATE SET" 中的 UPDATE 后跟 SET 关键字，不是表名，跳过。
// 越界属编程错误（可能跨生态误删），直接 panic。
func assertTables(query string) {
	for _, m := range tableRefRe.FindAllStringSubmatch(query, -1) {
		name := m[1]
		if strings.EqualFold(name, "SET") {
			continue
		}
		if !allowTables[name] {
			panic(fmt.Sprintf("[go-adapter] 表隔离断言失败：SQL 触达非本生态表 %q：%s", name, query))
		}
	}
}

// DB 包装从单库 Store 注入的读写两个连接池：
//   - 写池单连接（Store 持有）：平台 + 四生态共享一把写锁，database/sql 层排队；
//   - 读池多连接：多读可并发；写提交瞬间读会短暂等待（busy_timeout 兜底）。
//
// 以下状态为本生态私有：页面缓存代次（writeGen/statsGen）、高频统计内存缓冲、
// settings 进程内缓存。四生态各自的代次互不影响，页面缓存 key 天然带生态前缀。
type DB struct {
	w *sql.DB
	r *sql.DB

	// writeGen 数据代次：模块/版本/配置等展示数据的写操作递增，供页面缓存失效判断。
	writeGen atomic.Uint64
	// statsGen 统计代次：命中率累计与每日趋势写入递增。与 writeGen 分开，
	// 避免周期性的统计落库把所有页面缓存（模块列表/搜索）一起打掉。
	statsGen atomic.Uint64
	// ready 启动期回填是否完成（/healthz 暴露）。
	ready    atomic.Bool
	mem      statsMem // 高频统计内存缓冲（命中率计数 / 模块访问时间）
	settings atomic.Pointer[models.Settings]
}

// Attach 在单库 Store 上挂载 go 生态查询层。schema（含全部前缀表）已由
// Store.Open 建齐；历史数据回填放后台：大库时不必阻塞启动，回填期间读接口仍可服务。
// ready 在回填完成后才置 true（healthz 的 503 判定只看 DB 可用性，不受影响）。
func Attach(store *database.Store) *DB {
	db := &DB{w: store.WritePool(), r: store.ReadPool()}
	go db.backfillAsync()
	return db
}

// backfillAsync 后台回填历史数据（幂等，失败不影响运行，下次启动重试）。
func (db *DB) backfillAsync() {
	defer db.ready.Store(true)
	if err := db.BackfillModuleStats(); err != nil {
		log.Printf("[database] 回填模块统计列失败(忽略，下次启动重试): %v", err)
	}
	if err := db.BackfillDailyFromCache(); err != nil {
		log.Printf("[database] 回填每日缓存趋势失败(忽略，不影响运行): %v", err)
	}
}

// Close 停止刷盘 goroutine 并补一次落盘。连接池归 core Store 所有，由 Store.Close 统一关闭。
func (db *DB) Close() {
	db.mem.stopFlusher()
	db.FlushStats()
}

// WriteGen 返回当前数据代次：模块/版本/配置等展示数据的写操作会使其递增。
func (db *DB) WriteGen() uint64 { return db.writeGen.Load() }

// StatsGen 返回统计代次：命中率累计与每日趋势写入会使其递增。
func (db *DB) StatsGen() uint64 { return db.statsGen.Load() }

// Ready 报告启动期后台回填是否已完成（仅用于健康检查展示）。
func (db *DB) Ready() bool { return db.ready.Load() }

// ---------- 连接池路由：写池/读池 + 表隔离断言 + 慢查询日志 ----------

// Exec 写操作：走写池（单连接串行），成功后推进数据代次。
func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	assertTables(query)
	start := time.Now()
	res, err := db.w.Exec(query, args...)
	logSlowQuery("exec", query, start)
	if err == nil {
		db.writeGen.Add(1)
	}
	return res, err
}

// execStats 统计类写入：只推进统计代次，不影响模块列表/搜索等数据页面缓存。
func (db *DB) execStats(query string, args ...any) (sql.Result, error) {
	assertTables(query)
	start := time.Now()
	res, err := db.w.Exec(query, args...)
	logSlowQuery("exec", query, start)
	if err == nil {
		db.statsGen.Add(1)
	}
	return res, err
}

// execNoGen 与 Exec 相同，但不推进写代次。
// 用于缓存时间戳这类不影响页面展示数据的写入：否则每次元数据 304 刷新都会让
// 首页/列表/搜索的 30s 页面缓存全量失效，短 TTL 的收益被抵消。
func (db *DB) execNoGen(query string, args ...any) (sql.Result, error) {
	assertTables(query)
	start := time.Now()
	res, err := db.w.Exec(query, args...)
	logSlowQuery("exec", query, start)
	return res, err
}

// Query 读操作：走读池（多连接，多读并发；写提交瞬间由 busy_timeout 兜底等待）。
func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	assertTables(query)
	start := time.Now()
	rows, err := db.r.Query(query, args...)
	logSlowQuery("query", query, start)
	return rows, err
}

// QueryRow 读操作：走读池。错误延迟到 Scan 才可见，故此处不判错。
func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	assertTables(query)
	start := time.Now()
	row := db.r.QueryRow(query, args...)
	logSlowQuery("query_row", query, start)
	return row
}

// Begin 写事务：走写池，返回带表隔离断言的事务句柄。
func (db *DB) Begin() (*guardTx, error) {
	start := time.Now()
	tx, err := db.w.Begin()
	logSlowQuery("begin", "BEGIN", start)
	if err != nil {
		return nil, err
	}
	return &guardTx{tx: tx}, nil
}

// guardTx 带表隔离断言的事务句柄：Exec/QueryRow/Prepare 在执行前先断言表白名单。
// Prepare 返回的语句在 Prepare 时已校验，stmt.Exec 不再重复断言。
type guardTx struct{ tx *sql.Tx }

func (t *guardTx) Exec(query string, args ...any) (sql.Result, error) {
	assertTables(query)
	return t.tx.Exec(query, args...)
}

func (t *guardTx) QueryRow(query string, args ...any) *sql.Row {
	assertTables(query)
	return t.tx.QueryRow(query, args...)
}

func (t *guardTx) Prepare(query string) (*sql.Stmt, error) {
	assertTables(query)
	return t.tx.Prepare(query)
}

func (t *guardTx) Commit() error   { return t.tx.Commit() }
func (t *guardTx) Rollback() error { return t.tx.Rollback() }

func logSlowQuery(kind, query string, start time.Time) {
	if d := time.Since(start); d >= slowQueryThreshold {
		q := query
		if len(q) > 160 {
			q = q[:160] + "..."
		}
		log.Printf("[database] 慢查询 %s 耗时 %v: %s", kind, d.Round(time.Millisecond), q)
	}
}

// Ping 检查读写连接池可用性（healthz 健康检查用）。
func (db *DB) Ping() error {
	if err := db.r.Ping(); err != nil {
		return err
	}
	return db.w.Ping()
}

// Optimize 让 SQLite 重新分析索引统计（定期调用，代价很低）。
func (db *DB) Optimize() {
	_, _ = db.w.Exec(`PRAGMA optimize`)
}

// BackfillModuleStats 全量重算模块物化统计列（启动回填与清理后修正历史漂移）。
// 单条 UPDATE + 关联子查询完成，避免逐模块往返。
func (db *DB) BackfillModuleStats() error {
	_, err := db.Exec(`UPDATE go_modules SET
		version_count = (SELECT COUNT(*) FROM go_versions v WHERE v.module_id = go_modules.id),
		cached_version_count = (SELECT COUNT(*) FROM go_versions v WHERE v.module_id = go_modules.id AND v.zip_size_bytes > 0),
		cached_total_size = (SELECT COALESCE(SUM(v.zip_size_bytes),0) FROM go_versions v WHERE v.module_id = go_modules.id AND v.zip_size_bytes > 0),
		last_cached_at = (SELECT COALESCE(MAX(v.zip_cached_at),'') FROM go_versions v WHERE v.module_id = go_modules.id AND v.zip_size_bytes > 0)`)
	return err
}
