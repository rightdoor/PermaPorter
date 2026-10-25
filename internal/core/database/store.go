// Package database 维护单库 data/permaporter.db：平台与四个生态共用一个 SQLite 文件。
//
// 生态隔离靠表名前缀（go_* / npm_* / py_* / mvn_*）而非 ecosystem 判别列：判别列漏写即跨生态
// 误删；前缀让错表在评审期暴露。各生态的 versions 表不合并，四套清理语义不通用化。
// 存储目录（四棵树）不在库内共享，仍由 config 启动校验互异。
//
// 密码统一存 platform_settings（单行 id=1）；生态配置存 ecosystem_settings
// （go / npm / py / mvn 各一行）。首次创建库时写入 schema_version 标记初始化完成时间。
package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（不依赖 CGO，静态单二进制）
)

// Store 单库句柄：平台与四生态共用同一数据库的读写连接池。
//   - 写池单连接：串行化全部写入（平台 + 四生态共享一把写锁），杜绝 SQLITE_BUSY；
//   - 读池多连接：多读可并发；写提交瞬间读会短暂等待（busy_timeout 兜底），
//     DELETE 回滚日志模式下运行期只存在单个 .db 文件。
//
// dialect 标识底层驱动（SQLite / PostgreSQL）：PRAGMA、busy_timeout、DELETE 日志
// 模式等仅作用于 sqlite；PG 侧的方言差异全部收敛在 dialect.go / pgdriver.go。
type Store struct {
	w       *sql.DB
	r       *sql.DB
	dialect Dialect

	// initMu/initCached：IsInitialized 的正向结果缓存。控制台的 State/SPA/浏览器
	// 404 回落每请求都要读该状态，而"已初始化"在进程生命周期内只会从 false 变 true
	// （密码可改但哈希永不为空），缓存正向结果可消除稳态下的每请求 DB 往返。
	initMu     sync.Mutex
	initCached bool
}

// Dialect 数据库方言。
type Dialect string

const (
	DialectSQLite     Dialect = "sqlite"
	DialectPostgreSQL Dialect = "postgresql"
)

// Dialect 返回当前单库方言。
func (s *Store) Dialect() Dialect { return s.dialect }

