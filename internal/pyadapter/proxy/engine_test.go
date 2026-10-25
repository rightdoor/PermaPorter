package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/models"
)

// atomicString 并发安全的字符串（模拟上游 ETag 变化）。
type atomicString struct{ v atomic.Value }

func (s *atomicString) Store(v string) { s.v.Store(v) }
func (s *atomicString) Load() string   { return s.v.Load().(string) }

// TestNormalizeProjectName PEP 503 规范化：-_. 折叠为 - 并转小写。
func TestNormalizeProjectName(t *testing.T) {
	cases := map[string]string{
		"Typing_extensions": "typing-extensions",
		"Django..py":        "django-py",
		"Flask---CLI":       "flask-cli",
		"numpy":             "numpy",
	}
	for in, want := range cases {
		if got := NormalizeProjectName(in); got != want {
			t.Errorf("NormalizeProjectName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVersionFromFilename wheel 与 sdist 文件名的版本解析。
func TestVersionFromFilename(t *testing.T) {
	cases := map[string]string{
		"typing_extensions-4.12.2-py3-none-any.whl": "4.12.2",
		"numpy-1.26.0-cp312-cp312-win_amd64.whl":    "1.26.0",
		"requests-2.31.0.tar.gz":                    "2.31.0",
		"demo_pkg-0.1.0.zip":                        "0.1.0",
		"weird-file.whl":                            "",
	}
	for in, want := range cases {
		if got := VersionFromFilename(in); got != want {
			t.Errorf("VersionFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// openTestDB 打开临时单库并挂载 py 生态查询层。
func openTestDB(t *testing.T, dir string) *database.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db := database.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

func fileSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestServeProjectPageAndFile 回归测试：锚点页 TTL 过期后第一次请求返回新版本；
// 文件按锚点页 sha256 校验后落盘，二次请求零拷贝命中。
func TestServeProjectPageAndFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	wheel := []byte("demo-wheel-bytes")
	targz := []byte("demo-sdist-bytes")
	var etag atomicString
	etag.Store(`"v1"`)
	var upstreamHits int
	// 先声明再赋值：handler 闭包内需要引用 upstream.URL 拼锚点地址
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		switch {
		case r.URL.Path == "/demo-pkg/":
			// 引擎以 settings.UpstreamURL 为 Simple 根，项目页路径相对该根拼接
			if r.Header.Get("If-None-Match") == etag.Load() {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag.Load())
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>
				<a href="` + upstream.URL + `/packages/demo_pkg-0.1.0-py3-none-any.whl#sha256=` + fileSHA(wheel) + `" data-requires-python="&gt;=3.8">demo_pkg-0.1.0-py3-none-any.whl</a><br/>
				<a href="` + upstream.URL + `/packages/demo-0.1.0.tar.gz">demo-0.1.0.tar.gz</a><br/>
			</body></html>`))
		case r.URL.Path == "/packages/demo_pkg-0.1.0-py3-none-any.whl":
			_, _ = w.Write(wheel)
		case r.URL.Path == "/packages/demo-0.1.0.tar.gz":
			_, _ = w.Write(targz)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	const ttl = 10 * time.Millisecond
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), ttl); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	// 冷缓存：锚点页解析入库并返回锚点 HTML（含 sha256 片段与 requires-python）
	w := serve(func(c *gin.Context) { engine.ServeProjectPage(c, "Demo.Pkg") })
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "demo_pkg-0.1.0-py3-none-any.whl#sha256="+fileSHA(wheel)) {
		t.Fatalf("锚点页应含文件锚点: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `data-requires-python="&gt;=3.8"`) {
		t.Fatalf("锚点页应保留 requires-python: %s", w.Body.String())
	}
	// 规范化名入库；latest 版本取自 wheel 文件名
	proj, err := db.GetProject("demo-pkg")
	if err != nil || proj.LatestVersion != "0.1.0" {
		t.Fatalf("项目应按规范化名入库且 latest=0.1.0: %+v %v", proj, err)
	}

	// 文件下载：sha256 校验通过后落盘
	w = serve(func(c *gin.Context) { engine.ServePackageFile(c, "demo_pkg-0.1.0-py3-none-any.whl") })
	if w.Code != http.StatusOK || w.Body.String() != string(wheel) {
		t.Fatalf("文件应按锚点下载: %d %q", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "packages", "demo_pkg-0.1.0-py3-none-any.whl")); err != nil {
		t.Fatalf("文件应已落盘: %v", err)
	}
	// 二次请求命中本地
	before := upstreamHits
	w = serve(func(c *gin.Context) { engine.ServePackageFile(c, "demo_pkg-0.1.0-py3-none-any.whl") })
	if w.Code != http.StatusOK || upstreamHits != before {
		t.Fatalf("二次文件请求应命中本地缓存: %d", w.Code)
	}

	// 锚点页过期、上游内容未变化：条件请求命中 304，只刷新时间戳且不重写文件表
	time.Sleep(2 * ttl)
	genBefore := db.WriteGen()
	w = serve(func(c *gin.Context) { engine.ServeProjectPage(c, "demo-pkg") })
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "demo_pkg-0.1.0-py3-none-any.whl") {
		t.Fatalf("304 后应继续返回旧锚点页: %d", w.Code)
	}
	if genAfter := db.WriteGen(); genAfter != genBefore {
		t.Fatalf("304 路径不应推进写代次: before=%d after=%d", genBefore, genAfter)
	}
}

// TestServeSimpleIndex 根索引列出本地已收录项目。
func TestServeSimpleIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings("http://127.0.0.1:1", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))
	if _, err := db.UpsertProject(&models.Project{ProjectName: "demo-pkg"}); err != nil {
		t.Fatal(err)
	}
	w := serve(func(c *gin.Context) { engine.ServeSimpleIndex(c) })
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="demo-pkg/"`) {
		t.Fatalf("根索引应列出项目: %d %s", w.Code, w.Body.String())
	}
}

// TestPreheatProjectLandsInPackagesDir 回归测试（预热落盘布局）：
// 预热文件必须落在 storage/packages/<filename>（与正常请求路径一致，清理器只扫描
// packages/），重复预热已缓存项目零下载，体积统计取实际落盘大小。
func TestPreheatProjectLandsInPackagesDir(t *testing.T) {
	gin.SetMode(gin.TestMode)

	wheel := []byte("preheat-wheel-content-0123456789")
	var upstream *httptest.Server
	var fileDownloads atomic.Int32
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/demo-pkg/":
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>
				<a href="` + upstream.URL + `/packages/demo_pkg-0.1.0-py3-none-any.whl#sha256=` + fileSHA(wheel) + `">demo_pkg-0.1.0-py3-none-any.whl</a><br/>
			</body></html>`))
		case r.URL.Path == "/packages/demo_pkg-0.1.0-py3-none-any.whl":
			fileDownloads.Add(1)
			_, _ = w.Write(wheel)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	// 第一次预热：下载并落盘到 packages/
	state, ver, size, err := engine.PreheatProject("demo-pkg", "", true)
	if err != nil {
		t.Fatalf("首次预热失败: %v", err)
	}
	if state != "done" || ver != "0.1.0" {
		t.Fatalf("首次预热 state/version 异常: %s %s", state, ver)
	}
	if size != int64(len(wheel)) {
		t.Fatalf("体积统计应取实际文件大小 %d，实际 %d", len(wheel), size)
	}
	want := filepath.Join(dir, "storage", "packages", "demo_pkg-0.1.0-py3-none-any.whl")
	if fi, err := os.Stat(want); err != nil || fi.IsDir() {
		t.Fatalf("预热文件应落在 packages/ 下: %v", err)
	}
	// 存储根不得出现孤立文件（历史缺陷落盘点）
	if entries, _ := os.ReadDir(filepath.Join(dir, "storage")); len(entries) != 1 || entries[0].Name() != "packages" {
		t.Fatalf("存储根只应包含 packages/ 目录")
	}
	if n := fileDownloads.Load(); n != 1 {
		t.Fatalf("首次预热应恰好下载 1 次，实际 %d 次", n)
	}

	// 第二次预热：全部命中 DB 缓存状态，零下载
	state, _, size2, err := engine.PreheatProject("demo-pkg", "", true)
	if err != nil {
		t.Fatalf("重复预热失败: %v", err)
	}
	if state != "cached" {
		t.Fatalf("重复预热应返回 cached，实际 %s", state)
	}
	if size2 != int64(len(wheel)) {
		t.Fatalf("重复预热体积统计应稳定: %d", size2)
	}
	if n := fileDownloads.Load(); n != 1 {
		t.Fatalf("重复预热不得再次下载（应仍为 1 次，实际 %d 次）", n)
	}
}

// TestServeProjectPageRejectsOversizedBody 回归测试：上游返回超过大小上限的锚点页
// （max+1 字节）时按上游错误处理返回 502，绝不解析入库（截断 HTML 会静默丢失尾部发行文件）。
func TestServeProjectPageRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	big := bytes.Repeat([]byte("x"), maxIndexSize+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	w := serve(func(c *gin.Context) { engine.ServeProjectPage(c, "big-pkg") })
	if w.Code != http.StatusBadGateway {
		t.Fatalf("超限响应应 502，实际 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("错误响应应为 text/plain: %s", ct)
	}
	if _, err := db.GetProject("big-pkg"); err == nil {
		t.Fatal("超限锚点页不得入库")
	}
}

// TestServeProjectPageAndFileWithRelativeAnchors 回归测试（阿里云镜像形态）：
// 上游锚点页使用相对锚点（../../packages/… 与裸文件名）时，解析入库的
// SourceURL 必须是相对锚点页自身 URL 解析出的绝对地址，文件按该地址回源成功。
func TestServeProjectPageAndFileWithRelativeAnchors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	wheel := []byte("relative-anchor-wheel-bytes")
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rel-pkg/":
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>
				<a href="../../packages/rel_pkg-0.3.0-py3-none-any.whl#sha256=` + fileSHA(wheel) + `">rel_pkg-0.3.0-py3-none-any.whl</a><br/>
				<a href="rel_pkg-0.3.0.tar.gz">rel_pkg-0.3.0.tar.gz</a><br/>
			</body></html>`))
		case r.URL.Path == "/packages/rel_pkg-0.3.0-py3-none-any.whl":
			_, _ = w.Write(wheel)
		case r.URL.Path == "/rel-pkg/rel_pkg-0.3.0.tar.gz":
			_, _ = w.Write([]byte("bare-name-sdist"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	// 锚点页解析入库：SourceURL 应为绝对地址（裸文件名相对锚点页本身）
	w := serve(func(c *gin.Context) { engine.ServeProjectPage(c, "rel-pkg") })
	if w.Code != http.StatusOK {
		t.Fatalf("锚点页请求失败: %d %s", w.Code, w.Body.String())
	}
	if rec, err := db.GetFileByName("rel_pkg-0.3.0-py3-none-any.whl"); err != nil || rec.SourceURL != upstream.URL+"/packages/rel_pkg-0.3.0-py3-none-any.whl" {
		t.Fatalf("相对锚点 SourceURL 应为绝对地址: %+v %v", rec, err)
	}
	if rec, err := db.GetFileByName("rel_pkg-0.3.0.tar.gz"); err != nil || rec.SourceURL != upstream.URL+"/rel-pkg/rel_pkg-0.3.0.tar.gz" {
		t.Fatalf("裸文件名 SourceURL 应为绝对地址: %+v %v", rec, err)
	}

	// 文件按解析出的绝对地址回源（sha256 校验链不变）
	w = serve(func(c *gin.Context) { engine.ServePackageFile(c, "rel_pkg-0.3.0-py3-none-any.whl") })
	if w.Code != http.StatusOK || w.Body.String() != string(wheel) {
		t.Fatalf("相对锚点文件应回源成功: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Proxy-Cache") != "MISS" {
		t.Fatalf("首次文件请求应为 MISS: %s", w.Header().Get("X-Proxy-Cache"))
	}
	// 二次请求命中本地缓存
	w = serve(func(c *gin.Context) { engine.ServePackageFile(c, "rel_pkg-0.3.0-py3-none-any.whl") })
	if w.Code != http.StatusOK || w.Header().Get("X-Proxy-Cache") != "HIT" {
		t.Fatalf("二次文件请求应 HIT: %d %s", w.Code, w.Header().Get("X-Proxy-Cache"))
	}
}

// TestServePackageFileHealsLegacyRelativeSourceURL 回归测试（存量自愈）：
// 历史缺陷落库的相对 SourceURL（../../packages/…）无需迁移，回源时按当前上游
// 锚点页解析为绝对地址并排在原样值之前，首个候选即命中。
func TestServePackageFileHealsLegacyRelativeSourceURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	wheel := []byte("legacy-relative-source-bytes")
	var resolvedHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// 首个候选：按上游锚点页 http://<upstream>/legacy-pkg/ 解析 ../../packages/… 的结果
		case r.URL.Path == "/packages/0e/f9/66471f9b/legacy_pkg-0.2.0-py3-none-any.whl":
			resolvedHits.Add(1)
			_, _ = w.Write(wheel)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	db := openTestDB(t, dir)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	// 直接播种存量形态：项目行 + 相对 SourceURL 的文件记录（不经锚点页解析）
	projectID, err := db.UpsertProject(&models.Project{ProjectName: "legacy-pkg", DisplayName: "legacy_pkg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFiles(projectID, []models.File{{
		ProjectID: projectID, Version: "0.2.0", Filename: "legacy_pkg-0.2.0-py3-none-any.whl",
		SHA256: fileSHA(wheel), SourceURL: "../../packages/0e/f9/66471f9b/legacy_pkg-0.2.0-py3-none-any.whl",
	}}); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(db, NewStats(db))

	w := serve(func(c *gin.Context) { engine.ServePackageFile(c, "legacy_pkg-0.2.0-py3-none-any.whl") })
	if w.Code != http.StatusOK || w.Body.String() != string(wheel) {
		t.Fatalf("存量相对 SourceURL 应自愈回源: %d %q", w.Code, w.Body.String())
	}
	if n := resolvedHits.Load(); n != 1 {
		t.Fatalf("应恰好命中解析后的绝对地址 1 次，实际 %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "storage", "packages", "legacy_pkg-0.2.0-py3-none-any.whl")); err != nil {
		t.Fatalf("文件应落盘: %v", err)
	}
}

func serve(h func(*gin.Context)) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h(c)
	return w
}
