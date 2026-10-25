// Package web 的协议端口契约测试（9091 清单）：
// 只挂 /healthz /metrics 与 NoRoute 协议分发；错误一律 JSON；只读（405）。
package web

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
	npmdatabase "permaporter/internal/npmadapter/database"
)

// newTestRouter 组装 9091 协议端口引擎（假上游不可达，用于验证错误形态）。
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	store, err := coredatabase.Open("file:" + filepath.ToSlash(filepath.Join(dir, "permaporter.db")) + "?_busy_timeout=10000&_journal_mode=DELETE")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := npmdatabase.Attach(store)
	t.Cleanup(db.Close)
	if err := db.InitSettings("http://127.0.0.1:1", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	app := NewApp(&config.Config{}, db, middleware.NewSessionStore(), middleware.NewLoginLimiter())
	r := gin.New()
	RegisterProtocolRoutes(r, app)
	return r
}

// TestNpmPortContract 9091 端口契约：healthz/metrics 本口端点、协议错误 JSON、
// 非 GET/HEAD 405（JSON）、UI 路径不存在（JSON 404）。
func TestNpmPortContract(t *testing.T) {
	r := newTestRouter(t)

	// healthz：200 JSON
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("healthz 异常: %d %s", w.Code, w.Body.String())
	}

	// metrics：200 text/plain 且含本生态指标族
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics 异常: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "npm_permaporter_") {
		t.Fatalf("metrics 应含 npm_permaporter_* 指标族")
	}

	// /admin：非浏览器形态 → JSON 404 {"error":...}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("/admin 应为 JSON 404: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("/admin 响应应为 {\"error\":…}: %s", w.Body.String())
	}

	// /api、/static：协议端口不注册 UI 数据接口（非包名形态 → JSON 404）
	for _, p := range []string{"/api/modules", "/static/logo.png"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s 应为 JSON 404: %d %s", p, w.Code, w.Header().Get("Content-Type"))
		}
	}

	// 包元数据：上游不可达 → 502 JSON {"error":…}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/demo-pkg", nil))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("上游不可达应为 502 JSON: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("错误响应应为 JSON: %s", w.Body.String())
	}

	// 发布类请求：405 JSON
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/demo-pkg", nil))
	if w.Code != http.StatusMethodNotAllowed || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("PUT 应为 405 JSON: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}
