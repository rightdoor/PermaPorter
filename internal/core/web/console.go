// 控制台聚合层（9090）：SPA 外壳、/static、/api 生态路由、/admin、
// 聚合 /healthz 与 /metrics。/api 为进程内直调（直接持有各套 adapter 句柄），
// 严禁对 9091/9092/9093/9094 发起 HTTP 反代。会话与登录只存在于本端口。
//
// core 不 import 任何 adapter：各生态句柄经下方接口注入（cmd 装配时传入）。
package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/middleware"
	"permaporter/internal/core/utils"
	"permaporter/internal/core/webui"
	"permaporter/internal/core/webutil"
)

// GoEcosystem go 生态适配器暴露给控制台的句柄面（*goadapter 的 App 隐式实现）。
type GoEcosystem interface {
	ModulesAPI(c *gin.Context)
	ModuleDetailAPI(c *gin.Context)
	AdminModulesAPI(c *gin.Context)
	ModuleVersionsAPI(c *gin.Context)
	DeleteVersionAPI(c *gin.Context)
	RefreshModuleMetaAPI(c *gin.Context)
	PreheatAPI(c *gin.Context)
	PreheatStartAPI(c *gin.Context)
	LogsAPI(c *gin.Context)
	SettingsAPI(c *gin.Context)
	SaveUpstream(c *gin.Context)
	SaveProxy(c *gin.Context)
	TestProxy(c *gin.Context)
	StartCleanupJSON(c *gin.Context)
	CleanupStatusJSON(c *gin.Context)
	DashboardAPI(c *gin.Context)
	Summary(c *gin.Context)
	Healthz(c *gin.Context)
	Metrics(c *gin.Context)
	EnsureInitSettings() error
}

// NpmEcosystem npm 生态适配器暴露给控制台的句柄面（*npmadapter 的 App 隐式实现）。
type NpmEcosystem interface {
	PackagesAPI(c *gin.Context)
	PackageDetailAPI(c *gin.Context)
	AdminPackagesAPI(c *gin.Context)
	PackageVersionsAPI(c *gin.Context)
	DeleteVersionAPI(c *gin.Context)
	RefreshMetaAPI(c *gin.Context)
	PreheatAPI(c *gin.Context)
	PreheatStartAPI(c *gin.Context)
	LogsAPI(c *gin.Context)
	SettingsAPI(c *gin.Context)
	SaveUpstream(c *gin.Context)
	SaveProxy(c *gin.Context)
	TestProxy(c *gin.Context)
	StartCleanupJSON(c *gin.Context)
	CleanupStatusJSON(c *gin.Context)
	DashboardAPI(c *gin.Context)
	Summary(c *gin.Context)
	Healthz(c *gin.Context)
	Metrics(c *gin.Context)
	EnsureInitSettings() error
}

// PyEcosystem py 生态适配器暴露给控制台的句柄面（*pyadapter 的 App 隐式实现）。
type PyEcosystem interface {
	PyProjectsAPI(c *gin.Context)
	PyProjectDetailAPI(c *gin.Context)
	AdminPyProjectsAPI(c *gin.Context)
	PyProjectFilesAPI(c *gin.Context)
	DeletePyFileAPI(c *gin.Context)
	RefreshPyProjectAPI(c *gin.Context)
	PreheatAPI(c *gin.Context)
	PreheatStartAPI(c *gin.Context)
	LogsAPI(c *gin.Context)
	SettingsAPI(c *gin.Context)
	SaveUpstream(c *gin.Context)
	SaveProxy(c *gin.Context)
	TestProxy(c *gin.Context)
	StartCleanupJSON(c *gin.Context)
	CleanupStatusJSON(c *gin.Context)
	DashboardAPI(c *gin.Context)
	Summary(c *gin.Context)
	Healthz(c *gin.Context)
	Metrics(c *gin.Context)
	EnsureInitSettings() error
}

