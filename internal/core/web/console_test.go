package web

import (
	"encoding/json"
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
)

// fakeEco 生态句柄替身：以固定生态标记响应同时实现 GoEcosystem、NpmEcosystem、
// PyEcosystem 与 MvnEcosystem 的全部 gin handler（并集方法集，同一类型可同时充任四生态）。
// 聚合层经 capture() 进程内直调这些 handler，这里验证合并语义而非生态内部逻辑。
type fakeEco struct{ eco string }

func (f *fakeEco) respond(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "eco": f.eco})
}

// go 生态方法集
func (f *fakeEco) ModulesAPI(c *gin.Context)           { f.respond(c) }
func (f *fakeEco) ModuleDetailAPI(c *gin.Context)      { f.respond(c) }
func (f *fakeEco) AdminModulesAPI(c *gin.Context)      { f.respond(c) }
func (f *fakeEco) ModuleVersionsAPI(c *gin.Context)    { f.respond(c) }
func (f *fakeEco) RefreshModuleMetaAPI(c *gin.Context) { f.respond(c) }

// npm 生态方法集
func (f *fakeEco) PackagesAPI(c *gin.Context)        { f.respond(c) }
func (f *fakeEco) PackageDetailAPI(c *gin.Context)   { f.respond(c) }
func (f *fakeEco) AdminPackagesAPI(c *gin.Context)   { f.respond(c) }
func (f *fakeEco) PackageVersionsAPI(c *gin.Context) { f.respond(c) }
func (f *fakeEco) RefreshMetaAPI(c *gin.Context)     { f.respond(c) }

// py 生态方法集
func (f *fakeEco) PyProjectsAPI(c *gin.Context)       { f.respond(c) }
func (f *fakeEco) PyProjectDetailAPI(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) AdminPyProjectsAPI(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) PyProjectFilesAPI(c *gin.Context)   { f.respond(c) }
func (f *fakeEco) DeletePyFileAPI(c *gin.Context)     { f.respond(c) }
func (f *fakeEco) RefreshPyProjectAPI(c *gin.Context) { f.respond(c) }

// mvn 生态方法集
func (f *fakeEco) MvnArtifactsAPI(c *gin.Context)       { f.respond(c) }
func (f *fakeEco) MvnArtifactDetailAPI(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) AdminMvnArtifactsAPI(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) MvnArtifactFilesAPI(c *gin.Context)   { f.respond(c) }
func (f *fakeEco) DeleteMvnFileAPI(c *gin.Context)      { f.respond(c) }
func (f *fakeEco) RefreshMvnMetaAPI(c *gin.Context)     { f.respond(c) }

// 共享方法集
func (f *fakeEco) DeleteVersionAPI(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) PreheatAPI(c *gin.Context)        { f.respond(c) }
func (f *fakeEco) PreheatStartAPI(c *gin.Context)   { f.respond(c) }
func (f *fakeEco) LogsAPI(c *gin.Context)           { f.respond(c) }
func (f *fakeEco) SettingsAPI(c *gin.Context)       { f.respond(c) }
func (f *fakeEco) SaveUpstream(c *gin.Context)      { f.respond(c) }
func (f *fakeEco) SaveProxy(c *gin.Context)         { f.respond(c) }
func (f *fakeEco) TestProxy(c *gin.Context)         { f.respond(c) }
func (f *fakeEco) StartCleanupJSON(c *gin.Context)  { f.respond(c) }
func (f *fakeEco) CleanupStatusJSON(c *gin.Context) { f.respond(c) }
func (f *fakeEco) DashboardAPI(c *gin.Context)      { f.respond(c) }
func (f *fakeEco) Summary(c *gin.Context)           { f.respond(c) }
func (f *fakeEco) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "db": "ok", "eco": f.eco})
}

// Metrics 输出各生态自有前缀的指标行（go_permaporter_* / npm_permaporter_* / py_permaporter_*）。
func (f *fakeEco) Metrics(c *gin.Context) {
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(f.eco+"_permaporter_metric 1\n"))
}

func (f *fakeEco) EnsureInitSettings() error { return nil }

// newTestConsole 组装聚合层：替身生态句柄 + 内存会话 + 临时单库
// （SPA/State 路径需要读 platform_settings 的 initialized 状态）。
func newTestConsole(t *testing.T) *Console {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store, err := coredatabase.Open("file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "permaporter.db")))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Console{
		Cfg:      &config.Config{UIPort: 9090, NPMPort: 9091, GoPort: 9092, PyPort: 9093, MVNPort: 9094},
		Store:    store,
		Sessions: middleware.NewSessionStore(),
		Limiter:  middleware.NewLoginLimiter(),
		Go:       &fakeEco{eco: "go"},
		Npm:      &fakeEco{eco: "npm"},
		Py:       &fakeEco{eco: "py"},
		Mvn:      &fakeEco{eco: "mvn"},
		Started:  time.Now(),
	}
}

// call 直接调用聚合 handler 并返回 recorder。
func call(h func(*gin.Context), target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h(c)
	return w
}

