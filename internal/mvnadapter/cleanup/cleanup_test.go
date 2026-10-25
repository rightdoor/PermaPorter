// Package cleanup 的 mvn 生态清理测试：复位缺失文件状态、孤立文件保护窗口、
// 临时残留回收、空目录剪枝、仅元数据构件绝不删除。
package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/models"
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

// seedCachedFile 登记构件与已缓存文件记录并落盘文件。
func seedCachedFile(t *testing.T, db *database.DB, group, artifact, repoPath string, content []byte) {
	t.Helper()
	id, err := db.UpsertArtifact(&models.Artifact{GroupID: group, ArtifactID: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureFileRecord(id, &models.File{
		ArtifactID: id, RepoPath: repoPath, Version: "1.0.0",
		Filename: filepath.Base(repoPath), FileSizeBytes: int64(len(content)),
		SHA1: "ab", FilePath: repoPath,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRunResetsMissingFiles 有缓存记录但文件缺失 → 复位状态且保留记录。
func TestRunResetsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	seedCachedFile(t, db, "com.example", "demo",
		"com/example/demo/1.0.0/demo-1.0.0.jar", []byte("jar"))
	// 记录标记已缓存但磁盘无文件

	res := Run(db, storage)
	if res.ResetFileRecords != 1 {
		t.Fatalf("应复位 1 条缺失文件记录，实际 %d", res.ResetFileRecords)
	}
	// 记录保留（下次请求重新回源）
	if _, err := db.GetFileByRepoPath("com/example/demo/1.0.0/demo-1.0.0.jar"); err != nil {
		t.Fatalf("复位后记录应保留: %v", err)
	}
}

// TestRunOrphansAndTmpResidue 孤立构件按保护窗口删除；.mvndl-* 下载残留超时删除；
// 被 DB 引用的有效文件绝不删除；清理后空目录被剪枝。
func TestRunOrphansAndTmpResidue(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")
	verDir := filepath.Join(storage, "com", "example", "real", "1.0.0")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 被 DB 引用的有效文件
	keepRel := "com/example/real/1.0.0/real-1.0.0.jar"
	keep := filepath.Join(storage, filepath.FromSlash(keepRel))
	if err := os.WriteFile(keep, []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCachedFile(t, db, "com.example", "real", keepRel, []byte("jar"))

	// 孤立文件：旧的删除、新的保留
	orphanOld := filepath.Join(verDir, "orphan-old.jar")
	orphanNew := filepath.Join(verDir, "orphan-new.jar")
	for _, p := range []string{orphanOld, orphanNew} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(orphanOld, oldTime(), oldTime()); err != nil {
		t.Fatal(err)
	}

	// 下载临时残留：超过 1 小时删除
	tmpOld := filepath.Join(verDir, tmpPrefix+"123")
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
	// 版本目录仍含有效文件与新孤立文件：不剪枝
	if _, err := os.Stat(verDir); err != nil {
		t.Fatal("非空版本目录不应被剪枝")
	}
}

// TestRunPrunesEmptyDirs 清理删除孤立文件后产生的空目录（自底向上）被剪枝。
func TestRunPrunesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")
	orphan := filepath.Join(storage, "com", "example", "gone", "1.0.0", "gone-1.0.0.jar")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, oldTime(), oldTime()); err != nil {
		t.Fatal(err)
	}

	res := Run(db, storage)
	if res.RemovedFiles != 1 || res.PrunedDirs != 4 {
		t.Fatalf("孤立文件与空目录剪枝计数不符: %+v", res)
	}
	for _, d := range []string{"1.0.0", "gone", "example", "com"} {
		if _, err := os.Stat(filepath.Join(storage, "com", "example", "gone", d)); err == nil && d != "com" {
			t.Fatalf("空目录 %s 应被剪枝", d)
		}
	}
}

// TestRunKeepsMetaOnlyArtifacts 仅元数据构件（只缓存过 metadata）是合法终态，
// 绝不删除构件记录与 metadata 缓存。
func TestRunKeepsMetaOnlyArtifacts(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	storage := filepath.Join(dir, "storage")

	if _, err := db.UpsertArtifact(&models.Artifact{
		GroupID: "com.example", ArtifactID: "meta-only", LatestVersion: "1.0.0", VersionCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveMetaEntry("com/example/meta-only/maven-metadata.xml", sampleMeta, `"v1"`); err != nil {
		t.Fatal(err)
	}

	Run(db, storage)

	if _, err := db.GetArtifact("com.example", "meta-only"); err != nil {
		t.Fatalf("仅元数据构件不应被清理: %v", err)
	}
	if _, err := db.GetMetaEntry("com/example/meta-only/maven-metadata.xml"); err != nil {
		t.Fatalf("metadata 缓存不应被清理: %v", err)
	}
}

const sampleMeta = `<metadata><groupId>com.example</groupId><artifactId>meta-only</artifactId>
<versioning><latest>1.0.0</latest><release>1.0.0</release><versions><version>1.0.0</version></versions></versioning></metadata>`
