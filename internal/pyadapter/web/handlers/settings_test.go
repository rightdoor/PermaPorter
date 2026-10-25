package handlers

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/middleware"
	pydatabase "permaporter/internal/pyadapter/database"
	pyproxy "permaporter/internal/pyadapter/proxy"
)

// newTestApp 搭建 py 生态管理端 App。
func newTestApp(t *testing.T) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=10000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := pydatabase.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	if err := db.InitSettings("http://127.0.0.1:1/simple/", filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	return &App{
		Cfg:      &config.Config{},
		DB:       db,
		Engine:   pyproxy.NewEngine(db, pyproxy.NewStats(db)),
		Sessions: middleware.NewSessionStore(),
		Limiter:  middleware.NewLoginLimiter(),
		Cache:    NewViewCache(db.WriteGen, db.StatsGen, 0),
	}
}

// TestParsePyRequirementNames requirements.txt 解析：版本说明符/extras/注释/环境标记。
func TestParsePyRequirementNames(t *testing.T) {
	raw := `# 生产依赖
requests==2.31.0
flask>=3.0,<4.0
numpy[extra]>=1.26 ; python_version >= "3.9"
--index-url https://example.com/simple
Demo.Pkg
!!bad
`
	names, invalid := parsePyRequirementNames(raw)
	want := []string{"demo-pkg", "flask", "numpy", "requests"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("解析结果 = %v, 期望 %v", names, want)
	}
	if len(invalid) != 1 || invalid[0] != "!!bad" {
		t.Fatalf("无效行应上报: %v", invalid)
	}
}

// TestSettingsSaveUpstream 设置读取与上游保存。
func TestSettingsSaveUpstream(t *testing.T) {
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
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"upstreamUrl":"https://pypi.org/simple/"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	app.SaveUpstream(c)
	if w.Code != http.StatusOK {
		t.Fatalf("合法上游应 200: %d %s", w.Code, w.Body.String())
	}
	settings, err := app.DB.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.UpstreamURL != "https://pypi.org/simple/" {
		t.Fatalf("配置未生效: %+v", settings)
	}
}

// TestPyHealthz 健康检查 JSON 形态。
func TestPyHealthz(t *testing.T) {
	app := newTestApp(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	app.Healthz(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("healthz 异常: %d %s", w.Code, w.Body.String())
	}
}

// TestParsePyRequirementNamesMarkerOnlyLines 整行只有环境标记（; python_version >= "3"）
// 或注释截断后为空的行必须跳过，不得越界 panic（回归：预热提交接口可被打崩）。
func TestParsePyRequirementNamesMarkerOnlyLines(t *testing.T) {
	names, invalid := parsePyRequirementNames("requests\n; python_version >= \"3.8\"\nflask==3.0.0\n# comment\n")
	if len(invalid) != 0 {
		t.Fatalf("不应产生无效项: %v", invalid)
	}
	want := []string{"flask", "requests"}
	if len(names) != len(want) {
		t.Fatalf("解析结果 = %v，期望 %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("解析结果 = %v，期望 %v", names, want)
		}
	}
}