// TestSummaryAggregated 合并四生态 summary：{"ok":true,"go":…,"npm":…,"py":…,"mvn":…,"authed":…}。
func TestSummaryAggregated(t *testing.T) {
	con := newTestConsole(t)
	w := call(con.SummaryAggregated, "/api/summary")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	var body struct {
		Ok bool `json:"ok"`
		Go struct {
			Eco string `json:"eco"`
		} `json:"go"`
		Npm struct {
			Eco string `json:"eco"`
		} `json:"npm"`
		Py struct {
			Eco string `json:"eco"`
		} `json:"py"`
		Mvn struct {
			Eco string `json:"eco"`
		} `json:"mvn"`
		Authed bool `json:"authed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应应为 JSON: %v %s", err, w.Body.String())
	}
	if !body.Ok || body.Go.Eco != "go" || body.Npm.Eco != "npm" || body.Py.Eco != "py" ||
		body.Mvn.Eco != "mvn" || body.Authed {
		t.Fatalf("合并结果不符: %s", w.Body.String())
	}
}

// TestDashboardAggregated 四卡片数据源各为一段，互不串数据。
func TestDashboardAggregated(t *testing.T) {
	con := newTestConsole(t)
	w := call(con.DashboardAggregated, "/api/admin/dashboard")
	var body struct {
		Ok bool `json:"ok"`
		Go struct {
			Eco string `json:"eco"`
		} `json:"go"`
		Npm struct {
			Eco string `json:"eco"`
		} `json:"npm"`
		Py struct {
			Eco string `json:"eco"`
		} `json:"py"`
		Mvn struct {
			Eco string `json:"eco"`
		} `json:"mvn"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ok || body.Go.Eco != "go" || body.Npm.Eco != "npm" || body.Py.Eco != "py" || body.Mvn.Eco != "mvn" {
		t.Fatalf("dashboard 合并结果不符: %s", w.Body.String())
	}
}

// TestHealthzAggregated 探活摘要：ui 恒 ok，各生态段带各自 db 状态。
func TestHealthzAggregated(t *testing.T) {
	con := newTestConsole(t)
	w := call(con.HealthzAggregated, "/healthz")
	var body struct {
		Ok      bool   `json:"ok"`
		Ui      string `json:"ui"`
		Version string `json:"version"`
		Go      struct {
			Ok  bool   `json:"ok"`
			Db  string `json:"db"`
			Eco string `json:"eco"`
		} `json:"go"`
		Npm struct {
			Eco string `json:"eco"`
		} `json:"npm"`
		Py struct {
			Eco string `json:"eco"`
		} `json:"py"`
		Mvn struct {
			Eco string `json:"eco"`
		} `json:"mvn"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ok || body.Ui != "ok" || !body.Go.Ok || body.Go.Db != "ok" ||
		body.Go.Eco != "go" || body.Npm.Eco != "npm" || body.Py.Eco != "py" || body.Mvn.Eco != "mvn" {
		t.Fatalf("healthz 合并结果不符: %s", w.Body.String())
	}
}

// TestMetricsAggregated 四族指标直接拼接，text/plain 输出。
func TestMetricsAggregated(t *testing.T) {
	con := newTestConsole(t)
	w := call(con.MetricsAggregated, "/metrics")
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("metrics Content-Type 应为 text/plain: %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "go_permaporter_metric 1") || !strings.Contains(body, "npm_permaporter_metric 1") ||
		!strings.Contains(body, "py_permaporter_metric 1") || !strings.Contains(body, "mvn_permaporter_metric 1") {
		t.Fatalf("metrics 应拼接四族指标: %q", body)
	}
}

// TestDispatchEco 按参数分派：默认 go，eco=npm/py/mvn 分别走对应生态。
func TestDispatchEco(t *testing.T) {
	con := newTestConsole(t)
	goBody, _ := capture(con.Go.Summary, http.MethodGet, "/")
	npmBody, _ := capture(con.Npm.Summary, http.MethodGet, "/")
	if !strings.Contains(string(goBody), `"go"`) || !strings.Contains(string(npmBody), `"npm"`) {
		t.Fatalf("capture 应分别命中各生态: %q %q", goBody, npmBody)
	}
	// dispatchEco 与 /api/admin 下按生态分派的接口语义一致（preheat/logs/cleanup 等）
	h := dispatchEco(con.Go.ModulesAPI, con.Npm.PackagesAPI, con.Py.PyProjectsAPI, con.Mvn.MvnArtifactsAPI)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/preheat", nil)
	h(c)
	if !strings.Contains(w.Body.String(), `"go"`) {
		t.Fatalf("默认应分派 go: %s", w.Body.String())
	}
	for _, eco := range []string{"npm", "py", "mvn"} {
		w = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/preheat?eco="+eco, nil)
		h(c)
		if !strings.Contains(w.Body.String(), `"`+eco+`"`) {
			t.Fatalf("eco=%s 应分派 %s: %s", eco, eco, w.Body.String())
		}
	}
}

// TestNotFoundAPIJSON 9090 非 API 形态 404 语义：JSON 请求回落 {"error":...}，
// 浏览器形态回落 SPA 外壳（前端未构建时输出降级外壳）。
func TestNotFoundAPIJSON(t *testing.T) {
	con := newTestConsole(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/unknown", nil)
	c.Request.Header.Set("Accept", "application/json")
	con.NotFound(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("API 形态应 404: %d", w.Code)
	}
	var body gin.H
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != "not found" {
		t.Fatalf("API 404 应为 JSON error: %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/unknown", nil)
	c.Request.Header.Set("Accept", "text/html")
	con.NotFound(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatalf("浏览器形态应回落 SPA 外壳: %d", w.Code)
	}
}
