package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/models"
)

// openAdminTestDB 打开临时单库并挂载 npm 生态查询层。
func openAdminTestDB(t *testing.T, dir string) *database.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=10000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := database.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

// TestDeleteVersionAPISkipsTraversalPath 越界版本记录的管理删除：
// DB 记录正常删除，但存储树外的文件不得被删除（路径必须经越界断言）。
func TestDeleteVersionAPISkipsTraversalPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	db := openAdminTestDB(t, dir)
	storage := filepath.Join(dir, "storage")
	if err := db.InitSettings("http://127.0.0.1:1", storage, time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	pkgID, err := db.UpsertPackage(&models.Package{
		PkgName:      "demo",
		MetaCachedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO npm_versions (pkg_id, version, tarball_path, file_size_bytes)
		VALUES (?, '1.0.0', 'packages/demo/../../../evil.tgz', 100)`, pkgID); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "evil.tgz")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{DB: db}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete,
		"/api/admin/package/version?pkg=demo&version=1.0.0", nil)
	app.DeleteVersionAPI(c)
	if w.Code != http.StatusOK {
		t.Fatalf("删除接口应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("存储树外的文件不得被删除")
	}
	if _, err := db.GetVersionByName("demo", "1.0.0"); err == nil {
		t.Fatal("版本记录应已删除")
	}
}
