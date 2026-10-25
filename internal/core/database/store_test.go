package database

import (
	"errors"
	"path/filepath"
	"testing"
)

// testDSN 临时目录内的单库 DSN（参数与生产默认一致）。
func testDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "permaporter.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
}

// TestOpenCreatesSchema 首次打开空库：建齐全部表与索引，并写入 schema_version 三行。
func TestOpenCreatesSchema(t *testing.T) {
	s, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("打开空库不应失败: %v", err)
	}
	defer s.Close()

	wantTables := map[string]bool{
		"platform_settings": false, "ecosystem_settings": false, "schema_version": false,
		"go_modules": false, "go_versions": false, "go_status": false, "go_daily_stats": false,
		"npm_packages": false, "npm_versions": false, "npm_status": false, "npm_daily_stats": false,
		"py_projects": false, "py_files": false, "py_status": false, "py_daily_stats": false,
		"mvn_artifacts": false, "mvn_metadata": false, "mvn_files": false, "mvn_status": false, "mvn_daily_stats": false,
	}
	rows, err := s.r.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, ok := wantTables[name]; !ok {
			t.Errorf("出现非目标表 %q", name)
		} else {
			wantTables[name] = true
		}
	}
	rows.Close()
	for name, ok := range wantTables {
		if !ok {
			t.Errorf("缺少目标表 %q", name)
		}
	}

	for _, c := range schemaComponents {
		var version int
		var appliedAt string
		if err := s.r.QueryRow(
			`SELECT version, applied_at FROM schema_version WHERE component = ?`, c.Name,
		).Scan(&version, &appliedAt); err != nil {
			t.Fatalf("schema_version 缺少组件 %s: %v", c.Name, err)
		}
		if version != c.Version {
			t.Errorf("组件 %s 版本应为 %d，实际 %d", c.Name, c.Version, version)
		}
		if appliedAt == "" {
			t.Errorf("组件 %s 缺少初始化完成时间", c.Name)
		}
	}
}

// TestOpenIdempotent 已初始化的库再次打开不应重复建表或报错。
func TestOpenIdempotent(t *testing.T) {
	dsn := testDSN(t)
	s1, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SetPasswordHash("hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("重新打开不应失败: %v", err)
	}
	defer s2.Close()
	hash, err := s2.PasswordHash()
	if err != nil || hash != "hash-1" {
		t.Fatalf("已有数据不应丢失: hash=%q err=%v", hash, err)
	}
}

// TestOpenAutoAddsNewComponent 可加性演进：已有单库缺少新生态组件行时，
// 启动自动建表并补写版本行（幂等，不拒启、不影响既有数据）。
func TestOpenAutoAddsNewComponent(t *testing.T) {
	dsn := testDSN(t)
	s1, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SetPasswordHash("keep-me"); err != nil {
		t.Fatal(err)
	}
	// 模拟未含 maven 组件的存量单库（表与 schema_version 行均不存在），校验建库可加性演进补齐
	if _, err := s1.w.Exec(`DROP TABLE mvn_files; DROP TABLE mvn_metadata; DROP TABLE mvn_artifacts; DROP TABLE mvn_status; DROP TABLE mvn_daily_stats;
		DELETE FROM schema_version WHERE component = 'maven'`); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("缺组件行的库应自动补齐而不是拒启: %v", err)
	}
	defer s2.Close()
	var version int
	if err := s2.r.QueryRow(`SELECT version FROM schema_version WHERE component = 'maven'`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("maven 组件行应自动补齐: version=%d err=%v", version, err)
	}
	var n int
	if err := s2.r.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mvn_artifacts'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("mvn 表应已重建: n=%d err=%v", n, err)
	}
	if hash, _ := s2.PasswordHash(); hash != "keep-me" {
		t.Fatalf("既有数据不应丢失: %q", hash)
	}
}

// TestOpenRejectsSchemaMismatch schema_version 缺失/不匹配的库必须拒绝打开。
func TestOpenRejectsSchemaMismatch(t *testing.T) {
	dsn := testDSN(t)

	// 非本服务初始化的库（有表但无 schema_version）
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`DROP TABLE schema_version`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(dsn); err == nil {
		t.Fatal("缺少 schema_version 的库应拒绝打开")
	}

	// 版本不匹配
	dsn2 := testDSN(t)
	s2, err := Open(dsn2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.w.Exec(`UPDATE schema_version SET version = 99 WHERE component = 'go'`); err != nil {
		t.Fatal(err)
	}
	s2.Close()
	_, err = Open(dsn2)
	var mismatch *SchemaMismatchError
	if err == nil || !errors.As(err, &mismatch) {
		t.Fatalf("schema 版本不匹配应返回 SchemaMismatchError，实际 %v", err)
	}
}

// TestOpenRejectsUnknownComponent schema_version 含未知组件同样拒绝。
func TestOpenRejectsUnknownComponent(t *testing.T) {
	dsn := testDSN(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`INSERT INTO schema_version (component, version, applied_at) VALUES ('other', 1, '')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(dsn); err == nil {
		t.Fatal("含未知组件的 schema_version 应拒绝打开")
	}
}

// TestPasswordLifecycle 管理员密码写入/读取/Touch 全链路（存 platform_settings 单行）。
func TestPasswordLifecycle(t *testing.T) {
	s, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.IsInitialized() {
		t.Fatal("空库不应视为已初始化")
	}
	if err := s.SetPasswordHash("$2a$10$placeholder"); err != nil {
		t.Fatalf("写入密码失败: %v", err)
	}
	if !s.IsInitialized() {
		t.Fatal("写入密码后应视为已初始化")
	}
	hash, err := s.PasswordHash()
	if err != nil || hash != "$2a$10$placeholder" {
		t.Fatalf("读回密码不一致: %q %v", hash, err)
	}
	if err := s.Touch(); err != nil {
		t.Fatalf("Touch 失败: %v", err)
	}
	// 覆盖更新
	if err := s.SetPasswordHash("second"); err != nil {
		t.Fatal(err)
	}
	if hash, _ := s.PasswordHash(); hash != "second" {
		t.Fatalf("覆盖后密码应为 second，实际 %q", hash)
	}
}