// MvnEcosystem mvn 生态适配器暴露给控制台的句柄面（*mvnadapter 的 App 隐式实现）。
type MvnEcosystem interface {
	MvnArtifactsAPI(c *gin.Context)
	MvnArtifactDetailAPI(c *gin.Context)
	AdminMvnArtifactsAPI(c *gin.Context)
	MvnArtifactFilesAPI(c *gin.Context)
	DeleteMvnFileAPI(c *gin.Context)
	RefreshMvnMetaAPI(c *gin.Context)
	PreheatAPI(c *gin.Context)
	PreheatStartAPI(c *gin.Context)
	LogsAPI(c *gin.Context)
	SettingsAPI(c *gin.Context)
	SaveUpstream(c *gin.Context)
	SaveProxy(c *gin.Context)
	TestProxy(c *gin.Context)
	StartCleanupJSON(c *gin.Context)
	CleanupStatusJSON(c *gin.Context)
	DashboardAPI(c *gin.Context)
	Summary(c *gin.Context)
	Healthz(c *gin.Context)
	Metrics(c *gin.Context)
	EnsureInitSettings() error
}

// Console 控制台聚合依赖。单库句柄与四生态句柄由 cmd 注入，core 不感知具体类型。
type Console struct {
	Cfg      *config.Config
	Store    *coredatabase.Store
	Sessions *middleware.SessionStore
	Limiter  *middleware.LoginLimiter
	Go       GoEcosystem
	Npm      NpmEcosystem
	Py       PyEcosystem
	Mvn      MvnEcosystem
	Started  time.Time
}

