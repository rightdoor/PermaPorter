// Package database 的 py 生态查询层测试：项目/文件 CRUD、物化统计、
// 缓存状态与统计落库。
package database

import (
	"path/filepath"
	"testing"
	"time"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/pyadapter/models"
)

func openTestDB(t *testing.T, dir string) *DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=5000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

// TestProjectAndFilesUpsert 项目与文件清单 upsert：重跑幂等、元数据更新不影响缓存状态。
func TestProjectAndFilesUpsert(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings("http://127.0.0.1:1/simple/", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}

	projID, err := db.UpsertProject(&models.Project{ProjectName: "demo-pkg", DisplayName: "Demo.Pkg"})
	if err != nil {
		t.Fatal(err)
	}
	files := []models.File{
		{ProjectID: projID, Version: "0.1.0", Filename: "demo_pkg-0.1.0-py3-none-any.whl", SHA256: "aa"},
		{ProjectID: projID, Version: "0.1.0", Filename: "demo-pkg-0.1.0.tar.gz"},
	}
	if err := db.UpsertFiles(projID, files); err != nil {
		t.Fatal(err)
	}

	// 标记 wheel 已缓存
	rec, err := db.GetFileByName("demo_pkg-0.1.0-py3-none-any.whl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFileCached(rec.ID, 123, "packages/demo_pkg-0.1.0-py3-none-any.whl"); err != nil {
		t.Fatal(err)
	}

	// 重跑 upsert（如锚点页刷新）：已缓存状态不受影响
	files[0].SHA256 = "bb"
	if err := db.UpsertFiles(projID, files); err != nil {
		t.Fatal(err)
	}
	rec2, err := db.GetFileByName("demo_pkg-0.1.0-py3-none-any.whl")
	if err != nil {
		t.Fatal(err)
	}
	if rec2.FileSizeBytes != 123 || rec2.SHA256 != "bb" {
		t.Fatalf("upsert 应保留缓存状态并更新元数据: %+v", rec2)
	}

	// 物化统计：file_count/cached 计数
	proj, err := db.GetProject("demo-pkg")
	if err != nil {
		t.Fatal(err)
	}
	if proj.FileCount != 2 || proj.CachedFileCount != 1 || proj.CachedTotalSize != 123 {
		t.Fatalf("物化统计不符: %+v", proj)
	}
}

// TestListFilesForVersion 按版本过滤文件清单。
func TestListFilesForVersion(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	projID, err := db.UpsertProject(&models.Project{ProjectName: "ver-pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFiles(projID, []models.File{
		{ProjectID: projID, Version: "1.0.0", Filename: "ver_pkg-1.0.0-py3-none-any.whl"},
		{ProjectID: projID, Version: "1.1.0", Filename: "ver_pkg-1.1.0-py3-none-any.whl"},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListProjectFilesForVersion(projID, "1.0.0")
	if err != nil || len(items) != 1 || items[0].Filename != "ver_pkg-1.0.0-py3-none-any.whl" {
		t.Fatalf("按版本过滤异常: %v %+v", err, items)
	}
}

// TestRecordHitsAndFlush 命中统计进内存缓冲，FlushStats 后可从 status 表读回。
func TestRecordHitsAndFlush(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	db.RecordIndexHit(true)
	db.RecordIndexHit(false)
	db.RecordFileHit(true)
	db.FlushStats()

	st, err := db.GetStatusStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.IndexTotal != 2 || st.IndexHits != 1 || st.FileTotal != 1 || st.FileHits != 1 {
		t.Fatalf("落盘统计不符: %+v", st)
	}
}
