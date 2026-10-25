package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newConsoleRouter 完整注册 9090 控制台路由（真实单库 + 替身生态句柄）。
func newConsoleRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	con := newTestConsole(t)
	r := gin.New()
	RegisterConsoleRoutes(r, con)
	return r
}

// postJSON 发送 JSON POST 请求。
func postJSON(r *gin.Engine, path, body string, cookie ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range cookie {
		req.AddCookie(&http.Cookie{Name: "permaporter_session", Value: ck})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestConsoleSetupLoginFlow 首次初始化 → 登录（含错误密码）→ 管理 API 鉴权 → 登出的完整链路。
func TestConsoleSetupLoginFlow(t *testing.T) {
	r := newConsoleRouter(t)

	// 初始状态：未初始化，state 注入四生态协议端口地址
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"initialized":false`) {
		t.Fatalf("state 应为未初始化: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"goProxy"`) || !strings.Contains(w.Body.String(), `"npmRegistry"`) ||
		!strings.Contains(w.Body.String(), `"pyIndex"`) {
		t.Fatalf("state 应注入四生态协议端口地址: %s", w.Body.String())
	}

	// 重复密码不一致 → 400
	if w := postJSON(r, "/api/auth/setup", `{"password":"abc123","confirm":"abc124"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("密码不一致应 400: %d %s", w.Code, w.Body.String())
	}
	// 正常初始化 → 200
	if w := postJSON(r, "/api/auth/setup", `{"password":"secret-1","confirm":"secret-1"}`); w.Code != http.StatusOK {
		t.Fatalf("初始化失败: %d %s", w.Code, w.Body.String())
	}
	// 重复初始化 → 409
	if w := postJSON(r, "/api/auth/setup", `{"password":"secret-1","confirm":"secret-1"}`); w.Code != http.StatusConflict {
		t.Fatalf("重复初始化应 409: %d", w.Code)
	}

	// 错误密码 → 401（提示剩余次数）
	w = postJSON(r, "/api/auth/login", `{"password":"wrong"}`)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "还可尝试") {
		t.Fatalf("错误密码应 401: %d %s", w.Code, w.Body.String())
	}

	// 未登录访问管理 API → 401
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401: %d", w.Code)
	}

	// 正确密码登录 → 会话 Cookie
	w = postJSON(r, "/api/auth/login", `{"password":"secret-1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var token string
	for _, ck := range cookies {
		if ck.Name == "permaporter_session" {
			token = ck.Value
		}
	}
	if token == "" {
		t.Fatal("登录应下发会话 Cookie")
	}

	// 带会话访问聚合设置 → 200 且四生态各一段
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.AddCookie(&http.Cookie{Name: "permaporter_session", Value: token})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("聚合设置应 200: %d %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("聚合设置应为 JSON: %v", err)
	}
	for _, eco := range []string{"go", "npm", "py"} {
		if m[eco] == nil {
			t.Fatalf("聚合设置应含 %s 段: %v", eco, m)
		}
	}

	// 旧密码错误 → 400
	if w := postJSON(r, "/api/admin/settings/password", `{"oldPassword":"wrong","newPassword":"secret-2","confirm":"secret-2"}`, token); w.Code != http.StatusBadRequest {
		t.Fatalf("旧密码错误应 400: %d %s", w.Code, w.Body.String())
	}
	// 修改密码（携带正确旧密码）→ 200
	if w := postJSON(r, "/api/admin/settings/password", `{"oldPassword":"secret-1","newPassword":"secret-2","confirm":"secret-2"}`, token); w.Code != http.StatusOK {
		t.Fatalf("修改密码失败: %d %s", w.Code, w.Body.String())
	}

	// 登出后再访问管理 API → 401
	postJSON(r, "/api/auth/logout", `{}`, token)
	req = httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.AddCookie(&http.Cookie{Name: "permaporter_session", Value: token})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("登出后应 401: %d", w.Code)
	}
}

// TestConsoleSPARoutes SPA 外壳：浏览器路径回落 HTML；API 形态 JSON 404。
func TestConsoleSPARoutes(t *testing.T) {
	r := newConsoleRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("根路径应输出 SPA 外壳: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/admin 应输出 SPA 外壳: %d", w.Code)
	}
}

// TestAccessLogAndResponseTime 访问日志与 X-Response-Time 中间件。
func TestAccessLogAndResponseTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AccessLog(""), ResponseTime(), gin.Recovery())
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "fine") })
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if w.Code != http.StatusOK || w.Header().Get("X-Response-Time") == "" {
		t.Fatalf("应带 X-Response-Time: %d %v", w.Code, w.Header())
	}
	// panic 被恢复为 500，中间件链不中断
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应恢复为 500: %d", w.Code)
	}
	// 请求 ID 存在且稳定
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if w.Header().Get("X-Request-Id") == "" {
		t.Fatal("应生成 X-Request-Id")
	}
}