// RegisterConsoleRoutes 在 9090 引擎上注册全部控制台路由。
func RegisterConsoleRoutes(r *gin.Engine, con *Console) {
	// ---------- 静态资源与 SPA 外壳（仅 9090） ----------
	initStaticAssets(webui.FS)
	r.GET("/static/*filepath", staticHandler)

	// ---------- 公开 JSON API（/api 按生态路由） ----------
	r.GET("/api/state", con.State)
	r.GET("/api/summary", con.SummaryAggregated)
	r.GET("/api/modules", con.Go.ModulesAPI) // go 库
	r.GET("/api/module/*rest", con.Go.ModuleDetailAPI)
	r.GET("/api/packages", con.Npm.PackagesAPI) // npm 库
	r.GET("/api/package/*rest", con.Npm.PackageDetailAPI)
	r.GET("/api/pyprojects", con.Py.PyProjectsAPI) // py 库
	r.GET("/api/pyproject/*rest", con.Py.PyProjectDetailAPI)
	r.GET("/api/mvnartifacts", con.Mvn.MvnArtifactsAPI) // mvn 库
	r.GET("/api/mvnartifact/*rest", con.Mvn.MvnArtifactDetailAPI)

	// ---------- 认证 API（仅 9090，凭据在单库 platform_settings） ----------
	r.POST("/api/auth/setup", con.SetupAPI)
	r.POST("/api/auth/login", con.LoginAPI)
	r.POST("/api/auth/logout", con.LogoutAPI)

	// ---------- 管理 API（需登录；生态接口经参数分派） ----------
	auth := r.Group("/api/admin", con.RequireAuth)
	{
		auth.GET("/dashboard", con.DashboardAggregated) // 各生态各一段
		// go 生态
		auth.GET("/modules", con.Go.AdminModulesAPI)
		auth.GET("/module-versions", con.Go.ModuleVersionsAPI)
		auth.DELETE("/module-version", con.Go.DeleteVersionAPI)
		auth.POST("/module-refresh", con.Go.RefreshModuleMetaAPI)
		// npm 生态
		auth.GET("/packages", con.Npm.AdminPackagesAPI)
		auth.GET("/package-versions", con.Npm.PackageVersionsAPI)
		auth.DELETE("/package-version", con.Npm.DeleteVersionAPI)
		auth.POST("/package-refresh", con.Npm.RefreshMetaAPI)
		// py 生态
		auth.GET("/pyprojects", con.Py.AdminPyProjectsAPI)
		auth.GET("/pyproject-files", con.Py.PyProjectFilesAPI)
		auth.DELETE("/py-file", con.Py.DeletePyFileAPI)
		auth.POST("/py-refresh", con.Py.RefreshPyProjectAPI)
		// mvn 生态
		auth.GET("/mvnartifacts", con.Mvn.AdminMvnArtifactsAPI)
		auth.GET("/mvnartifact-files", con.Mvn.MvnArtifactFilesAPI)
		auth.DELETE("/mvn-file", con.Mvn.DeleteMvnFileAPI)
		auth.POST("/mvn-refresh", con.Mvn.RefreshMvnMetaAPI)
		// 按生态分派（各生态日志各自独立 Ring，行内带生态标记）
		auth.GET("/preheat", dispatchEco(con.Go.PreheatAPI, con.Npm.PreheatAPI, con.Py.PreheatAPI, con.Mvn.PreheatAPI))
		auth.POST("/preheat", dispatchEco(con.Go.PreheatStartAPI, con.Npm.PreheatStartAPI, con.Py.PreheatStartAPI, con.Mvn.PreheatStartAPI))
		auth.GET("/logs", dispatchEco(con.Go.LogsAPI, con.Npm.LogsAPI, con.Py.LogsAPI, con.Mvn.LogsAPI))
		auth.GET("/settings", con.SettingsAggregated) // 各生态各一段
		auth.POST("/settings/upstream", dispatchEco(con.Go.SaveUpstream, con.Npm.SaveUpstream, con.Py.SaveUpstream, con.Mvn.SaveUpstream))
		auth.POST("/settings/proxy", dispatchEco(con.Go.SaveProxy, con.Npm.SaveProxy, con.Py.SaveProxy, con.Mvn.SaveProxy))
		auth.POST("/settings/test-proxy", dispatchEco(con.Go.TestProxy, con.Npm.TestProxy, con.Py.TestProxy, con.Mvn.TestProxy))
		auth.POST("/settings/password", con.ChangePassword) // 写单库 platform_settings
		auth.POST("/settings/cleanup", dispatchEco(con.Go.StartCleanupJSON, con.Npm.StartCleanupJSON, con.Py.StartCleanupJSON, con.Mvn.StartCleanupJSON))
		auth.GET("/settings/cleanup", dispatchEco(con.Go.CleanupStatusJSON, con.Npm.CleanupStatusJSON, con.Py.CleanupStatusJSON, con.Mvn.CleanupStatusJSON))
		auth.GET("/system-info", con.SystemInfoAPI) // 数据库与端口只读卡片（修改需编辑服务器配置文件并重启）
	}

	// ---------- 聚合健康检查与指标（9090 输出四族并集；HEAD 供负载均衡探活） ----------
	r.GET("/healthz", con.HealthzAggregated)
	r.HEAD("/healthz", con.HealthzAggregated)
	r.GET("/metrics", con.MetricsAggregated)
	r.HEAD("/metrics", con.MetricsAggregated)

	// ---------- SPA 路由（单 SPA 双路由树；quickstart/独立搜索页已下线，其余路径由 NoRoute 回落） ----------
	r.GET("/", con.SPA)
	r.GET("/goproxy", con.SPA)
	r.GET("/npmregistry", con.SPA)
	r.GET("/pythonsimple", con.SPA)
	r.GET("/maven", con.SPA)
	r.GET("/module/*rest", con.SPA)
	r.GET("/package/*rest", con.SPA)
	r.GET("/pyproject/*rest", con.SPA)
	r.GET("/mvnartifact/*rest", con.SPA)
	r.GET("/admin", con.SPA)
	r.GET("/admin/*rest", con.SPA)

	// 其余路径：浏览器回落 SPA 外壳、API 形态 JSON 404
	r.NoRoute(con.NotFound)

	// pprof 调试端点：PPROF=1 开启，仅 9090 且需管理员登录
	if con.pprofEnabled() {
		registerPprof(r, con)
	}
}

