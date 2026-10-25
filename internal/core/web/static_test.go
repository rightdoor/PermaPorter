package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestIsHashedAsset 匹配 Vite 实际产物命名（连字符 + 8 位 base62，可含 _ 与非十六进制字符）。
func TestIsHashedAsset(t *testing.T) {
	hashed := []string{
		"assets/main-CToQXxdf.css",
		"assets/vendor-BQVu_NzZ.js",
		"assets/main-NgoONgJT.js",
		"assets/PreheatView-DbBGmV4D.js",
		"assets/ModuleDetailView-BzuWe-JG.js",
		"assets/chunk-0123abcd.mjs",
	}
	plain := []string{
		"assets/logo.png", "assets/main.css", "assets/main.js", "assets/sw.js",
		"assets/main-CToQXdf.css", // hash 段仅 7 位
		"assets/main-CToQXddfa.css",
	}
	for _, n := range hashed {
		if !isHashedAsset(n) {
			t.Errorf("应识别为 hash 产物: %s", n)
		}
	}
	for _, n := range plain {
		if isHashedAsset(n) {
			t.Errorf("不应识别为 hash 产物: %s", n)
		}
	}
}

// TestStaticHandlerCacheControl hash 产物一年期 immutable；非 hash 文件 must-revalidate；
// ETag/304 行为不变。
func TestStaticHandlerCacheControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	files := map[string]string{
		"assets/main-CToQXxdf.css": `"css-etag"`,
		"assets/vendor-BQVu_NzZ.js": `"js-etag"`,
		"logo.png":                  `"logo-etag"`,
	}
	for name, etag := range files {
		staticFiles[name] = []byte("content of " + name)
		staticETags[name] = etag
		defer func() {
			delete(staticFiles, name)
			delete(staticETags, name)
		}()
	}

	r := gin.New()
	r.GET("/static/*_", staticHandler)

	cases := []struct {
		path string
		want string
	}{
		{"/static/assets/main-CToQXxdf.css", "public, max-age=31536000, immutable"},
		{"/static/assets/vendor-BQVu_NzZ.js", "public, max-age=31536000, immutable"},
		{"/static/logo.png", "public, max-age=0, must-revalidate"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, c.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d", c.path, w.Code)
		}
		if got := w.Header().Get("Cache-Control"); got != c.want {
			t.Fatalf("%s Cache-Control = %q, 期望 %q", c.path, got, c.want)
		}
	}

	// ETag 条件请求：命中返回 304
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/assets/main-CToQXxdf.css", nil)
	req.Header.Set("If-None-Match", `"css-etag"`)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("ETag 命中应 304，实际 %d", w.Code)
	}
}
