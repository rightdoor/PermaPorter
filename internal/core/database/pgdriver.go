// PostgreSQL 驱动接入：包装 pgx stdlib 的 Connector/Conn，所有语句（含事务与
// 预编译）执行前经 rewriteForPostgres 改写，SQLite 方言 SQL 无需改动即可在 PG 上运行。
//
// 这是方言分派的唯一发生地：adapter 与协议层只面向双方言兼容 SQL（或 SQLite 形态），
// 是否改写由 Store 打开的连接池驱动决定。PRAGMA 语句在 PG 上转为 no-op 成功
// （各生态 Optimize 的周期性调用无需按方言分支）。
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// pgHandshakeTimeout 启动校验时的 PG 握手预算。
const pgHandshakeTimeout = 10 * time.Second

// openPostgresDB 打开一个经改写包装的 PG 连接池（不建 schema）。
func openPostgresDB(dsn string) (*sql.DB, error) {
	dc, ok := stdlib.GetDefaultDriver().(driver.DriverContext)
	if !ok {
		return nil, fmt.Errorf("pgx 驱动不支持 Connector 接口")
	}
	base, err := dc.OpenConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析 PostgreSQL 连接串失败: %w", err)
	}
	return sql.OpenDB(&pgConnector{inner: base}), nil
}

// OpenPostgreSQL 打开（必要时创建）PostgreSQL 单库并确保 schema 存在。
// 连接失败视为启动校验失败（调用方拒绝启动）。连接池与 SQLite 同构：
// 写池单连接（平台 + 四生态共享一把写锁，语义与 SQLite 侧一致）、读池 4 连接。
func OpenPostgreSQL(dsn string) (*Store, error) {
	dw, err := openPostgresDB(dsn)
	if err != nil {
		return nil, err
	}
	dw.SetMaxOpenConns(1)
	dw.SetMaxIdleConns(1)
	dr, err := openPostgresDB(dsn)
	if err != nil {
		dw.Close()
		return nil, err
	}
	dr.SetMaxOpenConns(4)
	dr.SetMaxIdleConns(4)
	s := &Store{w: dw, r: dr, dialect: DialectPostgreSQL}
	// 启动校验：PG 握手必须成功（host/port/密码错误、网络不可达 → 拒绝启动）
	ctx, cancel := context.WithTimeout(context.Background(), pgHandshakeTimeout)
	defer cancel()
	if err := dw.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败（检查 host/port/user/password 与网络连通性）: %w", err)
	}
	if err := dr.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败（读连接池）: %w", err)
	}
	if err := s.ensureSchema(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// ProbePostgreSQL 仅测试 PG 连接可用性（向导"测试连接"），不建库建表。
func ProbePostgreSQL(dsn string) error {
	db, err := openPostgresDB(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), pgHandshakeTimeout)
	defer cancel()
	return db.PingContext(ctx)
}

// ProbeSQLite 仅测试 SQLite 可建库/可连通（向导"测试连接"）：打开驱动连接并 Ping，
// 不建 schema。父目录不存在时先创建（与 Open 的行为一致——全新部署时 data/ 尚未建立，
// "可建库"本身就是探测目标）。文件不存在时会创建空文件（0 字节），
// 随后 initialize 的 Open 正常建库。
func ProbeSQLite(dsn string) error {
	if path, err := sqliteFilePath(dsn); err == nil {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("创建数据目录失败: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", normalizeDSN(dsn, false))
	if err != nil {
		return fmt.Errorf("打开 SQLite 失败: %w", err)
	}
	defer db.Close()
	return db.Ping()
}

// ---------- 驱动包装器 ----------

// pgConnector 包装 pgx stdlib Connector：Connect 返回的连接同样被包装。
type pgConnector struct{ inner driver.Connector }

func (c *pgConnector) Connect(ctx context.Context) (driver.Conn, error) {
	raw, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pgConn{inner: raw}, nil
}

func (c *pgConnector) Driver() driver.Driver { return c.inner.Driver() }

// pgConn 包装 pgx stdlib Conn：Exec/Query/Prepare 前改写 SQL；PRAGMA 转 no-op。
// 其余可选接口（BeginTx/Ping/ResetSession/CheckNamedValue 等）逐项转发给内层，
// 保证 database/sql 的连接管理与 pgx 的预编译缓存行为不受包装影响。
type pgConn struct{ inner driver.Conn }

func (c *pgConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(rewriteForPostgres(query))
}

func (c *pgConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, rewriteForPostgres(query))
	}
	return c.Prepare(query)
}

func (c *pgConn) Close() error { return c.inner.Close() }

func (c *pgConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *pgConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Begin()
}

func (c *pgConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if isPragma(query) {
		return driver.RowsAffected(0), nil // PRAGMA optimize 等：PG 侧 no-op
	}
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, rewriteForPostgres(query), args)
	}
	return nil, driver.ErrSkip // 交由 database/sql 走 Prepare 路径（同样被改写）
}

func (c *pgConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, rewriteForPostgres(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *pgConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *pgConn) CheckNamedValue(nv *driver.NamedValue) error {
	if v, ok := c.inner.(driver.NamedValueChecker); ok {
		return v.CheckNamedValue(nv)
	}
	return nil
}

func (c *pgConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