// dispatchEco 按 query 参数 eco 分派到对应生态 handler（默认 go）。
func dispatchEco(goH, npmH, pyH, mvnH gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Query("eco") {
		case "npm":
			npmH(c)
		case "py":
			pyH(c)
		case "mvn":
			mvnH(c)
		default:
			goH(c)
		}
	}
}

// ---------- 状态 / SPA / 404 ----------

// State 初始状态：initialized 以单库 platform_settings 为准。
// goProxy / npmRegistry / pyIndex / mavenRepo 为服务端注入的四生态协议端口地址
// （前端不得硬编码端口），按当前请求的 scheme 与主机名还原，端口取自配置
// （默认 9092 / 9091 / 9093 / 9094）。
func (con *Console) State(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": con.Store.IsInitialized(),
		"authed":      con.IsAuthed(c),
		"setupMode":   false,
		"ok":          true,
		"version":     config.Version,
		"goProxy":     externalOrigin(c, con.Cfg, con.Cfg.GoPort),
		"npmRegistry": externalOrigin(c, con.Cfg, con.Cfg.NPMPort) + "/",
		"pyIndex":     externalOrigin(c, con.Cfg, con.Cfg.PyPort) + "/simple/",
		"mavenRepo":   externalOrigin(c, con.Cfg, con.Cfg.MVNPort),
	})
}

// externalOrigin 构造同主机指定端口的外部基地址（scheme://host[:port]）。
// scheme 与 host 的反代头仅在直连 peer 为本机可信代理时采信（见 webutil.ExternalScheme）。
// 端口推导：UI 请求自带的显式外部端口（Host/X-Forwarded-Host 的 host:port 形态，
// 如容器 nginx 转发来的 host:9090）相对 UIPort 的差值即宿主端口映射的统一偏移，
// 五端口统一套用该偏移（单个请求无从得知四个协议口各自的外部映射号；官方
// compose/nginx 部署与直连部署的映射均满足统一偏移）。无显式端口（https 标准
// 口等部署）或偏移为 0（直连）时维持配置端口。
func externalOrigin(c *gin.Context, cfg *config.Config, port int) string {
	scheme := webutil.ExternalScheme(c)
	hostPort := webutil.ForwardedHost(c)
	host := hostPort
	if h, p, err := net.SplitHostPort(hostPort); err == nil {
		host = h
		if ext, err := strconv.Atoi(p); err == nil {
			// 推导结果须仍是合法端口：越界时回退配置端口
			if off := ext - cfg.UIPort; off != 0 && port+off > 0 && port+off <= 65535 {
				port += off
			}
		}
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + host
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// SPA 输出 SPA 外壳页面（路由由前端 vue-router 接管，含 404 页）。
func (con *Console) SPA(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", webui.IndexHTML(webui.State{
		Initialized: con.Store.IsInitialized(),
		Authed:      con.IsAuthed(c),
		SetupMode:   false,
		Version:     config.Version,
		GoProxy:     externalOrigin(c, con.Cfg, con.Cfg.GoPort),
		NPMRegistry: externalOrigin(c, con.Cfg, con.Cfg.NPMPort) + "/",
		PyIndex:     externalOrigin(c, con.Cfg, con.Cfg.PyPort) + "/simple/",
		MavenRepo:   externalOrigin(c, con.Cfg, con.Cfg.MVNPort),
	}))
}