// Open 打开（必要时创建）SQLite 单库并确保 schema 存在。
// DSN 支持 file:<path>?<params> 形态或纯路径；参数取路径部分，
// PRAGMA 在连接层显式设置（DELETE 回滚日志模式，无 -wal/-shm 文件）。
// 首次以 DELETE 模式打开曾处于 WAL 模式的库文件时，SQLite 会自动转换日志模式
// 并删除 -wal/-shm 文件。FULL 在回滚日志模式下保证断电不损坏。
func Open(dsn string) (*Store, error) {
	path, err := sqliteFilePath(dsn)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	dw, err := sql.Open("sqlite", normalizeDSN(dsn, false))
	if err != nil {
		return nil, fmt.Errorf("打开单库失败: %w", err)
	}
	// 写池单连接：database/sql 层排队，杜绝 SQLITE_BUSY
	dw.SetMaxOpenConns(1)
	dw.SetMaxIdleConns(1)
	// 日志模式持久化在 DB 文件中，此处显式确保一次
	for _, pragma := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		// 开启外键约束检查 — 保证 ON DELETE CASCADE 级联删除生态 versions 记录
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := dw.Exec(pragma); err != nil {
			dw.Close()
			return nil, fmt.Errorf("单库 PRAGMA 失败: %w", err)
		}
	}
	dr, err := sql.Open("sqlite", normalizeDSN(dsn, true))
	if err != nil {
		dw.Close()
		return nil, fmt.Errorf("打开单库读连接失败: %w", err)
	}
	dr.SetMaxOpenConns(4)
	dr.SetMaxIdleConns(4)
	s := &Store{w: dw, r: dr, dialect: DialectSQLite}
	if err := s.ensureSchema(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// ensureSchema 空库时建全部表/索引并写入 schema_version（标记初始化完成时间）；
// 库非空时核对 schema_version：版本不匹配即拒绝启动（开发期不迁移，
// 提示执行 make db-reset 重置后按目标 schema 重建）。
// 新增生态的组件行缺失属可加性演进：自动建表并补写行（幂等），不拒启。
func (s *Store) ensureSchema() error {
	empty, err := s.isEmpty()
	if err != nil {
		return err
	}
	if empty {
		return s.createSchema()
	}
	return s.verifySchemaVersion()
}

// execDDL 依次执行一个组件的建表/建索引语句。
func (s *Store) execDDL(ddl []string) error {
	for _, stmt := range ddl {
		if _, err := s.w.Exec(stmt); err != nil {
			return fmt.Errorf("单库 schema 初始化失败: %w", err)
		}
	}
	return nil
}

// insertSchemaVersionRow 写入单个组件的版本标记行。
func (s *Store) insertSchemaVersionRow(name string, version int) error {
	_, err := s.w.Exec(
		`INSERT INTO schema_version (component, version, applied_at) VALUES (?, ?, ?)`,
		name, version, time.Now().Format(time.DateTime))
	if err != nil {
		return fmt.Errorf("写入 schema_version 失败: %w", err)
	}
	return nil
}

// createSchema 逐组件建表/建索引并写入 schema_version 行。
func (s *Store) createSchema() error {
	for _, c := range schemaComponents {
		if err := s.execDDL(c.DDL); err != nil {
			return err
		}
		if err := s.insertSchemaVersionRow(c.Name, c.Version); err != nil {
			return err
		}
	}
	return nil
}

// verifySchemaVersion 校验已存在库的 schema_version：
// 已有组件版本必须一致；缺失组件按可加性演进自动建表补行；未知组件拒绝。
func (s *Store) verifySchemaVersion() error {
	rows, err := s.r.Query(`SELECT component, version FROM schema_version`)
	if err != nil {
		return fmt.Errorf("单库缺少 schema_version（不是本服务初始化的库）: %w", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var name string
		var version int
		if err := rows.Scan(&name, &version); err != nil {
			return err
		}
		got[name] = version
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range schemaComponents {
		version, ok := got[c.Name]
		if !ok {
			// 可加性演进：新生态组件缺失 → 建对应表并补写版本行（幂等）
			if err := s.ensureComponent(c.Name, c.Version); err != nil {
				return err
			}
			log.Printf("[database] schema 可加性演进：已自动补齐组件 %q（版本 %d）", c.Name, c.Version)
			continue
		}
		if version != c.Version {
			return &SchemaMismatchError{Detail: fmt.Sprintf("组件 %q schema 版本为 %d，要求 %d", c.Name, version, c.Version)}
		}
		delete(got, c.Name)
	}
	if len(got) > 0 {
		return &SchemaMismatchError{Detail: fmt.Sprintf("schema_version 含未知组件 %v", keys(got))}
	}
	return nil
}

// ensureComponent 建单个组件的全部表/索引并写入其版本行（幂等，调用方需确认该组件行缺失）。
func (s *Store) ensureComponent(name string, version int) error {
	for _, c := range schemaComponents {
		if c.Name != name {
			continue
		}
		if err := s.execDDL(c.DDL); err != nil {
			return err
		}
		return s.insertSchemaVersionRow(name, version)
	}
	return &SchemaMismatchError{Detail: fmt.Sprintf("schema_version 缺少未知组件 %q 的建表定义", name)}
}

// SchemaMismatchError 单库 schema 版本不匹配：拒绝启动，需执行 make db-reset。
type SchemaMismatchError struct{ Detail string }

func (e *SchemaMismatchError) Error() string {
	return e.Detail + "（开发期不做数据迁移，请停止服务后执行 make db-reset 重置数据库，重置将丢弃库内全部数据）"
}

func keys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isEmpty 判断库中是否还没有任何用户表。SQLite 查 sqlite_master；
// PostgreSQL 查当前 schema 下的基表（两者都排除系统/驱动内部表）。
func (s *Store) isEmpty() (bool, error) {
	var n int
	var err error
	if s.dialect == DialectPostgreSQL {
		err = s.r.QueryRow(
			`SELECT COUNT(*) FROM information_schema.tables
			 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`,
		).Scan(&n)
	} else {
		err = s.r.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`,
		).Scan(&n)
	}
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// WritePool 暴露写池（单连接，串行写）给生态查询层挂载。调用方不得关闭。
func (s *Store) WritePool() *sql.DB { return s.w }

// ReadPool 暴露读池（多连接，连接级 query_only）给生态查询层挂载。调用方不得关闭。
func (s *Store) ReadPool() *sql.DB { return s.r }

// Close 关闭单库读写连接池。
func (s *Store) Close() error {
	errW := s.w.Close()
	errR := s.r.Close()
	if errW != nil {
		return errW
	}
	return errR
}

// ---------- 平台凭据（platform_settings，单行 id=1） ----------

// IsInitialized 是否已设置管理员密码。
// 正向结果按"一旦初始化即永久初始化"缓存（SetPasswordHash 成功后同样置位）；
// 未初始化或读库失败时保持查库，保证 setup 流程即时生效。
func (s *Store) IsInitialized() bool {
	s.initMu.Lock()
	cached := s.initCached
	s.initMu.Unlock()
	if cached {
		return true
	}
	hash, err := s.PasswordHash()
	ok := err == nil && hash != ""
	if ok {
		s.initMu.Lock()
		s.initCached = true
		s.initMu.Unlock()
	}
	return ok
}

// PasswordHash 读取管理员密码哈希。
func (s *Store) PasswordHash() (string, error) {
	var hash string
	err := s.r.QueryRow(`SELECT admin_password_hash FROM platform_settings WHERE id = 1`).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}

// SetPasswordHash 写入管理员密码哈希并刷新 updated_at。
func (s *Store) SetPasswordHash(hash string) error {
	_, err := s.w.Exec(`INSERT INTO platform_settings (id, admin_password_hash, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET admin_password_hash = excluded.admin_password_hash,
		                               updated_at = excluded.updated_at`,
		hash, time.Now().Format(time.DateTime))
	if err == nil {
		s.initMu.Lock()
		s.initCached = true
		s.initMu.Unlock()
	}
	return err
}

// Touch 仅刷新 updated_at（登录等管理活动时间戳）。
func (s *Store) Touch() error {
	_, err := s.w.Exec(`UPDATE platform_settings SET updated_at = ? WHERE id = 1`, time.Now().Format(time.DateTime))
	return err
}

// InitPasswordHash 首次初始化写入密码哈希：仅当当前哈希为空时写入，已初始化返回
// false。单条条件 upsert 在写池单连接上原子执行，消除 setup 接口"两个并发请求
// 都通过 IsInitialized 检查后各自写入"的竞写窗口。
func (s *Store) InitPasswordHash(hash string) (bool, error) {
	res, err := s.w.Exec(`INSERT INTO platform_settings (id, admin_password_hash, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET admin_password_hash = excluded.admin_password_hash,
		                               updated_at = excluded.updated_at
		WHERE platform_settings.admin_password_hash = ''`,
		hash, time.Now().Format(time.DateTime))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	s.initMu.Lock()
	s.initCached = true
	s.initMu.Unlock()
	return true, nil
}

// sqliteFilePath 从 DSN 提取 SQLite 文件路径（file:<path>?<params> 或纯路径）。
func sqliteFilePath(dsn string) (string, error) {
	p := dsn
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, "file:")
	p = filepath.Clean(p)
	if p == "." || p == string(filepath.Separator) || p == "" {
		return "", fmt.Errorf("非法 SQLite DSN: %q", dsn)
	}
	return p, nil
}
