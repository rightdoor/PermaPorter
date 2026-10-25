package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/middleware"
	godatabase "permaporter/internal/goadapter/database"
	goproxy "permaporter/internal/goadapter/proxy"
)

// newTestApp 搭建 go 生态管理端 App。
func newTestApp(t *testing.T) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=10000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := godatabase.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	if err := db.InitSettings("http://127.0.0.1:1", "", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	return &App{
		Cfg:      &config.Config{},
		DB:       db,
		Engine:   goproxy.NewEngine(db, goproxy.NewStats(db)),
		Sessions: middleware.NewSessionStore(),
		Limiter:  middleware.NewLoginLimiter(),
		Cache:    NewViewCache(db.WriteGen, db.StatsGen, 0),
	}
}

// postBody 发送 JSON POST。
func postBody(handler func(*gin.Context), body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}

// TestSaveUpstreamValidates 上游配置保存：非法 URL 拒绝，合法 URL 生效。
func TestSaveUpstreamValidates(t *testing.T) {
	app := newTestApp(t)

	if w := postBody(app.SaveUpstream, `{"upstreamUrl":"not-a-url"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 URL 应 400: %d %s", w.Code, w.Body.String())
	}
	if w := postBody(app.SaveUpstream, `{"upstreamUrl":"https://proxy.golang.org","fallbackUrl":"https://fallback.example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("合法配置应 200: %d %s", w.Code, w.Body.String())
	}
	settings, err := app.DB.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.UpstreamURL != "https://proxy.golang.org" || settings.FallbackURL != "https://fallback.example.com" {
		t.Fatalf("配置未生效: %+v", settings)
	}
}

// TestSaveProxy 代理配置：非法方案拒绝，合法值生效。
func TestSaveProxy(t *testing.T) {
	app := newTestApp(t)
	if w := postBody(app.SaveProxy, `{"outboundProxy":"ftp://bad"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("非法代理方案应 400: %d", w.Code)
	}
	if w := postBody(app.SaveProxy, `{"outboundProxy":"socks5://127.0.0.1:1080"}`); w.Code != http.StatusOK {
		t.Fatalf("合法代理应 200: %d %s", w.Code, w.Body.String())
	}
}

// TestSettingsAndHealthz 设置读取与健康检查的 JSON 形态。
func TestSettingsAndHealthz(t *testing.T) {
	app := newTestApp(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	app.SettingsAPI(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"upstreamUrl"`) {
		t.Fatalf("settings 异常: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	app.Healthz(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("healthz 异常: %d %s", w.Code, w.Body.String())
	}
}

// TestDashboardAPI 仪表盘空库形态：三段数据齐全且 ok。
func TestDashboardAPI(t *testing.T) {
	app := newTestApp(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/dashboard", nil)
	app.DashboardAPI(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hitRate"`) {
		t.Fatalf("dashboard 异常: %d %s", w.Code, w.Body.String())
	}
}
