// Package web 的协议端口契约测试（9092 清单）：
// 只挂 /healthz /metrics 与 NoRoute 协议分发；错误一律 text/plain；只读（405）。
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
	godatabase "permaporter/internal/goadapter/database"
)

// newTestRouter 组装 9092 协议端口引擎（假上游不可达，用于验证错误形态）。
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	store, err := coredatabase.Open("file:" + filepath.ToSlash(filepath.Join(dir, "permaporter.db")) + "?_busy_timeout=10000&_journal_mode=DELETE")
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := godatabase.Attach(store)
	t.Cleanup(db.Close)
	if err := db.InitSettings("http://127.0.0.1:1", "", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	app := NewApp(&config.Config{}, db, middleware.NewSessionStore(), middleware.NewLoginLimiter())
	r := gin.New()
	RegisterProtocolRoutes(r, app)
	return r
}

// TestGOPortContract 9092 端口契约：healthz/metrics 本口端点、协议错误 text/plain、
// 非 GET/HEAD 405、UI 路径不存在（text 404，保留 GOPROXY 链式回退语义）。
func TestGOPortContract(t *testing.T) {
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
	if !strings.Contains(w.Body.String(), "go_permaporter_") {
		t.Fatalf("metrics 应含 go_permaporter_* 指标族")
	}

	// /admin：非浏览器形态 → text/plain 404
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("/admin 应为 text 404: %d %s", w.Code, w.Header().Get("Content-Type"))
	}

	// /api、/static：协议端口不注册 UI 数据接口
	for _, p := range []string{"/api/modules", "/static/logo.png"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s 应为 text 404: %d %s", p, w.Code, w.Header().Get("Content-Type"))
		}
	}

	// 协议路径：上游不可达 → 502 text/plain
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/example.com/demo/@v/list", nil))
	if w.Code != http.StatusBadGateway || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("上游不可达应为 502 text/plain: %d %s", w.Code, w.Header().Get("Content-Type"))
	}

	// 发布类请求：405 text/plain
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/example.com/demo/@v/list", nil))
	if w.Code != http.StatusMethodNotAllowed || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("PUT 应为 405 text/plain: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}
