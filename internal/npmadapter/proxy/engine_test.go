package proxy

import (
	"bytes"
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
	"permaporter/internal/npmadapter/database"
)

// openTestDB 打开临时单库并挂载 npm 生态查询层。
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

// TestServePackumentRefreshesStaleCacheBeforeServing 回归测试：
// 元数据过期后，第一次请求就必须返回上游的新版本（而不是先返回旧缓存导致客户端 ETARGET）。
func TestServePackumentRefreshesStaleCacheBeforeServing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var current atomic.Value
	current.Store("1.0.0")
	var currentETag atomic.Value
	currentETag.Store(`"v1"`)
	var notModified int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ver := current.Load().(string)
		tag := currentETag.Load().(string)
		if r.Header.Get("If-None-Match") == tag {
			atomic.AddInt32(&notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", tag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"demo","dist-tags":{"latest":"` + ver + `"},"versions":{"` + ver +
			`":{"version":"` + ver + `","dist":{"tarball":"https://example.com/demo-` + ver + `.tgz"}}}}`))
	}))
	defer upstream.Close()

	const ttl = 10 * time.Millisecond
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), ttl); err != nil {
		t.Fatalf("init settings: %v", err)
	}

	engine := NewEngine(db, NewStats(db))

	// 冷缓存：拿到 1.0.0
	if body := servePackument(t, engine, "demo"); !strings.Contains(body, "1.0.0") {
		t.Fatalf("首次请求应包含 1.0.0，实际: %s", body)
	}

	// 上游发布 2.0.0，本地缓存已过期
	current.Store("2.0.0")
	currentETag.Store(`"v2"`)
	time.Sleep(2 * ttl)

	if body := servePackument(t, engine, "demo"); !strings.Contains(body, "2.0.0") {
		t.Fatalf("过期后第一次请求就应返回新版本 2.0.0，实际: %s", body)
	}

	// 再次过期但上游未变化：走 304，仍返回 2.0.0 且不重写版本表
	time.Sleep(2 * ttl)
	genBefore := db.WriteGen()
	if body := servePackument(t, engine, "demo"); !strings.Contains(body, "2.0.0") {
		t.Fatalf("304 后应继续返回 2.0.0，实际: %s", body)
	}
	if atomic.LoadInt32(&notModified) == 0 {
		t.Fatal("第二次过期刷新应发送 If-None-Match 并命中 304")
	}
	// 304 只更新时间戳，不应推进写代次（否则每次刷新都会让页面缓存全量失效）
	if genAfter := db.WriteGen(); genAfter != genBefore {
		t.Fatalf("304 路径不应推进写代次: before=%d after=%d", genBefore, genAfter)
	}
}

// TestServePackumentFallsBackToStaleCacheWhenUpstreamStuck 回归测试：
// 缓存过期但上游卡住时，最多等 MetaRefreshWait 就返回旧缓存（可用性优先），
// 且进入冷却期后不再重复等待同一个慢刷新。
func TestServePackumentFallsBackToStaleCacheWhenUpstreamStuck(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var stuck atomic.Bool
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stuck.Load() {
			<-block // 模拟上游无响应
			return
		}
		_, _ = w.Write([]byte(`{"name":"demo","dist-tags":{"latest":"1.0.0"},` +
			`"versions":{"1.0.0":{"version":"1.0.0","dist":{"tarball":"https://example.com/demo-1.0.0.tgz"}}}}`))
	}))
	defer upstream.Close()
	defer close(block) // 先于 upstream.Close 执行，避免测试结束时等待卡住的请求

	const ttl = 10 * time.Millisecond
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	if err := db.InitSettings(upstream.URL, filepath.Join(dir, "storage"), ttl); err != nil {
		t.Fatalf("init settings: %v", err)
	}

	engine := NewEngine(db, NewStats(db))
	engine.MetaRefreshWait = 50 * time.Millisecond

	// 冷缓存：正常拉取
	if body := servePackument(t, engine, "demo"); !strings.Contains(body, "1.0.0") {
		t.Fatalf("首次请求应包含 1.0.0，实际: %s", body)
	}

	// 上游卡死且缓存过期：必须在上限内返回旧缓存，而不是一直挂着
	stuck.Store(true)
	time.Sleep(2 * ttl)
	begin := time.Now()
	body := servePackument(t, engine, "demo")
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("上游卡住时应在 MetaRefreshWait 附近返回，实际耗时 %s", elapsed)
	}
	if !strings.Contains(body, "1.0.0") {
		t.Fatalf("应回退返回旧缓存 1.0.0，实际: %s", body)
	}
	if !engine.refreshCoolingDown("demo") {
		t.Fatal("超时后应进入刷新冷却期")
	}

	// 冷却期内：不再等待刷新，立即返回旧缓存
	begin = time.Now()
	body = servePackument(t, engine, "demo")
	if elapsed := time.Since(begin); elapsed > engine.MetaRefreshWait {
		t.Fatalf("冷却期内应立即返回旧缓存，实际耗时 %s", elapsed)
	}
	if !strings.Contains(body, "1.0.0") {
		t.Fatalf("冷却期内应返回旧缓存 1.0.0，实际: %s", body)
	}
}

// TestMetaStale 校验过期判定：空值与不可解析时间一律视为过期。
func TestMetaStale(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		value string
		ttl   time.Duration
		want  bool
	}{
		{"空值视为过期", "", time.Minute, true},
		{"无法解析视为过期", "not-a-time", time.Minute, true},
		{"刚刚缓存未过期", now.Format(dbTimeFormat), time.Minute, false},
		{"超过 TTL 视为过期", now.Add(-2 * time.Minute).Format(dbTimeFormat), time.Minute, true},
		{"RFC3339 也支持", now.Format(time.RFC3339Nano), time.Minute, false},
		{"TTL 为 0 用默认值", now.Format(dbTimeFormat), 0, false},
	}
	for _, c := range cases {
		if got := metaStale(c.value, c.ttl); got != c.want {
			t.Errorf("%s: metaStale(%q, %s) = %v, want %v", c.name, c.value, c.ttl, got, c.want)
		}
	}
}

// TestServePackumentRejectsOversizedBody 回归测试：上游返回超过大小上限的 packument
// （max+1 字节）时按上游错误处理返回 502，绝不解析入库或落盘（截断 JSON 可能静默丢失尾部版本）。
func TestServePackumentRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	big := bytes.Repeat([]byte("x"), maxPackumentSize+1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	db := openTestDB(t, dsn)
	storage := filepath.Join(dir, "storage")
	if err := db.InitSettings(upstream.URL, storage, time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := NewEngine(db, NewStats(db))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/big-pkg", nil)
	engine.ServePackument(c, "big-pkg")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("超限响应应 502，实际 %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(storage, "meta", "big-pkg.json")); !os.IsNotExist(err) {
		t.Fatal("超限 packument 不得落盘")
	}
	if _, err := db.GetPackage("big-pkg"); err == nil {
		t.Fatal("超限 packument 不得入库")
	}
}

func servePackument(t *testing.T, engine *Engine, pkgName string) string {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/"+pkgName, nil)
	c.Request.Host = "registry.local"
	engine.ServePackument(c, pkgName)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}
