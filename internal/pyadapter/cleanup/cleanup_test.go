// Package cleanup 的 py 生态清理测试：复位缺失文件状态、孤立文件保护窗口、
// 临时残留回收、仅元数据项目绝不删除。
package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/models"
)

func openTestDB(t *testing.T, dir string) *database.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=5000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := database.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

// oldTime 超过保护窗口的修改时间。
func oldTime() time.Time { return time.Now().Add(-2 * time.Hour) }

// TestRunResetsMissingFiles 有缓存记录但文件缺失 → 复位状态且保留记录。
func TestRunResetsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	projID, err := db.UpsertProject(&models.Project{ProjectName: "demo-pkg"})
	if err != nil {
		t.Fatal(err)
	}
	files := []models.File{{
		ProjectID: projID, Version: "0.1.0", Filename: "demo_pkg-0.1.0-py3-none-any.whl",
		FilePath: "packages/demo_pkg-0.1.0-py3-none-any.whl",
	}}
	if err := db.UpsertFiles(projID, files); err != nil {
		t.Fatal(err)
	}
	// 标记已缓存（SetFileCached）但不落盘文件
	rec, err := db.GetFileByName("demo_pkg-0.1.0-py3-none-any.whl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFileCached(rec.ID, 100, "packages/demo_pkg-0.1.0-py3-none-any.whl"); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)
	if res.ResetFileRecords != 1 {
		t.Fatalf("应复位 1 条缺失文件记录，实际 %d", res.ResetFileRecords)
	}
	// 记录保留（锚点页仍列出它）
	if _, err := db.GetFileByName("demo_pkg-0.1.0-py3-none-any.whl"); err != nil {
		t.Fatalf("复位后记录应保留: %v", err)
	}
}

// TestRunOrphansAndTmpResidue 孤立发行文件按保护窗口删除；
// .pydl-* 下载残留超时删除；被 DB 引用的有效文件绝不删除。
func TestRunOrphansAndTmpResidue(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")
	pkgDir := filepath.Join(storage, "packages")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 被 DB 引用的有效文件
	projID, err := db.UpsertProject(&models.Project{ProjectName: "real-pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFiles(projID, []models.File{{
		ProjectID: projID, Version: "1.0.0", Filename: "real_pkg-1.0.0-py3-none-any.whl",
	}}); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(pkgDir, "real_pkg-1.0.0-py3-none-any.whl")
	if err := os.WriteFile(keep, []byte("wheel"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := db.GetFileByName("real_pkg-1.0.0-py3-none-any.whl")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetFileCached(rec.ID, 5, "packages/real_pkg-1.0.0-py3-none-any.whl"); err != nil {
		t.Fatal(err)
	}

	// 孤立文件：旧的删除、新的保留
	orphanOld := filepath.Join(pkgDir, "orphan-old.whl")
	orphanNew := filepath.Join(pkgDir, "orphan-new.whl")
	for _, p := range []string{orphanOld, orphanNew} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(orphanOld, oldTime(), oldTime()); err != nil {
		t.Fatal(err)
	}

	// 下载临时残留：超过 1 小时删除
	tmpOld := filepath.Join(pkgDir, ".pydl-123")
	if err := os.WriteFile(tmpOld, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmpOld, oldTime(), oldTime()); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)

	if _, err := os.Stat(keep); err != nil {
		t.Fatal("被 DB 引用的有效文件不应被清理")
	}
	if _, err := os.Stat(orphanOld); err == nil {
		t.Fatal("超过保护窗口的孤立文件应被清理")
	}
	if _, err := os.Stat(orphanNew); err != nil {
		t.Fatal("保护窗口内的孤立文件不应被清理")
	}
	if _, err := os.Stat(tmpOld); err == nil {
		t.Fatal("超时的下载临时残留应被清理")
	}
	if res.RemovedFiles != 1 || res.RemovedTmpFiles != 1 {
		t.Fatalf("清理计数不符: %+v", res)
	}
}

// TestRunKeepsMetaOnlyProjects 仅元数据项目（锚点页缓存）是合法终态，绝不删除项目记录。
func TestRunKeepsMetaOnlyProjects(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	projID, err := db.UpsertProject(&models.Project{ProjectName: "meta-only-pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFiles(projID, []models.File{{
		ProjectID: projID, Version: "0.2.0", Filename: "meta_only_pkg-0.2.0.tar.gz",
	}}); err != nil {
		t.Fatal(err)
	}

	Run(db, storage)

	if _, err := db.GetProject("meta-only-pkg"); err != nil {
		t.Fatalf("仅元数据项目不应被清理: %v", err)
	}
	if _, err := db.GetFileByName("meta_only_pkg-0.2.0.tar.gz"); err != nil {
		t.Fatalf("仅元数据文件记录不应被清理: %v", err)
	}
}
