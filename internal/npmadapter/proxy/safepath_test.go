package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/npmadapter/models"
)

// TestValidVersionKey 版本键白名单：合法 semver 变体通过，穿越载荷与控制字符拒绝。
func TestValidVersionKey(t *testing.T) {
	valid := []string{"1.0.0", "2.0.0-beta.1", "1.0.0-rc.0+build.5", "0.0.0", "10.20.30", "a1"}
	invalid := []string{
		"", ".", "..", "../x", "../../x", "../../../../tmp/x",
		"/abs", "a/b", `a\b`, "a b", "a\nb", "-1.0.0", ".hidden",
	}
	for _, v := range valid {
		if !ValidVersionKey(v) {
			t.Errorf("合法版本键被误判: %q", v)
		}
	}
	for _, v := range invalid {
		if ValidVersionKey(v) {
			t.Errorf("非法版本键未被拒绝: %q", v)
		}
	}
}

// TestStorageFilePath 越界断言：相对路径不得组合出存储目录之外的绝对路径。
func TestStorageFilePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage")
	if p, err := StorageFilePath(root, filepath.FromSlash("packages/demo/demo-1.0.0.tgz")); err != nil || !strings.HasPrefix(p, root) {
		t.Fatalf("合法路径被拒绝: %s %v", p, err)
	}
	if _, err := StorageFilePath(root, filepath.FromSlash("packages/demo/../../../evil.tgz")); err == nil {
		t.Fatal("穿越路径未被拒绝")
	}
	// 反斜杠仅在 Windows 是路径分隔符（Linux 下是合法文件名字符，不构成穿越）
	if runtime.GOOS == "windows" {
		if _, err := StorageFilePath(root, filepath.FromSlash(`..\evil.tgz`)); err == nil {
			t.Fatal("反斜杠穿越路径未被拒绝")
		}
	}
}

// TestExtractPackumentRejectsTraversalVersionKey 上游 packument 携带恶意版本键时，
// 该版本不得入库（其 TarballPath 会拼出存储树外的磁盘路径）。
func TestExtractPackumentRejectsTraversalVersionKey(t *testing.T) {
	raw := []byte(`{"name":"demo","dist-tags":{"latest":"1.0.0"},"versions":{
		"1.0.0":{"version":"1.0.0","dist":{"tarball":"https://example.com/demo-1.0.0.tgz"}},
		"2.0.0-beta.1":{"version":"2.0.0-beta.1","dist":{"tarball":"https://example.com/demo-2.0.0-beta.1.tgz"}},
		"../../../../tmp/x":{"version":"../../../../tmp/x","dist":{"tarball":"https://example.com/x.tgz"}}}}`)
	_, versions, err := ExtractPackument(raw, "demo")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("应只保留 2 个合法版本，实际 %d: %+v", len(versions), versions)
	}
	for _, v := range versions {
		if strings.ContainsAny(v.Version, "/\\") || strings.Contains(v.TarballPath, "..") {
			t.Fatalf("非法版本键入库: %+v", v)
		}
	}
}

// TestServeTarballRefusesTraversalVersionRecord 历史库中已存在越界版本记录时：
// ServeTarball 拒绝服务（500），PreheatPackage 拒绝下载，存储树外不产生任何文件。
func TestServeTarballRefusesTraversalVersionRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	storage := filepath.Join(dir, "storage")
	if err := db.InitSettings("http://127.0.0.1:1", storage, time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	pkgID, err := db.UpsertPackage(&models.Package{
		PkgName:       "demo",
		LatestVersion: "1.0.0",
		MetaCachedAt:  time.Now().UTC().Format(dbTimeFormat),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO npm_versions (pkg_id, version, tarball_path, file_size_bytes)
		VALUES (?, '1.0.0', 'packages/demo/../../../evil.tgz', 0)`, pkgID); err != nil {
		t.Fatal(err)
	}
	// 预热以「meta 文件存在 + DB 有包记录」判定元数据就绪，避免回源
	if err := os.MkdirAll(filepath.Join(storage, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "meta", "demo.json"), []byte(`{"name":"demo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(db, NewStats(db))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/demo/-/demo-1.0.0.tgz", nil)
	engine.ServeTarball(c, "demo", "demo-1.0.0.tgz")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("越界版本记录应拒绝服务返回 500，实际 %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.tgz")); err == nil {
		t.Fatal("不得在存储树外产生文件")
	}
	if _, _, _, err := engine.PreheatPackage("demo", true); err == nil {
		t.Fatal("预热应拒绝越界版本记录")
	} else if !strings.Contains(err.Error(), "路径非法") {
		t.Fatalf("预热错误应为路径非法，实际: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.tgz")); err == nil {
		t.Fatal("预热不得在存储树外产生文件")
	}
}