// NotFound 9090 未匹配路径：浏览器 → SPA 外壳；API 形态 → JSON 404。
func (con *Console) NotFound(c *gin.Context) {
	if wantsBrowserHTML(c) {
		con.SPA(c)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
}

// wantsBrowserHTML 判断是否为浏览器页面请求：GET + Accept 声明 text/html。
func wantsBrowserHTML(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet {
		return false
	}
	return strings.Contains(c.GetHeader("Accept"), "text/html")
}

// ---------- 聚合接口 ----------

// capture 进程内直调 handler 并捕获其响应体（严禁 HTTP 反代）。单库下聚合即
// "对同一库的多条带前缀查询（go_* / npm_*）后合并"：各生态 handler 只查自己前缀表，
// 此处按生态组装响应，绝不发起跨生态 JOIN。
func capture(h func(*gin.Context), method, path string) ([]byte, int) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	h(c)
	return w.Body.Bytes(), w.Code
}

// captureJSON 捕获 handler 的 JSON 响应为 gin.H；失败时返回 {"ok":false,"error":...}。
func captureJSON(h func(*gin.Context), method, path string) gin.H {
	body, code := capture(h, method, path)
	var m gin.H
	if err := json.Unmarshal(body, &m); err != nil || code >= 400 {
		return gin.H{"ok": false, "error": "生态句柄响应异常", "status": code}
	}
	return m
}

// SummaryAggregated 四生态各一段：{"ok":true,"go":{...},"npm":{...},"py":{...},"mvn":{...}}。
func (con *Console) SummaryAggregated(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ok":     true,
		"go":     captureJSON(con.Go.Summary, http.MethodGet, "/api/summary"),
		"npm":    captureJSON(con.Npm.Summary, http.MethodGet, "/api/summary"),
		"py":     captureJSON(con.Py.Summary, http.MethodGet, "/api/summary"),
		"mvn":    captureJSON(con.Mvn.Summary, http.MethodGet, "/api/summary"),
		"authed": con.IsAuthed(c),
	})
}

// DashboardAggregated 四卡片数据源：各生态 dashboard 各自一段。
func (con *Console) DashboardAggregated(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ok":  true,
		"go":  captureJSON(con.Go.DashboardAPI, http.MethodGet, "/api/admin/dashboard"),
		"npm": captureJSON(con.Npm.DashboardAPI, http.MethodGet, "/api/admin/dashboard"),
		"py":  captureJSON(con.Py.DashboardAPI, http.MethodGet, "/api/admin/dashboard"),
		"mvn": captureJSON(con.Mvn.DashboardAPI, http.MethodGet, "/api/admin/dashboard"),
	})
}

// SettingsAggregated 四生态设置各一段（按生态隔离展示）。
func (con *Console) SettingsAggregated(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ok":  true,
		"go":  captureJSON(con.Go.SettingsAPI, http.MethodGet, "/api/admin/settings"),
		"npm": captureJSON(con.Npm.SettingsAPI, http.MethodGet, "/api/admin/settings"),
		"py":  captureJSON(con.Py.SettingsAPI, http.MethodGet, "/api/admin/settings"),
		"mvn": captureJSON(con.Mvn.SettingsAPI, http.MethodGet, "/api/admin/settings"),
	})
}

// HealthzAggregated 探活摘要：9090 自身 + 四生态 healthz。
func (con *Console) HealthzAggregated(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"ui":      "ok",
		"uptime":  time.Since(con.Started).Round(time.Second).String(),
		"version": config.Version,
		"go":      captureJSON(con.Go.Healthz, http.MethodGet, "/healthz"),
		"npm":     captureJSON(con.Npm.Healthz, http.MethodGet, "/healthz"),
		"py":      captureJSON(con.Py.Healthz, http.MethodGet, "/healthz"),
		"mvn":     captureJSON(con.Mvn.Healthz, http.MethodGet, "/healthz"),
	})
}

