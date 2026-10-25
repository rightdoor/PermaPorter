package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/goadapter/database"
)

// newTestEngine 搭建带假上游的测试引擎（metaTTL<=0 时生态配置行存 0，
// GetSettings 兜底为默认 TTL）。
func newTestEngine(t *testing.T, upstream *httptest.Server, metaTTL time.Duration) *Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	if err := db.InitSettings(upstream.URL, "", filepath.Join(dir, "storage"), metaTTL); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	return NewEngine(db, NewStats(db))
}

// openTestDB 打开临时单库并挂载 go 生态查询层。
func openTestDB(t *testing.T, dsn string) *database.DB {
	t.Helper()
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db := database.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

// makeModuleZip 生成符合模块 zip 规范的测试 zip，返回字节数与内容哈希前的路径。
func makeModuleZip(t *testing.T, dir, modulePath, version string) string {
	t.Helper()
	root := filepath.Join(dir, "pkgsrc")
	if err := os.MkdirAll(filepath.Join(root, modulePath+"@"+version), 0o755); err != nil {
		t.Fatal(err)
	}
	readme := "# Test Module\n\nThis is " + modulePath + "@" + version + "\n"
	if err := os.WriteFile(filepath.Join(root, modulePath+"@"+version, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	gomod := "module " + modulePath + "\n\ngo 1.21\n\nrequire github.com/dep/one v1.0.0\n"
	if err := os.WriteFile(filepath.Join(root, modulePath+"@"+version, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(dir, "out.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(f, module.Version{Path: modulePath, Version: version}, filepath.Join(root, "..", "..")); err != nil {
		f.Close()
		t.Fatalf("创建模块 zip 失败: %v", err)
	}
	f.Close()
	return zipPath
}

// dispatch 模拟一次协议请求，返回 recorder。
func dispatch(t *testing.T, e *Engine, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.NoRoute(func(c *gin.Context) {
		req, err := Parse(c.Request.URL.Path)
		if err != nil {
			c.Data(400, "text/plain; charset=utf-8", []byte(err.Error()))
			return
		}
		if req == nil {
			c.Data(404, "text/plain; charset=utf-8", []byte("not found"))
			return
		}
		e.ServeRequest(c, req)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestProtocolMatrix 覆盖 @v/list / @latest / .info / .mod / .zip / .ziphash 的
// 首次回源、二次命中与上游 404 语义。
func TestProtocolMatrix(t *testing.T) {
	zipPath := makeModuleZip(t, t.TempDir(), "example.com/demo", "v1.0.0")
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	var upstreamRequests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamRequests, 1)
		p := r.URL.Path
		switch {
		case p == "/example.com/demo/@v/list":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("v1.0.0\nv0.9.0\nv0.0.0-20200101000000-abcdef123456\nbadline\n"))
		case p == "/example.com/demo/@latest":
			_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2026-01-02T03:04:05Z"}`))
		case p == "/example.com/demo/@v/v1.0.0.info":
			_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2026-01-02T03:04:05Z"}`))
		case p == "/example.com/demo/@v/v1.0.0.mod":
			_, _ = w.Write([]byte("module example.com/demo\n\ngo 1.21\n\nrequire github.com/dep/one v1.0.0\n"))
		case p == "/example.com/demo/@v/v1.0.0.zip":
			_, _ = w.Write(zipBytes)
		case p == "/example.com/demo/@v/v1.0.0.ziphash":
			// 上游不提供 ziphash（部分上游代理如此）
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	e := newTestEngine(t, upstream, 0)

	// list：200 text/plain，过滤伪版本与非法行，semver 升序
	w := dispatch(t, e, "GET", "/example.com/demo/@v/list")
	if w.Code != 200 {
		t.Fatalf("list 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("list Content-Type 应为 text/plain: %s", ct)
	}
	if got := w.Body.String(); got != "v0.9.0\nv1.0.0\n" {
		t.Fatalf("list 规范化结果不符: %q", got)
	}

	// latest：200 JSON
	w = dispatch(t, e, "GET", "/example.com/demo/@latest")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"Version":"v1.0.0"`) {
		t.Fatalf("latest 响应异常: %d %s", w.Code, w.Body.String())
	}

	// info / mod / zip
	w = dispatch(t, e, "GET", "/example.com/demo/@v/v1.0.0.info")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"v1.0.0"`) {
		t.Fatalf("info 响应异常: %d %s", w.Code, w.Body.String())
	}
	w = dispatch(t, e, "GET", "/example.com/demo/@v/v1.0.0.mod")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "module example.com/demo") {
		t.Fatalf("mod 响应异常: %d %s", w.Code, w.Body.String())
	}
	w = dispatch(t, e, "GET", "/example.com/demo/@v/v1.0.0.zip")
	if w.Code != 200 {
		t.Fatalf("zip 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() != len(zipBytes) {
		t.Fatalf("zip 内容长度不符: %d != %d", w.Body.Len(), len(zipBytes))
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("zip 应带 immutable 缓存头: %s", cc)
	}

	// ziphash：本地计算（上游 404 不提供），h1: 前缀
	w = dispatch(t, e, "GET", "/example.com/demo/@v/v1.0.0.ziphash")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "h1:") {
		t.Fatalf("ziphash 应 200 且以 h1: 开头: %d %q", w.Code, w.Body.String())
	}

	// 二次请求应命中缓存（上游请求数不再增长）
	before := atomic.LoadInt32(&upstreamRequests)
	for _, p := range []string{"/example.com/demo/@v/list", "/example.com/demo/@v/v1.0.0.info",
		"/example.com/demo/@v/v1.0.0.mod", "/example.com/demo/@v/v1.0.0.zip"} {
		if w := dispatch(t, e, "GET", p); w.Code != 200 {
			t.Fatalf("二次请求 %s 应 200: %d", p, w.Code)
		}
	}
	if after := atomic.LoadInt32(&upstreamRequests); after != before {
		t.Fatalf("二次请求不应回源: before=%d after=%d", before, after)
	}

	// 未知模块：404 text/plain
	w = dispatch(t, e, "GET", "/example.com/not-exist/@v/list")
	if w.Code != 404 {
		t.Fatalf("未知模块应 404: %d", w.Code)
	}
	// 非法路径：400
	if w := dispatch(t, e, "GET", "/example.com/x/@v/master.zip"); w.Code != 400 {
		t.Fatalf("非规范版本 zip 应 400: %d", w.Code)
	}
	// POST：405
	if w := dispatch(t, e, "POST", "/example.com/demo/@v/list"); w.Code != 405 {
		t.Fatalf("POST 应 405: %d", w.Code)
	}
	// HEAD zip：200 无 body
	w = dispatch(t, e, "HEAD", "/example.com/demo/@v/v1.0.0.zip")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD zip 应 200 且无 body: %d %d", w.Code, w.Body.Len())
	}
}

// TestEscapedUppercaseModule 大小写编码：磁盘与上游都使用编码形式。
func TestEscapedUppercaseModule(t *testing.T) {
	zipPath := makeModuleZip(t, t.TempDir(), "github.com/BurntSushi/toml", "v1.4.0")
	zipBytes, _ := os.ReadFile(zipPath)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/github.com/!burnt!sushi/toml/@v/list":
			_, _ = w.Write([]byte("v1.4.0\n"))
		case r.URL.Path == "/github.com/!burnt!sushi/toml/@v/v1.4.0.info":
			_, _ = w.Write([]byte(`{"Version":"v1.4.0","Time":"2026-01-01T00:00:00Z"}`))
		case r.URL.Path == "/github.com/!burnt!sushi/toml/@v/v1.4.0.mod":
			_, _ = w.Write([]byte("module github.com/BurntSushi/toml\n\ngo 1.17\n"))
		case r.URL.Path == "/github.com/!burnt!sushi/toml/@v/v1.4.0.zip":
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "t.db")) + "?_busy_timeout=10000&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	if err := db.InitSettings(upstream.URL, "", filepath.Join(dir, "storage"), 0); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(db, NewStats(db))

	if w := dispatch(t, e, "GET", "/github.com/!burnt!sushi/toml/@v/list"); w.Code != 200 {
		t.Fatalf("编码模块 list 应 200: %d", w.Code)
	}
	if w := dispatch(t, e, "GET", "/github.com/!burnt!sushi/toml/@v/v1.4.0.zip"); w.Code != 200 {
		t.Fatalf("编码模块 zip 应 200: %d", w.Code)
	}
	// 磁盘目录使用编码形式
	enc := filepath.Join(dir, "storage", "modules", "github.com", "!burnt!sushi", "toml", "@v")
	if fi, err := os.Stat(filepath.Join(enc, "v1.4.0.zip")); err != nil || fi.IsDir() {
		t.Fatalf("zip 应落在编码目录: %v", err)
	}
	// 裸大写路径在 GOPROXY 协议中非法（必须 ! 编码）：400
	if w := dispatch(t, e, "GET", "/github.com/BurntSushi/toml/@v/list"); w.Code != 400 {
		t.Fatalf("裸大写路径应 400: %d", w.Code)
	}
}

// TestOfflineAfterCache 上游不可达后，已缓存产物仍可安装（离线可用性）。
func TestOfflineAfterCache(t *testing.T) {
	zipPath := makeModuleZip(t, t.TempDir(), "example.com/offline", "v1.2.3")
	zipBytes, _ := os.ReadFile(zipPath)
	var alive atomic.Bool
	alive.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !alive.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch {
		case r.URL.Path == "/example.com/offline/@v/list":
			_, _ = w.Write([]byte("v1.2.3\n"))
		case r.URL.Path == "/example.com/offline/@latest":
			_, _ = w.Write([]byte(`{"Version":"v1.2.3","Time":"2026-01-01T00:00:00Z"}`))
		case r.URL.Path == "/example.com/offline/@v/v1.2.3.info":
			_, _ = w.Write([]byte(`{"Version":"v1.2.3","Time":"2026-01-01T00:00:00Z"}`))
		case r.URL.Path == "/example.com/offline/@v/v1.2.3.mod":
			_, _ = w.Write([]byte("module example.com/offline\n\ngo 1.21\n"))
		case r.URL.Path == "/example.com/offline/@v/v1.2.3.zip":
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	e := newTestEngine(t, upstream, 0)
	// 在线缓存全部产物
	for _, p := range []string{"/example.com/offline/@v/list", "/example.com/offline/@latest",
		"/example.com/offline/@v/v1.2.3.info", "/example.com/offline/@v/v1.2.3.mod",
		"/example.com/offline/@v/v1.2.3.zip"} {
		if w := dispatch(t, e, "GET", p); w.Code != 200 {
			t.Fatalf("在线缓存 %s 失败: %d", p, w.Code)
		}
	}
	// 模拟上游彻底不可达（关掉服务）
	alive.Store(false)
	upstream.Close()

	// 离线后：list（TTL 过期）先返回旧缓存，info/mod/zip 命中磁盘
	if w := dispatch(t, e, "GET", "/example.com/offline/@v/list"); w.Code != 200 || w.Body.String() != "v1.2.3\n" {
		t.Fatalf("离线 list 应返回旧缓存: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/example.com/offline/@latest",
		"/example.com/offline/@v/v1.2.3.info", "/example.com/offline/@v/v1.2.3.mod",
		"/example.com/offline/@v/v1.2.3.zip"} {
		if w := dispatch(t, e, "GET", p); w.Code != 200 {
			t.Fatalf("离线 %s 应命中缓存: %d %s", p, w.Code, w.Body.String())
		}
	}
	// 离线未缓存模块：上游网络错误返回 502
	//（客户端可配置 GOPROXY=host|direct 使任意错误回退 direct）
	if w := dispatch(t, e, "GET", "/example.com/never-cached/@v/list"); w.Code != 502 {
		t.Fatalf("离线未知模块应 502: %d", w.Code)
	}
}

// TestZipDownloadNoGateSelfLock 回归测试（并发闸自锁）：
// 16 个不同模块并发下载 zip、上游提供 .ziphash 时不得互相等待。
// 历史缺陷：zip 校验期间持有上游闸，而 ziphash 探测会再次申请同一把闸，
// 恰好 16 个并发下载时全部持槽等待第 17 个槽位，互相等待直至 30 分钟超时。
func TestZipDownloadNoGateSelfLock(t *testing.T) {
	const modules = 16
	const version = "v1.0.0"

	type modCase struct {
		path    string
		zip     []byte
		ziphash string
	}
	cases := make([]modCase, modules)
	for i := range cases {
		path := fmt.Sprintf("example.com/m%02d", i)
		zipBytes, hash, err := buildCleanModuleZip(t, path, version)
		if err != nil {
			t.Fatalf("构建 %s 测试 zip 失败: %v", path, err)
		}
		cases[i] = modCase{path: path, zip: zipBytes, ziphash: hash}
	}
	byPath := map[string]modCase{}
	for _, c := range cases {
		byPath[c.path] = c
	}

	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	var ziphashServed int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inflight--
			mu.Unlock()
		}()
		// 模块路径形如 /example.com/mNN/@v/v1.0.0.<ext>
		rest := strings.TrimPrefix(r.URL.Path, "/")
		i := strings.Index(rest, "/@v/")
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		modPath, file := rest[:i], rest[i+len("/@v/"):]
		c, ok := byPath[modPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch file {
		case version + ".zip":
			w.Write(c.zip)
		case version + ".ziphash":
			atomic.AddInt32(&ziphashServed, 1)
			w.Write([]byte(c.ziphash + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	e := newTestEngine(t, upstream, 0)
	settings, err := e.DB.GetSettings()
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}

	errs := make([]error, modules)
	var wg sync.WaitGroup
	for i := 0; i < modules; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, perr := Parse(fmt.Sprintf("/%s/@v/%s.zip", cases[i].path, version))
			if perr != nil {
				errs[i] = perr
				return
			}
			_, errs[i] = e.ensureZip(settings, req)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("16 并发 zip 下载超时（疑似下载闸与 ziphash 探测自锁）")
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("模块 %d 下载失败: %v", i, err)
		}
	}
	if n := atomic.LoadInt32(&ziphashServed); n != modules {
		t.Fatalf("上游 .ziphash 应被请求 %d 次（每次下载探测一次），实际 %d 次", modules, n)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxInflight > upstreamMaxConcurrent {
		t.Fatalf("上游并发请求峰值 %d 超过上限 %d", maxInflight, upstreamMaxConcurrent)
	}
}

// buildCleanModuleZip 生成内容完整的模块 zip：源目录即模块内容根（go.mod 所在目录），
// 输出文件不在压缩范围内；返回 zip 字节与 h1 校验和。
// 注意 modzip.CreateFromDir 会跳过"含 go.mod 的子目录"（视为子模块），
// 因此必须把 <mod>@<ver> 目录本身作为根传入，而不是它的父目录。
func buildCleanModuleZip(t *testing.T, path, version string) ([]byte, string, error) {
	t.Helper()
	modRoot := filepath.Join(t.TempDir(), path+"@"+version)
	if err := os.MkdirAll(modRoot, 0o755); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(modRoot, "go.mod"),
		[]byte("module "+path+"\n\ngo 1.21\n"), 0o644); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(modRoot, "README.md"),
		[]byte("# "+path+"\n\ntest module "+version+"\n"), 0o644); err != nil {
		return nil, "", err
	}
	out := filepath.Join(t.TempDir(), "out.zip")
	f, err := os.Create(out)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	if err := modzip.CreateFromDir(f, module.Version{Path: path, Version: version}, modRoot); err != nil {
		return nil, "", err
	}
	hash, err := HashZipFile(out)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, "", err
	}
	return data, hash, nil
}

// TestRangeRequest zip 支持 Range 分段。
func TestRangeRequest(t *testing.T) {
	zipPath := makeModuleZip(t, t.TempDir(), "example.com/rng", "v0.1.0")
	zipBytes, _ := os.ReadFile(zipPath)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/rng/@v/v0.1.0.info":
			_, _ = w.Write([]byte(`{"Version":"v0.1.0"}`))
		case "/example.com/rng/@v/v0.1.0.mod":
			_, _ = w.Write([]byte("module example.com/rng\n"))
		case "/example.com/rng/@v/v0.1.0.zip":
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	e := newTestEngine(t, upstream, 0)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.NoRoute(func(c *gin.Context) {
		req, _ := Parse(c.Request.URL.Path)
		e.ServeRequest(c, req)
	})

	// 首次下载缓存
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/example.com/rng/@v/v0.1.0.zip", nil))
	if w.Code != 200 {
		t.Fatalf("首次下载失败: %d", w.Code)
	}
	// Range 请求
	w = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/example.com/rng/@v/v0.1.0.zip", nil)
	req.Header.Set("Range", "bytes=0-99")
	r.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.Len() != 100 {
		t.Fatalf("Range 请求应 206 且返回 100 字节: %d %d", w.Code, w.Body.Len())
	}
}
