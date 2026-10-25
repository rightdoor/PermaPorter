package database

import (
	"path/filepath"
	"testing"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/npmadapter/models"
)

// testDB 打开一个临时单库并挂载 npm 生态查询层（DSN 与生产默认一致）。
func testDB(t *testing.T) *DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "t.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db := Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

// TestReadPoolIsReadOnly 校验读池的 query_only 真正生效（驱动须遵守 _query_only 连接参数）。
func TestReadPoolIsReadOnly(t *testing.T) {
	db := testDB(t)
	if _, err := db.r.Exec(`INSERT INTO npm_packages (pkg_name) VALUES ('readonly-probe')`); err == nil {
		t.Fatal("读池不应允许写入")
	}
	// 写池正常
	if _, err := db.w.Exec(`INSERT INTO npm_packages (pkg_name) VALUES ('write-probe')`); err != nil {
		t.Fatalf("写池写入失败: %v", err)
	}
}

// TestWritePoolForeignKeysOn 校验外键级联删除可用（依赖 PRAGMA foreign_keys）。
func TestWritePoolForeignKeysOn(t *testing.T) {
	db := testDB(t)
	pkgID, err := db.UpsertPackage(&models.Package{PkgName: "cascade-demo", MetaCachedAt: "2026-01-01 00:00:00"})
	if err != nil {
		t.Fatalf("upsert package: %v", err)
	}
	if err := db.UpsertVersions(pkgID, []models.Version{{Version: "1.0.0", TarballPath: "p/1.tgz"}}); err != nil {
		t.Fatalf("upsert versions: %v", err)
	}
	if err := db.DeletePackage("cascade-demo"); err != nil {
		t.Fatalf("delete package: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM npm_versions WHERE pkg_id = ?`, pkgID).Scan(&n); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if n != 0 {
		t.Fatalf("删除包后版本记录应被级联删除，剩余 %d 条", n)
	}
}

// TestAssertTablesRejectsForeignTable 表隔离断言：触达非本生态前缀表必须 panic。
func TestAssertTablesRejectsForeignTable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("触达 go_modules 应回收断言 panic")
		}
	}()
	assertTables(`SELECT * FROM go_modules WHERE module_path = ?`)
}

// TestAssertTablesAllowsOwnTables 本生态前缀表与共享配置表放行。
func TestAssertTablesAllowsOwnTables(t *testing.T) {
	assertTables(`SELECT p.pkg_name FROM npm_packages p JOIN npm_versions v ON v.pkg_id = p.id`)
	assertTables(`UPDATE ecosystem_settings SET upstream_url = ? WHERE ecosystem = 'npm'`)
	assertTables(`INSERT INTO npm_daily_stats (day, tarball_total) VALUES (?, ?)`)
	assertTables(`DELETE FROM npm_status WHERE id = 1`)
	assertTables(`UPDATE npm_packages SET meta_cached_at = CURRENT_TIMESTAMP WHERE pkg_name = ?`)
}