// MetricsAggregated go_permaporter_* / npm_permaporter_* / py_permaporter_* / mvn_permaporter_* 四族并集（直接拼接）。
func (con *Console) MetricsAggregated(c *gin.Context) {
	bodies := [][]byte{
		captureMust(con.Go.Metrics),
		captureMust(con.Npm.Metrics),
		captureMust(con.Py.Metrics),
		captureMust(con.Mvn.Metrics),
	}
	total := 0
	for _, body := range bodies {
		total += len(body) + 1
	}
	buf := make([]byte, 0, total)
	for _, body := range bodies {
		if len(body) == 0 {
			continue
		}
		if len(buf) > 0 && !bytes.HasSuffix(buf, []byte("\n")) {
			buf = append(buf, '\n')
		}
		buf = append(buf, body...)
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", buf)
}

// captureMust 进程内直调 /metrics 并捕获响应体（错误时返回空体）。
func captureMust(h func(*gin.Context)) []byte {
	body, _ := capture(h, http.MethodGet, "/metrics")
	return body
}

// ---------- 认证（仅 9090；凭据在单库 platform_settings） ----------

// IsAuthed 判断当前请求是否已登录。
func (con *Console) IsAuthed(c *gin.Context) bool {
	token, _ := c.Cookie(middleware.CookieName)
	return con.Sessions.Valid(token)
}

// RequireAuth 管理接口鉴权：SPA 场景返回 401 JSON，由前端路由跳转登录页。
func (con *Console) RequireAuth(c *gin.Context) {
	token, _ := c.Cookie(middleware.CookieName)
	if !con.Sessions.Valid(token) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录或登录已过期"})
		c.Abort()
		return
	}
	// Cookie 续期：服务端每次校验都会滑动续期 24h，但 Cookie 的 MaxAge 只在登录时
	// 设置——活跃用户登录满 24h 后仍会被要求重新登录。剩余有效期过半时重发 Cookie，
	// 让客户端与服务端的过期时间保持同步。
	if con.Sessions.Remaining(token) < sessionCookieTTL/2 {
		setSessionCookie(c, token, int(sessionCookieTTL/time.Second))
	}
	c.Next()
}

// SetupAPI 首次初始化：写入 platform_settings 密码，并确保四生态 settings 行存在
// （沿用各生态默认上游、存储目录与 meta_ttl；生态行已存在则原样保留）。
func (con *Console) SetupAPI(c *gin.Context) {
	if con.Store.IsInitialized() {
		c.JSON(http.StatusConflict, gin.H{"error": "已完成初始化，请直接登录"})
		return
	}
	var req struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	password := strings.TrimSpace(req.Password)
	if len(password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码长度至少为 6 位"})
		return
	}
	if len(password) > utils.MaxPasswordBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码长度不能超过 " + strconv.Itoa(utils.MaxPasswordBytes) + " 字节"})
		return
	}
	if password != strings.TrimSpace(req.Confirm) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "两次输入的密码不一致"})
		return
	}
	hash, err := utils.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 条件 upsert 原子写入：并发请求只有一个成功，消除 check-then-write 竞口
	init, err := con.Store.InitPasswordHash(hash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化失败: " + err.Error()})
		return
	}
	if !init {
		c.JSON(http.StatusConflict, gin.H{"error": "已完成初始化，请直接登录"})
		return
	}
	// 各生态 settings 行：缺失则按默认配置补齐（密码统一存 platform_settings，生态行不含密码列）
	if err := con.Go.EnsureInitSettings(); err != nil {
		log.Printf("[core] go 生态配置行初始化失败: %v", err)
	}
	if err := con.Npm.EnsureInitSettings(); err != nil {
		log.Printf("[core] npm 生态配置行初始化失败: %v", err)
	}
	if err := con.Py.EnsureInitSettings(); err != nil {
		log.Printf("[core] py 生态配置行初始化失败: %v", err)
	}
	if err := con.Mvn.EnsureInitSettings(); err != nil {
		log.Printf("[core] mvn 生态配置行初始化失败: %v", err)
	}
	_ = con.Store.Touch()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// LoginAPI 登录验证（5 次失败触发 IP 级 15 分钟封禁）。
