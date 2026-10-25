package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/models"
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

// 场景：批量下载进行中（元数据已入库、tarball 未落盘）触发清理，
// 仅跳过最近活跃（最近有访问）的包；只要长期无访问，即使元数据刚刷新
// （chokidar 场景）也视为过期仅元数据包并清理。
func TestRunProtectsActiveOnlyMetaPackages(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	old := time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02 15:04:05")

	// 包 A：刚访问 + 元数据刚入库（下载进行中的典型状态）→ 应保留
	pkgA := &models.Package{PkgName: "active-pkg"}
	if _, err := db.UpsertPackage(pkgA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE npm_packages SET meta_cached_at = CURRENT_TIMESTAMP,
		last_accessed_at = CURRENT_TIMESTAMP WHERE pkg_name = 'active-pkg'`); err != nil {
		t.Fatal(err)
	}

	// 包 B：元数据 2 天前入库、从未访问（真正过期的仅元数据包）→ 应清理
	pkgB := &models.Package{PkgName: "stale-pkg"}
	if _, err := db.UpsertPackage(pkgB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE npm_packages SET meta_cached_at = ?,
		last_accessed_at = NULL WHERE pkg_name = 'stale-pkg'`, old); err != nil {
		t.Fatal(err)
	}

	// 包 C：元数据刚刷新但已很久无访问（chokidar 场景）→ 也应清理
	pkgC := &models.Package{PkgName: "chokidar-like"}
	if _, err := db.UpsertPackage(pkgC); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE npm_packages SET meta_cached_at = CURRENT_TIMESTAMP,
		last_accessed_at = ? WHERE pkg_name = 'chokidar-like'`, old); err != nil {
		t.Fatal(err)
	}

	res := Run(db, dir)

	if res.RemovedMetaOnly != 2 {
		t.Fatalf("应清理 2 个过期仅元数据包（stale-pkg 与 chokidar-like），实际 %d", res.RemovedMetaOnly)
	}
	if _, err := db.GetPackage("active-pkg"); err != nil {
		t.Fatalf("最近活跃的包不应被清理: %v", err)
	}
	if _, err := db.GetPackage("stale-pkg"); err == nil {
		t.Fatal("过期的仅元数据包应被清理")
	}
	if _, err := db.GetPackage("chokidar-like"); err == nil {
		t.Fatal("元数据虽新但长期无访问的包应被清理")
	}
}

// 场景：历史库中存在越界版本记录（如被污染上游写入的 ../../.. 版本键）。
// 清理器不得触达存储树外的文件：记录按"文件缺失"删除，存储树外文件不被删除。
func TestRunDeletesTraversalVersionRecord(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")
	if err := os.MkdirAll(filepath.Join(storage, "packages"), 0o755); err != nil {
		t.Fatal(err)
	}

	pkgID, err := db.UpsertPackage(&models.Package{
		PkgName:      "evil-host",
		MetaCachedAt: time.Now().UTC().Add(-48 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO npm_versions (pkg_id, version, tarball_path, file_size_bytes)
		VALUES (?, '1.0.0', 'packages/evil-host/../../../evil.tgz', 100)`, pkgID); err != nil {
		t.Fatal(err)
	}
	// 存储树外的诱饵文件：清理后必须原样存在
	sentinel := filepath.Join(dir, "evil.tgz")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)

	if res.RemovedVersions != 1 {
		t.Fatalf("越界版本记录应按文件缺失删除，实际删除 %d 条", res.RemovedVersions)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("存储树外的文件不得被删除")
	}
	if _, err := db.GetVersionByName("evil-host", "1.0.0"); err == nil {
		t.Fatal("越界版本记录应已删除")
	}
}

// 场景：孤立 tarball 文件——近期修改的（可能是下载进行中的产物）跳过，
// 超过保护窗口的删除。
func TestRunKeepsRecentOrphanTarballs(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)

	// 一个真实的已缓存包，确保其文件不会被当孤立文件删除
	pkg := &models.Package{PkgName: "real-pkg", MetaCachedAt: time.Now().UTC().Format("2006-01-02 15:04:05")}
	pkgID, err := db.UpsertPackage(pkg)
	if err != nil {
		t.Fatal(err)
	}
	tarballDir := filepath.Join(dir, "packages", "real-pkg")
	if err := os.MkdirAll(tarballDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(tarballDir, "real-pkg-1.0.0.tgz")
	if err := os.WriteFile(keepPath, []byte("tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO npm_versions (pkg_id, version, tarball_path, file_size_bytes) VALUES (?, '1.0.0', 'packages/real-pkg/real-pkg-1.0.0.tgz', 7)`, pkgID); err != nil {
		t.Fatal(err)
	}

	// 孤立文件：旧的（应删除）与新的（应保留，可能是下载中产物）
	orphanOld := filepath.Join(dir, "packages", "orphan-old-1.0.0.tgz")
	orphanNew := filepath.Join(dir, "packages", ".download-123")
	for _, p := range []string{orphanOld, orphanNew} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(orphanOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	res := Run(db, dir)

	if _, err := os.Stat(orphanNew); err != nil {
		t.Fatal("近期修改的孤立文件不应被清理（可能正在下载）")
	}
	if _, err := os.Stat(orphanOld); err == nil {
		t.Fatal("超过保护窗口的孤立文件应被清理")
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatal("已缓存包的文件不应被清理")
	}
	if res.RemovedTarballs != 1 {
		t.Fatalf("应恰好清理 1 个孤立 tarball，实际 %d", res.RemovedTarballs)
	}
}
