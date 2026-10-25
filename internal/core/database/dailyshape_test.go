package database

import (
	"os"
	"path/filepath"
	"testing"
)

// dailyUpsertShapes 四生态 BumpDaily 的 upsert 语句（裸左值 + 表限定右侧）。
// 与 internal/{go,npm,py,mvn}adapter/database/daily.go 保持一致：
// PG 对 DO UPDATE SET 右侧裸列报 42702 歧义（目标表与 excluded 同名），
// 限定目标表名后双方言通用；SQLite 不接受限定左值，左值必须裸列。
var dailyUpsertShapes = []string{
	`INSERT INTO go_daily_stats (day, meta_total, meta_hits, zip_total, zip_hits)
	 VALUES ('2030-01-01', 1, 1, 1, 1)
	 ON CONFLICT(day) DO UPDATE SET
		meta_total = go_daily_stats.meta_total + excluded.meta_total,
		meta_hits = go_daily_stats.meta_hits + excluded.meta_hits,
		zip_total = go_daily_stats.zip_total + excluded.zip_total,
		zip_hits = go_daily_stats.zip_hits + excluded.zip_hits`,
	`INSERT INTO npm_daily_stats (day, meta_total, meta_hits, tarball_total, tarball_hits)
	 VALUES ('2030-01-01', 1, 1, 1, 1)
	 ON CONFLICT(day) DO UPDATE SET
		meta_total = npm_daily_stats.meta_total + excluded.meta_total,
		meta_hits = npm_daily_stats.meta_hits + excluded.meta_hits,
		tarball_total = npm_daily_stats.tarball_total + excluded.tarball_total,
		tarball_hits = npm_daily_stats.tarball_hits + excluded.tarball_hits`,
	`INSERT INTO py_daily_stats (day, index_total, index_hits, file_total, file_hits)
	 VALUES ('2030-01-01', 1, 1, 1, 1)
	 ON CONFLICT(day) DO UPDATE SET
		index_total = py_daily_stats.index_total + excluded.index_total,
		index_hits = py_daily_stats.index_hits + excluded.index_hits,
		file_total = py_daily_stats.file_total + excluded.file_total,
		file_hits = py_daily_stats.file_hits + excluded.file_hits`,
	`INSERT INTO mvn_daily_stats (day, meta_total, meta_hits, file_total, file_hits)
	 VALUES ('2030-01-01', 1, 1, 1, 1)
	 ON CONFLICT(day) DO UPDATE SET
		meta_total = mvn_daily_stats.meta_total + excluded.meta_total,
		meta_hits = mvn_daily_stats.meta_hits + excluded.meta_hits,
		file_total = mvn_daily_stats.file_total + excluded.file_total,
		file_hits = mvn_daily_stats.file_hits + excluded.file_hits`,
}

// dailyTables 四张每日统计表与各自首列（用于断言累加结果）。
var dailyTables = []struct {
	table string
	col   string
}{
	{"go_daily_stats", "meta_total"},
	{"npm_daily_stats", "meta_total"},
	{"py_daily_stats", "index_total"},
	{"mvn_daily_stats", "meta_total"},
}

// TestDailyStatsUpsertDualDialect 每日统计 upsert 双方言回归：
// SQLite 常跑；设置 TEST_PG_DSN 时对真实 PG 再跑同一组语句（原裸列形态在 PG 报
// SQLSTATE 42702，*_daily_stats 恒 0 行）。
func TestDailyStatsUpsertDualDialect(t *testing.T) {
	dir := t.TempDir()
	s, err := Open("file:" + filepath.ToSlash(filepath.Join(dir, "t.db")) + "?_busy_timeout=10000&_journal_mode=DELETE")
	if err != nil {
		t.Fatalf("打开 SQLite 失败: %v", err)
	}
	defer s.Close()
	dailyRunShapes(t, s)

	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Log("未设置 TEST_PG_DSN，跳过 PostgreSQL 侧验证")
		return
	}
	pg, err := OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatalf("打开 PG 失败: %v", err)
	}
	dropAllTables(t, pg)
	pg.Close()
	pg, err = OpenPostgreSQL(dsn)
	if err != nil {
		t.Fatalf("重开 PG 失败: %v", err)
	}
	defer pg.Close()
	dailyRunShapes(t, pg)
}

// dailyRunShapes 同一语句连续执行两次（各 +1），断言当日值累加为 2。
func dailyRunShapes(t *testing.T, s *Store) {
	t.Helper()
	for i, stmt := range dailyUpsertShapes {
		for n := 0; n < 2; n++ {
			if _, err := s.w.Exec(stmt); err != nil {
				t.Fatalf("第 %d 张表 upsert 失败（方言=%s）: %v\n%s", i+1, s.Dialect(), err, stmt)
			}
		}
	}
	for _, d := range dailyTables {
		var v int64
		if err := s.r.QueryRow(`SELECT ` + d.col + ` FROM ` + d.table + ` WHERE day = '2030-01-01'`).Scan(&v); err != nil {
			t.Fatalf("%s 查询失败: %v", d.table, err)
		}
		if v != 2 {
			t.Fatalf("%s.%s 应累加为 2，实际 %d", d.table, d.col, v)
		}
	}
}