func (con *Console) LoginAPI(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	ip := c.ClientIP()
	if !con.Limiter.Allow(ip) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "失败次数过多，该 IP 已被封禁，请 15 分钟后重试"})
		return
	}
	hash, err := con.Store.PasswordHash()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库读取失败"})
		return
	}
	if hash == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "系统尚未完成初始化"})
		return
	}
	if !utils.CheckPassword(hash, req.Password) {
		remaining := con.Limiter.RecordFailure(ip)
		msg := "密码错误，还可尝试 " + strconv.Itoa(remaining) + " 次"
		if remaining == 0 {
			msg = "失败次数过多，该 IP 已被封禁 15 分钟"
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": msg})
		return
	}
	con.Limiter.Reset(ip)
	token := con.Sessions.Create()
	setSessionCookie(c, token, int(sessionCookieTTL/time.Second))
	_ = con.Store.Touch()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// LogoutAPI 退出登录：销毁会话并清除 Cookie。
func (con *Console) LogoutAPI(c *gin.Context) {
	token, _ := c.Cookie(middleware.CookieName)
	con.Sessions.Destroy(token)
	setSessionCookie(c, "", -1)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ChangePassword 修改管理密码（写单库 platform_settings，生态配置行不含密码）。
// 必须校验当前密码：会话 Cookie 一旦被盗，攻击者不能直接换掉管理员密码。
func (con *Console) ChangePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
		Confirm     string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码长度至少为 6 位"})
		return
	}
	if len(req.NewPassword) > utils.MaxPasswordBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码长度不能超过 " + strconv.Itoa(utils.MaxPasswordBytes) + " 字节"})
		return
	}
	if req.NewPassword != req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "两次输入的密码不一致"})
		return
	}
	hash, err := con.Store.PasswordHash()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库读取失败"})
		return
	}
	if hash == "" || !utils.CheckPassword(hash, req.OldPassword) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "当前密码错误"})
		return
	}
	newHash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := con.Store.SetPasswordHash(newHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SystemInfoAPI 数据库与端口的只读信息（系统设置页"数据库与端口"卡片）。
// 这些值来自服务器配置文件（文件 > 环境变量 > 默认值），运行期不可改：
// 如需修改请编辑配置文件并重启进程。
func (con *Console) SystemInfoAPI(c *gin.Context) {
	driver := con.Store.Dialect()
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"driver":      driver,
		"configFile":  con.Cfg.ConfigPath,
		"ports":       gin.H{"ui": con.Cfg.UIPort, "npm": con.Cfg.NPMPort, "go": con.Cfg.GoPort, "py": con.Cfg.PyPort, "mvn": con.Cfg.MVNPort},
		"storageDirs": gin.H{"go": con.Cfg.GoStorageDir, "npm": con.Cfg.NPMStorageDir, "py": con.Cfg.PyStorageDir, "mvn": con.Cfg.MVNStorageDir},
		"hint":        "数据库与端口来自服务器配置文件，如需修改请编辑 " + con.Cfg.ConfigPath + " 并重启进程",
	})
}

// sessionCookieTTL 会话 Cookie 的 MaxAge（与会话服务端滑动窗口一致，24h）。
const sessionCookieTTL = 24 * time.Hour

// setSessionCookie 写入会话 Cookie：显式 SameSite=Lax 防跨站携带；
// 通过本机反代走 https 时加 Secure（否则浏览器会拒绝在 http 下发送）。
// X-Forwarded-Proto 仅在直连 peer 为本机可信代理时采信，防止伪造头使 Cookie
// 被打上 Secure 后浏览器拒发（表现为管理员持续掉线）。
func setSessionCookie(c *gin.Context, token string, maxAge int) {
	secure := webutil.ExternalScheme(c) == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.CookieName, token, maxAge, "/", "", secure, true)
}

// pprofEnabled PPROF=1 时开启调试端点（仅 9090）。
func (con *Console) pprofEnabled() bool { return os.Getenv("PPROF") == "1" }
