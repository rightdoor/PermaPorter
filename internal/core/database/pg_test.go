package database

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// pgDSN 集成测试数据库（未设置 TEST_PG_DSN 时跳过：PG 不可自动拉起）。
// 本地验证：docker compose --profile postgres up -d postgres 后
//
//	TEST_PG_DSN="postgres://permaporter:permaporter@127.0.0.1:5432/permaporter?sslmode=disable" go test ./internal/core/database/ -run PG
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_PG_DSN，跳过 PostgreSQL 集成测试")
	}
	return dsn
}

// dropAllTables 清空当前 schema（每个用例独立建库起点）。
func dropAllTables(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.w.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("清理 schema 失败: %v", err)
	}
}

// TestPGOpenCreatesSchema PG 空库建齐全部表 + schema_version 五行。
func TestPGOpenCreatesSchema(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatalf("打开 PG 失败: %v", err)
	}
	dropAllTables(t, s)
	s.Close()

	s, err = OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	defer s.Close()
	if s.Dialect() != DialectPostgreSQL {
		t.Fatalf("方言标记错误: %s", s.Dialect())
	}
	var n int
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name IN (
		'platform_settings','ecosystem_settings','schema_version',
		'go_modules','go_versions','go_status','go_daily_stats',
		'npm_packages','npm_versions','npm_status','npm_daily_stats',
		'py_projects','py_files','py_status','py_daily_stats',
		'mvn_artifacts','mvn_metadata','mvn_files','mvn_status','mvn_daily_stats')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Fatalf("应建齐 20 张表，实际 %d", n)
	}
	// 身份列自增可用（adapter 不指定 id 插入）
	res, err := s.w.Exec(`INSERT INTO go_modules (module_path) VALUES ('example.com/m')`)
	if err != nil {
		t.Fatalf("自增插入失败: %v", err)
	}
	_ = res
	var id int64
	if err := s.r.QueryRow(`SELECT id FROM go_modules WHERE module_path = 'example.com/m'`).Scan(&id); err != nil || id == 0 {
		t.Fatalf("身份列应自增分配: id=%d err=%v", id, err)
	}
}

// TestPGPasswordAndUpserts 平台凭据 upsert 语义（InitPasswordHash 条件写入）与生态配置行 upsert。
func TestPGPasswordAndUpserts(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dropAllTables(t, s)
	s.Close()
	s, err = OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.IsInitialized() {
		t.Fatal("空库不应已初始化")
	}
	init, err := s.InitPasswordHash("hash-1")
	if err != nil || !init {
		t.Fatalf("首次写入失败: %v %v", init, err)
	}
	// 已初始化后再次 Init 不覆盖（条件 upsert WHERE 哈希为空）
	init, err = s.InitPasswordHash("hash-2")
	if err != nil {
		t.Fatal(err)
	}
	if init {
		t.Fatal("重复初始化不应成功")
	}
	hash, _ := s.PasswordHash()
	if hash != "hash-1" {
		t.Fatalf("哈希不应被覆盖: %s", hash)
	}
	if !s.IsInitialized() {
		t.Fatal("应已初始化")
	}
	if err := s.SetPasswordHash("hash-3"); err != nil {
		t.Fatalf("改密失败: %v", err)
	}
	// 生态配置行 upsert（adapter settings.go 的 portable ON CONFLICT 形态）
	for _, stmt := range []string{
		`INSERT INTO ecosystem_settings (ecosystem, upstream_url, fallback_url, outbound_proxy, storage_dir, meta_ttl)
		 VALUES ('go', 'https://proxy.golang.org', '', '', './data/storage-go', 120000)
		 ON CONFLICT(ecosystem) DO UPDATE SET
			upstream_url = excluded.upstream_url, fallback_url = excluded.fallback_url,
			outbound_proxy = excluded.outbound_proxy, storage_dir = excluded.storage_dir,
			meta_ttl = excluded.meta_ttl`,
		`INSERT INTO go_status (id) VALUES (1) ON CONFLICT DO NOTHING`,
		`UPDATE go_status SET meta_total = meta_total + 1, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
	} {
		if _, err := s.w.Exec(stmt); err != nil {
			t.Fatalf("portable SQL 在 PG 失败: %v\n%s", err, stmt)
		}
	}
	var total int64
	if err := s.r.QueryRow(`SELECT meta_total FROM go_status WHERE id = 1`).Scan(&total); err != nil || total != 1 {
		t.Fatalf("统计累加错误: %d %v", total, err)
	}
	// LIKE → ILIKE：大小写不敏感搜索与 SQLite 行为一致
	if _, err := s.w.Exec(`INSERT INTO go_modules (module_path, summary) VALUES ('Example.COM/Case', 'x')`); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM go_modules WHERE module_path LIKE 'example.com/%' ESCAPE '\'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ILIKE 大小写不敏感失败: n=%d err=%v", n, err)
	}
}

// TestPGSchemaVersionRoundTrip 可加性演进：删除单组件行后重开自动补齐。
func TestPGSchemaVersionRoundTrip(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dropAllTables(t, s)
	s.Close()

	s, _ = OpenPostgreSQL(dsn)
	if _, err := s.w.Exec(`DELETE FROM schema_version WHERE component = 'maven'`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = OpenPostgreSQL(dsn) // 可加性演进：自动补 mvn 组件
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer s.Close()
	var n int
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM mvn_artifacts`).Scan(&n); err != nil {
		t.Fatalf("mvn_artifacts 应已补建: %v", err)
	}
}

