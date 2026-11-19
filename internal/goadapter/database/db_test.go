package database

import (
	"path/filepath"
	"testing"

	coredatabase "permaporter/internal/core/database"
)

// testDB 打开一个临时单库并挂载 go 生态查询层。
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

// TestVersionRowMaintainsMaterializedStats 复现：产物下载路径（UpsertVersionRow）
// 建立版本记录后，模块列表的 version_count / cached_version_count / cached_total_size
// 等物化列必须与 go_versions 实际数据一致——否则列表页显示 0，只有详情页实时统计才对。
func TestVersionRowMaintainsMaterializedStats(t *testing.T) {
	db := testDB(t)
	mid, err := db.UpsertModulePath("example.com/repro")
	if err != nil {
		t.Fatalf("upsert module: %v", err)
	}
	vid, err := db.UpsertVersionRow(mid, "v1.2.3", false)
	if err != nil {
		t.Fatalf("upsert version: %v", err)
	}
	if err := db.SetVersionZip(vid, 4096, "modules/example.com/repro/@v/v1.2.3.zip", "h1:abc"); err != nil {
		t.Fatalf("set zip: %v", err)
	}

	items, err := db.ListModules("", 0, 10)
	if err != nil {
		t.Fatalf("list modules: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 个模块，实际 %d", len(items))
	}
	got := items[0]
	if got.VersionCount != 1 {
		t.Errorf("version_count 应为 1，实际 %d", got.VersionCount)
	}
	if got.CachedVersionCount != 1 {
		t.Errorf("cached_version_count 应为 1，实际 %d", got.CachedVersionCount)
	}
	if got.CachedTotalSize != 4096 {
		t.Errorf("cached_total_size 应为 4096，实际 %d", got.CachedTotalSize)
	}
	if got.LatestVersion != "v1.2.3" {
		t.Errorf("latest_version 应推导为 v1.2.3，实际 %q", got.LatestVersion)
	}
}

// TestUpsertVersionKeepsAuthoritativeLatest 已有 @latest / @v/list 缓存的 latest_version
// 更权威，产物路径不得用具体下载版本覆盖它。
func TestUpsertVersionKeepsAuthoritativeLatest(t *testing.T) {
	db := testDB(t)
	mid, err := db.UpsertModulePath("example.com/keep-latest")
	if err != nil {
		t.Fatalf("upsert module: %v", err)
	}
	if err := db.UpdateLatestCached("example.com/keep-latest", "v2.0.0", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("set authoritative latest: %v", err)
	}
	// 产物下载的是老版本 v1.0.0，不应覆盖 v2.0.0
	_, err = db.UpsertVersionRow(mid, "v1.0.0", false)
	if err != nil {
		t.Fatalf("upsert version: %v", err)
	}
	items, err := db.ListModules("", 0, 10)
	if err != nil {
		t.Fatalf("list modules: %v", err)
	}
	if len(items) != 1 || items[0].LatestVersion != "v2.0.0" {
		t.Fatalf("latest_version 应保持 v2.0.0，实际 %+v", items)
	}
	if items[0].VersionCount != 1 {
		t.Errorf("version_count 应为 1，实际 %d", items[0].VersionCount)
	}
}

// TestDeriveLatestFromVersions 推导语义：release 优先于 prerelease，伪版本忽略。
func TestDeriveLatestFromVersions(t *testing.T) {
	got := deriveLatestFromVersions([]string{
		"v1.0.0", "v2.0.0-beta.1", "v1.5.0", "v0.0.0-20260101120000-abcdef123456", "garbage",
	})
	if got != "v1.5.0" {
		t.Fatalf("应取最高 release v1.5.0，实际 %q", got)
	}
	if got := deriveLatestFromVersions([]string{"v3.0.0-rc.1", "v3.0.0-alpha.1"}); got != "v3.0.0-rc.1" {
		t.Fatalf("无 release 时取最高 prerelease，实际 %q", got)
	}
	if got := deriveLatestFromVersions(nil); got != "" {
		t.Fatalf("空列表应返回空，实际 %q", got)
	}
}
