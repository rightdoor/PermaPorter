package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/mod/module"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/goadapter/database"
	"permaporter/internal/goadapter/proxy"
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

func oldTime() time.Time { return time.Now().Add(-2 * time.Hour) }

func touchOld(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := oldTime()
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestRunKeepsMetadataOnlyModules 仅元数据模块（只有 @v/list）是合法缓存，
// 清理绝不删除其记录与文件。
func TestRunKeepsMetadataOnlyModules(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	moduleID, err := db.UpsertModulePath("example.com/meta-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MarkListed(moduleID, []string{"v1.0.0", "v1.1.0"}, module.IsPseudoVersion); err != nil {
		t.Fatal(err)
	}
	listFile := filepath.Join(storage, "modules", "example.com", "meta-only", "@v", "list")
	touchOld(t, listFile)
	if err := db.UpdateListCached("example.com/meta-only", ""); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)
	if res.RemovedFiles != 0 {
		t.Fatalf("合法 list 文件不应被删除: %+v", res)
	}
	if _, err := os.Stat(listFile); err != nil {
		t.Fatal("list 文件应保留")
	}
	if _, err := db.GetModule("example.com/meta-only"); err != nil {
		t.Fatal("模块记录应保留")
	}
}

// TestRunResetsMissingZipResetsNotDeletes zip 文件丢失时只复位状态、保留版本记录。
func TestRunResetsMissingZipResetsNotDeletes(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	moduleID, _ := db.UpsertModulePath("example.com/lost")
	verID, err := db.UpsertVersionRow(moduleID, "v1.0.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetVersionZip(verID, 1234, "modules/example.com/lost/@v/v1.0.0.zip", "h1:xxx"); err != nil {
		t.Fatal(err)
	}
	// 文件丢失（从未创建）
	res := Run(db, storage)
	if res.ResetZipRecords != 1 {
		t.Fatalf("应复位 1 条 zip 状态: %+v", res)
	}
	v, err := db.GetVersion("example.com/lost", "v1.0.0")
	if err != nil {
		t.Fatal("版本记录应保留")
	}
	if v.ZipSizeBytes != 0 || v.Ziphash != "" {
		t.Fatalf("zip 状态应清零: %+v", v)
	}
}

// TestRunRemovesOrphanFiles 孤立产物文件被删除，有效文件保留；空目录剪枝。
func TestRunRemovesOrphanFiles(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	moduleID, _ := db.UpsertModulePath("example.com/orphan")
	verID, _ := db.UpsertVersionRow(moduleID, "v1.0.0", false)
	zipRel := "modules/example.com/orphan/@v/v1.0.0.zip"
	if err := db.SetVersionZip(verID, 100, zipRel, "h1:"); err != nil {
		t.Fatal(err)
	}
	zipFull := filepath.Join(storage, filepath.FromSlash(zipRel))
	touchOld(t, zipFull)
	// 有效 zip 的伴随 ziphash 视为有效（serveZiphash 会按需现算），不应删除
	staleHash := filepath.Join(storage, "modules", "example.com", "orphan", "@v", "v1.0.0.ziphash")
	touchOld(t, staleHash)
	// 无 DB 记录的孤立 info 应删除
	orphanInfo := filepath.Join(storage, "modules", "example.com", "orphan", "@v", "v2.0.0.info")
	touchOld(t, orphanInfo)
	// 近期修改的文件（下载进行中）应保留
	fresh := filepath.Join(storage, "modules", "example.com", "orphan", "@v", "v9.9.9.info")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)
	if res.RemovedFiles != 1 {
		t.Fatalf("应删除 1 个孤立文件: %+v", res)
	}
	if _, err := os.Stat(zipFull); err != nil {
		t.Fatal("有效 zip 应保留")
	}
	if _, err := os.Stat(staleHash); err != nil {
		t.Fatal("有效 zip 的伴随 ziphash 应保留")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("近期文件应保留")
	}
}

// TestRunNestedModulesNotEmptyRemoved 嵌套模块共享父目录：清理不得误删非空父目录。
func TestRunNestedModulesNotEmptyRemoved(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	// example.com/a 与 example.com/a/b 共享 example.com/a 目录
	for _, m := range []string{"example.com/a", "example.com/a/b"} {
		id, err := db.UpsertModulePath(m)
		if err != nil {
			t.Fatal(err)
		}
		esc := proxy.EscapePath(m)
		touchOld(t, filepath.Join(storage, "modules", filepath.FromSlash(esc), "@v", "list"))
		if err := db.UpdateListCached(m, ""); err != nil {
			t.Fatal(err)
		}
		_ = id
	}
	// 造一个真正的空目录
	emptyDir := filepath.Join(storage, "modules", "example.com", "empty", "@v")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	Run(db, storage)
	// 两个模块的 list 都应保留
	for _, m := range []string{"example.com/a", "example.com/a/b"} {
		list := filepath.Join(storage, "modules", filepath.FromSlash(proxy.EscapePath(m)), "@v", "list")
		if _, err := os.Stat(list); err != nil {
			t.Fatalf("嵌套模块 %s 的 list 应保留: %v", m, err)
		}
	}
	if _, err := os.Stat(emptyDir); !os.IsNotExist(err) {
		t.Fatal("空目录应被剪枝")
	}
}

// TestRunRemovesStaleTmp 过期临时文件删除，新临时文件保留。
func TestRunRemovesStaleTmp(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	tmpDir := filepath.Join(storage, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	touchOld(t, filepath.Join(tmpDir, ".zip-old"))
	fresh := filepath.Join(tmpDir, ".zip-new")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Run(db, storage)
	if res.RemovedTmpFiles != 1 {
		t.Fatalf("应删除 1 个过期临时文件: %+v", res)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新临时文件应保留")
	}
}

// TestPruneEmptyDirsStopsAtRoot 剪枝在存储根目录停止。
func TestPruneEmptyDirsStopsAtRoot(t *testing.T) {
	dir := t.TempDir()
	storage := filepath.Join(dir, "storage")
	deep := filepath.Join(storage, "modules", "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	PruneEmptyDirs(storage, deep)
	if _, err := os.Stat(storage); err != nil {
		t.Fatal("存储根目录不应被删除")
	}
	if _, err := os.Stat(deep); !os.IsNotExist(err) {
		t.Fatal("深层空目录应被删除")
	}
}
