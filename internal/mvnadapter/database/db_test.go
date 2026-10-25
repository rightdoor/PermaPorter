// Package database 的 mvn 生态查询层测试：构件/文件/元数据 CRUD、物化统计、
// 缓存状态与统计落库。
package database

import (
	"path/filepath"
	"testing"
	"time"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/mvnadapter/models"
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

// TestArtifactUpsertArtifactMinThenMeta 最小构件行（固定版本下载）先建，
// 构件级 metadata 解析再补齐版本信息；两者都幂等。
func TestArtifactUpsertArtifactMinThenMeta(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings("http://127.0.0.1:1/maven", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}

	// 最小行：固定版本下载（无版本清单信息）
	id1, err := db.UpsertArtifact(&models.Artifact{GroupID: "com.example", ArtifactID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	// 构件级 metadata 补齐
	id2, err := db.UpsertArtifact(&models.Artifact{
		GroupID: "com.example", ArtifactID: "demo",
		LatestVersion: "2.0.0", ReleaseVersion: "2.0.0", VersionCount: 3,
		LastUpdated: "20240306082314", MetaCachedAt: "2024-03-06 08:23:14",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("同一构件应复用一行: %d != %d", id1, id2)
	}
	a, err := db.GetArtifact("com.example", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if a.LatestVersion != "2.0.0" || a.VersionCount != 3 {
		t.Fatalf("元数据应补齐: %+v", a)
	}
	// 再次最小 upsert（另一文件下载）不覆盖版本信息
	if _, err := db.UpsertArtifact(&models.Artifact{GroupID: "com.example", ArtifactID: "demo"}); err != nil {
		t.Fatal(err)
	}
	a2, _ := db.GetArtifact("com.example", "demo")
	if a2.LatestVersion != "2.0.0" || a2.VersionCount != 3 || a2.LastUpdated != "20240306082314" {
		t.Fatalf("最小行不应清空既有元数据: %+v", a2)
	}
}

// TestFileRecordLifecycle 文件登记、缓存状态与物化统计。
func TestFileRecordLifecycle(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	artifactID, err := db.UpsertArtifact(&models.Artifact{GroupID: "com.example", ArtifactID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	repoPath := "com/example/demo/1.0.0/demo-1.0.0.jar"
	if err := db.EnsureFileRecord(artifactID, &models.File{
		ArtifactID: artifactID, RepoPath: repoPath, Version: "1.0.0",
		Filename: "demo-1.0.0.jar", FileSizeBytes: 42, SHA1: "ab", FilePath: repoPath,
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := db.GetFileByRepoPath(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if rec.FileSizeBytes != 42 || rec.GroupID != "com.example" || rec.ArtifactID != "demo" {
		t.Fatalf("文件记录不符: %+v", rec)
	}

	// 复位缓存状态（清理器调用）：记录保留、统计回零
	if err := db.ClearFile(rec.ID); err != nil {
		t.Fatal(err)
	}
	a, _ := db.GetArtifact("com.example", "demo")
	if a.FileCount != 1 || a.CachedFileCount != 0 || a.CachedTotalSize != 0 {
		t.Fatalf("复位后统计不符: %+v", a)
	}

	// 删除记录后统计归零
	if err := db.DeleteFileByRepoPath(repoPath); err != nil {
		t.Fatal(err)
	}
	a2, _ := db.GetArtifact("com.example", "demo")
	if a2.FileCount != 0 {
		t.Fatalf("删除后统计不符: %+v", a2)
	}
}

// TestMetaEntrySaveAndGet metadata 缓存读写与时间戳推进。
func TestMetaEntrySaveAndGet(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	p := "com/example/demo/maven-metadata.xml"
	if err := db.SaveMetaEntry(p, "<metadata/>", `W/"x1"`); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetMetaEntry(p)
	if err != nil || m.XML != "<metadata/>" || m.ETag != `W/"x1"` {
		t.Fatalf("读取不符: %+v %v", m, err)
	}
	// 覆盖更新
	if err := db.SaveMetaEntry(p, "<metadata v2/>", `W/"x2"`); err != nil {
		t.Fatal(err)
	}
	m2, _ := db.GetMetaEntry(p)
	if m2.XML != "<metadata v2/>" || m2.ETag != `W/"x2"` {
		t.Fatalf("覆盖更新不符: %+v", m2)
	}
	db.TouchMetaEntry(p) // execNoGen 时间戳推进：只验证不 panic、不报错
}

// TestRecordHitsAndFlush 命中统计进内存缓冲，FlushStats 后可从 status 表读回，
// TouchArtifact 的 group:artifact 键也能批量落库。
func TestRecordHitsAndFlush(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if _, err := db.UpsertArtifact(&models.Artifact{GroupID: "com.example", ArtifactID: "demo"}); err != nil {
		t.Fatal(err)
	}
	db.RecordMetaHit(true)
	db.RecordMetaHit(false)
	db.RecordFileHit(true)
	db.TouchArtifact("com.example", "demo")
	db.FlushStats()

	st, err := db.GetStatusStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.MetaTotal != 2 || st.MetaHits != 1 || st.FileTotal != 1 || st.FileHits != 1 {
		t.Fatalf("落盘统计不符: %+v", st)
	}
	a, err := db.GetArtifact("com.example", "demo")
	if err != nil || a.LastAccessedAt == "" {
		t.Fatalf("访问时间应已落库: %+v %v", a, err)
	}
}

// TestArtifactSearchAndSort 列表搜索（group/artifact/组合名）与排序白名单。
func TestArtifactSearchAndSort(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, dir)
	for _, a := range []models.Artifact{
		{GroupID: "com.google.guava", ArtifactID: "guava"},
		{GroupID: "org.slf4j", ArtifactID: "slf4j-api"},
	} {
		if _, err := db.UpsertArtifact(&a); err != nil {
			t.Fatal(err)
		}
	}
	// 组合名命中
	n, err := db.CountArtifacts("guava:guava")
	if err != nil || n != 1 {
		t.Fatalf("组合名搜索应命中 1: n=%d err=%v", n, err)
	}
	n2, _ := db.CountArtifacts("slf4j")
	if n2 != 1 {
		t.Fatalf("artifactId 搜索应命中 1: %d", n2)
	}
	// 排序键白名单：非法键回退默认（不注入）
	rows, err := db.ListArtifactsOrdered("", "name; DROP TABLE mvn_artifacts", "desc", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("非法排序键不应报错或丢行: %d", len(rows))
	}
}
