package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
)

// newSetupConsole 组装未初始化模式控制台：临时配置文件（模板态）+ 可注入的 Boot。
func newSetupConsole(t *testing.T, mutate func(*config.Config, *[]SetupRequest)) (*gin.Engine, *SetupConsole, *[]SetupRequest) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "permaporter.toml")
	ports := config.PortsConfig{UI: 9090, NPM: 9091, Go: 9092, Py: 9093, MVN: 9094}
	if err := os.WriteFile(path, []byte(config.TemplateContent(ports)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UIPort: 9090, NPMPort: 9091, GoPort: 9092, PyPort: 9093, MVNPort: 9094,
		DBDSN:        "file:./data/permaporter.db",
		GoStorageDir: "./data/storage-go", NPMStorageDir: "./data/storage-npm",
		PyStorageDir: "./data/storage-py", MVNStorageDir: "./data/storage-mvn",
		GoUpstream: "https://proxy.golang.org", NPMUpstream: "https://registry.npmjs.org",
		PyUpstream: "https://pypi.org/simple/", MVNUpstream: "https://repo.maven.apache.org/maven2",
		ConfigPath: path,
	}
	var got []SetupRequest
	if mutate != nil {
		mutate(cfg, &got)
	}
	sc := &SetupConsole{Cfg: cfg, InitToken: cfg.InitToken, Boot: func(req SetupRequest) error {
		got = append(got, req)
		return nil
	}}
	r := gin.New()
	RegisterSetupRoutes(r, sc)
	return r, sc, &got
}