// TestPGSumScanInt64 SUM 改写为 ::int8 后可扫描进 int64（numeric 直扫会失败）。
func TestPGSumScanInt64(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dropAllTables(t, s)
	s.Close()
	s, err = OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.w.Exec(`INSERT INTO go_versions (module_id, version, zip_size_bytes)
		SELECT id, 'v1.0.0', 1024 FROM go_modules`); err != nil {
		t.Fatal(err)
	}
	var total int64
	err = s.r.QueryRow(`SELECT (SELECT COALESCE(SUM(zip_size_bytes),0) FROM go_versions WHERE zip_size_bytes > 0)`).Scan(&total)
	if err != nil {
		t.Fatalf("SUM 扫描 int64 失败（::int8 改写未生效？）: %v", err)
	}
	if total != 1024 {
		t.Fatalf("SUM 值错误: %d", total)
	}
}

// TestPGUnreachable 不可达地址：OpenPostgreSQL 返回握手错误（启动校验拒绝启动的依据）。
func TestPGUnreachable(t *testing.T) {
	dsn := pgDSN(t)
	_ = dsn // 仅需环境可用性确认；用本地不监听端口模拟不可达
	bad := "postgres://nouser:nopass@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=2"
	if _, err := OpenPostgreSQL(bad); err == nil {
		t.Fatal("不可达 PG 应返回错误")
	} else if !strings.Contains(fmt.Sprint(err), "连接 PostgreSQL 失败") {
		t.Fatalf("错误应标注连接失败: %v", err)
	}
}

// TestProbePostgreSQL Probe 仅测连通不建表。
func TestProbePostgreSQL(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dropAllTables(t, s)
	s.Close()
	if err := ProbePostgreSQL(dsn); err != nil {
		t.Fatalf("Probe 应成功: %v", err)
	}
	if err := ProbePostgreSQL("postgres://u:p@127.0.0.1:1/d?sslmode=disable&connect_timeout=2"); err == nil {
		t.Fatal("Probe 不可达应失败")
	}
}

// TestPGTimestampFormat CURRENT_TIMESTAMP 改写后写入的时间串格式与 SQLite 一致（UTC 秒级）。
func TestPGTimestampFormat(t *testing.T) {
	dsn := pgDSN(t)
	s, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dropAllTables(t, s)
	s.Close()
	s, err = OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ts string
	if err := s.r.QueryRow(`SELECT ` + pgNowExpr).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.DateTime, ts); err != nil {
		t.Fatalf("时间串格式应为 YYYY-MM-DD HH:MM:SS: %q %v", ts, err)
	}
}