// postSetupJSON 发送 JSON POST。
func postSetupJSON(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestSetupStatus status 端点：未配置状态、配置文件路径、预填端口与上游。
func TestSetupStatus(t *testing.T) {
	r, _, _ := newSetupConsole(t, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status 应 200: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["configured"] != false || body["initTokenRequired"] != false {
		t.Fatalf("未配置状态错误: %v", body)
	}
	if !strings.Contains(w.Body.String(), `"ports"`) || !strings.Contains(w.Body.String(), `"upstreams"`) {
		t.Fatalf("应含预填端口与上游: %s", w.Body.String())
	}
	if p, _ := body["configFile"].(string); p == "" {
		t.Fatal("应返回配置文件路径")
	}
}

// TestSetupStateAndSPA state 标记 setupMode，SPA 外壳注入 setupMode 供路由守卫导向向导。
func TestSetupStateAndSPA(t *testing.T) {
	r, _, _ := newSetupConsole(t, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"setupMode":true`) {
		t.Fatalf("state 应标记 setupMode: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"setupMode":true`) {
		t.Fatalf("SPA 外壳应注入 setupMode: %d", w.Code)
	}
	// API 形态 404
	req := httptest.NewRequest(http.MethodGet, "/api/whatever", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("未知 API 应 JSON 404: %d %s", w.Code, w.Body.String())
	}
	// 管理端 API 在未初始化模式不存在（数据库尚未连接）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/dashboard", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("未初始化模式不应有管理 API: %d", w.Code)
	}
}

// TestSetupTestDatabase test-database：sqlite 连通成功、非法 driver 拒绝。
func TestSetupTestDatabase(t *testing.T) {
	r, _, _ := newSetupConsole(t, nil)
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "probe.db"))
	w := postSetupJSON(r, "/api/setup/test-database", `{"driver":"sqlite","dsn":"`+dsn+`"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "连接成功") {
		t.Fatalf("sqlite 探测应成功: %d %s", w.Code, w.Body.String())
	}
	w = postSetupJSON(r, "/api/setup/test-database", `{"driver":"mysql"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 driver 应 400: %d", w.Code)
	}
	w = postSetupJSON(r, "/api/setup/test-database", `{"driver":"postgresql","host":"h"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "必填字段") {
		t.Fatalf("PG 字段缺失应 400: %d %s", w.Code, w.Body.String())
	}
}

// TestSetupInitializeSuccess 完整 initialize：校验 → 写回配置文件 → Boot 收到正确参数。
func TestSetupInitializeSuccess(t *testing.T) {
	r, sc, got := newSetupConsole(t, nil)
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "init.db"))
	body := `{
		"driver":"sqlite","dsn":"` + dsn + `",
		"ports":{"ui":9090,"npm":9091,"go":9092,"py":9093,"mvn":9094},
		"upstreams":{"go":"https://goproxy.cn","goFallback":"","npm":"https://registry.npmmirror.com","py":"https://pypi.tuna.tsinghua.edu.cn/simple/","mvn":"https://maven.aliyun.com/repository/public"},
		"adminPassword":"secret-1","confirm":"secret-1"
	}`
	w := postSetupJSON(r, "/api/setup/initialize", body)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize 应成功: %d %s", w.Code, w.Body.String())
	}
	if len(*got) != 1 {
		t.Fatalf("Boot 应被调用一次: %d", len(*got))
	}
	req := (*got)[0]
	if req.Driver != config.DriverSQLite || req.DSN != dsn || req.Ports.UI != 9090 || req.Upstreams.Go != "https://goproxy.cn" {
		t.Fatalf("Boot 参数错误: %+v", req)
	}
	if req.AdminHash == "" || req.AdminHash == "secret-1" {
		t.Fatalf("密码应已哈希: %q", req.AdminHash)
	}
	// 配置文件已写回：[database] 填充、端口值更新、可重新解析
	fc, err := config.LoadConfigFile(sc.Cfg.ConfigPath)
	if err != nil {
		t.Fatalf("写回的配置文件应可解析: %v", err)
	}
	if fc.Database.NormalizedDriver() != config.DriverSQLite || fc.Database.DSN != dsn {
		t.Fatalf("数据库段填充错误: %+v", fc.Database)
	}
	if fc.Ports.UI != 9090 || fc.Ports.MVN != 9094 {
		t.Fatalf("端口写回错误: %+v", fc.Ports)
	}
	data, _ := os.ReadFile(sc.Cfg.ConfigPath)
	if !strings.Contains(string(data), "ui = 9090") {
		t.Fatalf("端口行与注释应保留:\n%s", data)
	}
	// 完成后向导端点 409
	w = postSetupJSON(r, "/api/setup/initialize", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("重复 initialize 应 409: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("完成后 status 应 409: %d", w.Code)
	}
}

// TestSetupInitializeValidation 各校验分支：端口冲突、密码、上游格式、INIT_TOKEN。
func TestSetupInitializeValidation(t *testing.T) {
	r, _, got := newSetupConsole(t, nil)
	okDB := `"driver":"sqlite","dsn":"file:` + filepath.ToSlash(filepath.Join(t.TempDir(), "v.db")) + `"`
	base := func(db, ports, ups, pwd string) string {
		return `{"` + db + `","ports":` + ports + `,"upstreams":` + ups + `,"adminPassword":"` + pwd + `","confirm":"` + pwd + `"}`
	}
	goodPorts := `{"ui":9090,"npm":9091,"go":9092,"py":9093,"mvn":9094}`
	goodUps := `{"go":"https://proxy.golang.org","goFallback":"","npm":"https://registry.npmjs.org","py":"https://pypi.org/simple/","mvn":"https://repo.maven.apache.org/maven2"}`

	cases := []struct {
		name string
		body string
		want int
	}{
		{"端口冲突", base(okDB, `{"ui":9090,"npm":9090,"go":9092,"py":9093,"mvn":9094}`, goodUps, "secret-1"), 400},
		{"端口越界", base(okDB, `{"ui":70000,"npm":9091,"go":9092,"py":9093,"mvn":9094}`, goodUps, "secret-1"), 400},
		{"密码过短", base(okDB, goodPorts, goodUps, "abc"), 400},
		{"上游协议非法", base(okDB, goodPorts, `{"go":"ftp://x","goFallback":"","npm":"https://registry.npmjs.org","py":"https://pypi.org/simple/","mvn":"https://repo.maven.apache.org/maven2"}`, "secret-1"), 400},
		{"上游缺失", base(okDB, goodPorts, `{"go":"","goFallback":"","npm":"https://registry.npmjs.org","py":"https://pypi.org/simple/","mvn":"https://repo.maven.apache.org/maven2"}`, "secret-1"), 400},
		{"非法 driver", base(`"driver":"oracle"`, goodPorts, goodUps, "secret-1"), 400},
	}
	for _, c := range cases {
		if w := postSetupJSON(r, "/api/setup/initialize", c.body); w.Code != c.want {
			t.Errorf("%s: 应 %d 实际 %d: %s", c.name, c.want, w.Code, w.Body.String())
		}
	}
	if len(*got) != 0 {
		t.Fatalf("校验失败不应调用 Boot: %d", len(*got))
	}
}

// TestSetupInitializeToken INIT_TOKEN 设置后未带/错带令牌均拒绝。
func TestSetupInitializeToken(t *testing.T) {
	r, _, got := newSetupConsole(t, func(cfg *config.Config, _ *[]SetupRequest) {
		cfg.InitToken = "tok-123"
	})
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "tok.db"))
	body := `{"driver":"sqlite","dsn":"` + dsn + `","ports":{"ui":9090,"npm":9091,"go":9092,"py":9093,"mvn":9094},"upstreams":{"go":"https://proxy.golang.org","goFallback":"","npm":"https://registry.npmjs.org","py":"https://pypi.org/simple/","mvn":"https://repo.maven.apache.org/maven2"},"adminPassword":"secret-1","confirm":"secret-1"}`
	if w := postSetupJSON(r, "/api/setup/initialize", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌应 401: %d %s", w.Code, w.Body.String())
	}
	if w := postSetupJSON(r, "/api/setup/test-database", `{"driver":"sqlite","token":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("test-database 缺令牌应 401: %d", w.Code)
	}
	// status 应提示需要令牌
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if !strings.Contains(w.Body.String(), `"initTokenRequired":true`) {
		t.Fatalf("status 应提示令牌: %s", w.Body.String())
	}
	if w := postSetupJSON(r, "/api/setup/initialize", strings.Replace(body, `"driver":`, `"token":"tok-123","driver":`, 1)); w.Code != http.StatusOK {
		t.Fatalf("正确令牌应通过: %d %s", w.Code, w.Body.String())
	}
	if len(*got) != 1 {
		t.Fatalf("Boot 应恰好调用一次: %d", len(*got))
	}
}

// TestSetupInitializeBootFails Boot 失败：500 且配置文件已写回（下次重启按明确日志拒绝启动）。
func TestSetupInitializeBootFails(t *testing.T) {
	r, sc, _ := newSetupConsole(t, nil)
	path := sc.Cfg.ConfigPath
	// 把 Boot 替换为恒失败（复用同一 cfg 与已写好的模板文件）
	sc.Boot = func(req SetupRequest) error { return errBoom }
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "fail.db"))
	body := `{"driver":"sqlite","dsn":"` + dsn + `","ports":{"ui":9090,"npm":9091,"go":9092,"py":9093,"mvn":9094},"upstreams":{"go":"https://proxy.golang.org","goFallback":"","npm":"https://registry.npmjs.org","py":"https://pypi.org/simple/","mvn":"https://repo.maven.apache.org/maven2"},"adminPassword":"secret-1","confirm":"secret-1"}`
	w := postSetupJSON(r, "/api/setup/initialize", body)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "初始化失败") {
		t.Fatalf("Boot 失败应 500: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置文件应已写回: %v", err)
	}
	// 失败后未置位 done：可修正后重试
	if sc.done.Load() {
		t.Fatal("Boot 失败不应标记完成")
	}
}

var errBoom = &staticError{"boom"}

type staticError struct{ msg string }

func (e *staticError) Error() string { return e.msg }

// TestConsoleSystemInfo 完整控制台的"数据库与端口"只读卡片数据源。
func TestConsoleSystemInfo(t *testing.T) {
	con := newTestConsole(t)
	con.Cfg.ConfigPath = "/etc/permaporter/permaporter.toml"
	w := call(con.SystemInfoAPI, "/api/admin/system-info")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		OK     bool   `json:"ok"`
		Driver string `json:"driver"`
		Config string `json:"configFile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Driver != "sqlite" || body.Config == "" {
		t.Fatalf("system-info 内容错误: %s", w.Body.String())
	}
}
