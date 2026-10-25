// 未初始化模式（初始化向导）：配置文件 [database].driver 为空且未设 UNATTENDED 时，
// 进程仅监听 9090（SPA + /api/setup/*），不连接数据库、不监听四个协议端口。
//
// initialize 成功后经 cmd 注入的 BootFunc 进程内原地启动完整后端（不重启进程），
// 9090 的处理器被替换为完整控制台；向导端点随后返回 409。重新进入向导的唯一途径
// 是手工清空/注释配置文件 [database] 段后重启（文档说明的高级操作）。
package web

import (
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/utils"
	"permaporter/internal/core/webui"
)

// SetupUpstreams 向导收集的四生态初始上游（取代对 DEFAULT_*_UPSTREAM 环境变量的依赖；
// 空串按配置默认值兜底由前端预填保证，后端仅校验格式）。
type SetupUpstreams struct {
	Go         string `json:"go"`
	GoFallback string `json:"goFallback"`
	NPM        string `json:"npm"`
	Py         string `json:"py"`
	MVN        string `json:"mvn"`
}

// SetupPorts 向导所选五端口。
type SetupPorts struct {
	UI  int `json:"ui"`
	NPM int `json:"npm"`
	Go  int `json:"go"`
	Py  int `json:"py"`
	MVN int `json:"mvn"`
}

// SetupRequest initialize 通过全部校验后交给 BootFunc 的启动参数
// （AdminHash 已是 Bcrypt 哈希，明文不出本包）。
type SetupRequest struct {
	Driver    string                // config.DriverSQLite | config.DriverPostgreSQL
	DSN       string                // sqlite 连接 DSN（已兜底默认值）
	PG        config.DatabaseConfig // postgresql 连接字段（NormalizedDriver/DSN 已就绪）
	Ports     SetupPorts
	Upstreams SetupUpstreams
	AdminHash string
}

// SetupBootFunc 由 cmd 注入：打开单库 → 建适配器 → 写管理员密码与生态初始上游 →
// 监听四个协议端口 → 把 9090 处理器替换为完整控制台。任一步失败返回错误
// （向导向前端展示；配置文件此时可能已写回，失败原因持续存在时下次重启会拒绝启动并给出明确日志）。
type SetupBootFunc func(req SetupRequest) error

// SetupConsole 未初始化模式的控制台：仅 9090，SPA + 向导 API。
type SetupConsole struct {
	Cfg       *config.Config
	InitToken string // 可选 INIT_TOKEN：设置后向导需先输入令牌
	Boot      SetupBootFunc

	// done initialize 成功后置位：向导端点改答 409（完整后端已原地启动）
	done atomic.Bool
}

// RegisterSetupRoutes 在 9090 引擎上注册未初始化模式的全部路由（仅 SPA + 向导 API +
// 探活；不注册 /api/admin 与任何生态数据接口——数据库尚未存在）。
func RegisterSetupRoutes(r *gin.Engine, sc *SetupConsole) {
	initStaticAssets(webui.FS)
	r.GET("/static/*filepath", staticHandler)

	r.GET("/api/state", sc.State)
	r.GET("/healthz", sc.Healthz)

	r.GET("/api/setup/status", sc.StatusAPI)
	r.POST("/api/setup/test-database", sc.TestDatabaseAPI)
	r.POST("/api/setup/initialize", sc.InitializeAPI)

	r.GET("/", sc.SPA)
	r.GET("/admin", sc.SPA)
	r.GET("/admin/*rest", sc.SPA)
	r.NoRoute(sc.NotFound)
}

// Healthz 未初始化模式探活：进程存活即 ok（db 未连接，明确标注 setupPending）。
func (sc *SetupConsole) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "ui": "ok", "setupPending": true, "version": config.Version})
}

// State 初始状态：setupMode 标记未初始化模式（前端路由守卫据此导向向导页）。
func (sc *SetupConsole) State(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"initialized": false,
		"authed":      false,
		"setupMode":   true,
		"ok":          true,
		"version":     config.Version,
		"goProxy":     externalOrigin(c, sc.Cfg, sc.Cfg.GoPort),
		"npmRegistry": externalOrigin(c, sc.Cfg, sc.Cfg.NPMPort) + "/",
		"pyIndex":     externalOrigin(c, sc.Cfg, sc.Cfg.PyPort) + "/simple/",
		"mavenRepo":   externalOrigin(c, sc.Cfg, sc.Cfg.MVNPort),
	})
}

// SPA 输出 SPA 外壳（setupMode 注入，前端路由守卫导向向导页）。
func (sc *SetupConsole) SPA(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", webui.IndexHTML(webui.State{
		Initialized: false,
		SetupMode:   true,
		Version:     config.Version,
		GoProxy:     externalOrigin(c, sc.Cfg, sc.Cfg.GoPort),
		NPMRegistry: externalOrigin(c, sc.Cfg, sc.Cfg.NPMPort) + "/",
		PyIndex:     externalOrigin(c, sc.Cfg, sc.Cfg.PyPort) + "/simple/",
		MavenRepo:   externalOrigin(c, sc.Cfg, sc.Cfg.MVNPort),
	}))
}

// NotFound 其余路径：浏览器回落 SPA 外壳、API 形态 JSON 404（与控制台口径一致）。
func (sc *SetupConsole) NotFound(c *gin.Context) {
	if wantsBrowserHTML(c) {
		sc.SPA(c)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
}

// ---------- 向导 API ----------

// StatusAPI 向导第一步数据：是否已配置、配置文件路径、可预填值（生效端口与
// 模板/环境变量默认上游）。未初始化模式下 configured 恒为 false（进程内已完成
// 初始化时全部向导端点答 409）。
func (sc *SetupConsole) StatusAPI(c *gin.Context) {
	if sc.done.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "初始化已完成，请刷新页面登录"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":                true,
		"configured":        false,
		"configFile":        sc.Cfg.ConfigPath,
		"initTokenRequired": sc.InitToken != "",
		"ports": gin.H{
			"ui": sc.Cfg.UIPort, "npm": sc.Cfg.NPMPort, "go": sc.Cfg.GoPort,
			"py": sc.Cfg.PyPort, "mvn": sc.Cfg.MVNPort,
		},
		"upstreams": gin.H{
			"go":         sc.Cfg.GoUpstream,
			"goFallback": sc.Cfg.GoFallback,
			"npm":        sc.Cfg.NPMUpstream,
			"py":         sc.Cfg.PyUpstream,
			"mvn":        sc.Cfg.MVNUpstream,
		},
	})
}

// setupDatabasePayload test-database / initialize 共用的数据库选择载荷。
type setupDatabasePayload struct {
	Token    string `json:"token"`
	Driver   string `json:"driver"`
	DSN      string `json:"dsn"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
	SSLMode  string `json:"sslmode"`
}

// toDatabaseConfig 载荷 → DatabaseConfig（sqlite DSN 缺省用默认值）。
func (p *setupDatabasePayload) toDatabaseConfig() config.DatabaseConfig {
	return config.DatabaseConfig{
		Driver:   p.Driver,
		DSN:      p.DSN,
		Host:     strings.TrimSpace(p.Host),
		Port:     p.Port,
		User:     strings.TrimSpace(p.User),
		Password: p.Password,
		Database: strings.TrimSpace(p.Database),
		SSLMode:  strings.TrimSpace(p.SSLMode),
	}
}

// checkToken INIT_TOKEN 校验（未设置时放行）。常数时间比较：向导端点在未初始化阶段
// 对网络开放，令牌比对不应暴露逐字符匹配的时间侧信道。
func (sc *SetupConsole) checkToken(got string) bool {
	if sc.InitToken == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(sc.InitToken)) == 1
}

// probeDatabase 连通性测试（不落盘、不建 schema）。
func probeDatabase(db config.DatabaseConfig) error {
	switch db.NormalizedDriver() {
	case config.DriverSQLite:
		dsn := db.DSN
		if dsn == "" {
			dsn = "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"
		}
		return coredatabase.ProbeSQLite(dsn)
	case config.DriverPostgreSQL:
		return coredatabase.ProbePostgreSQL(db.PostgresDSN())
	default:
		return &config.ValidationError{Item: "数据库", Detail: "driver 只支持 sqlite / postgresql"}
	}
}

// TestDatabaseAPI 仅测试连接可用性，不写配置文件、不落盘。
func (sc *SetupConsole) TestDatabaseAPI(c *gin.Context) {
	if sc.done.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "初始化已完成"})
		return
	}
	var p setupDatabasePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if !sc.checkToken(p.Token) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "初始化令牌错误"})
		return
	}
	db := p.toDatabaseConfig()
	if err := db.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := probeDatabase(db); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "连接失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "连接成功"})
}

// setupInitializePayload initialize 请求载荷。
type setupInitializePayload struct {
	setupDatabasePayload
	Ports     SetupPorts     `json:"ports"`
	Upstreams SetupUpstreams `json:"upstreams"`
	Password  string         `json:"adminPassword"`
	Confirm   string         `json:"confirm"`
}

// InitializeAPI 全量校验 → 原子写回配置文件 → Boot 原地启动完整后端。
func (sc *SetupConsole) InitializeAPI(c *gin.Context) {
	if sc.done.Load() {
		c.JSON(http.StatusConflict, gin.H{"error": "初始化已完成，请刷新页面登录"})
		return
	}
	var p setupInitializePayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if !sc.checkToken(p.Token) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "初始化令牌错误"})
		return
	}

	// 数据库段
	db := p.toDatabaseConfig()
	if err := db.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if db.NormalizedDriver() == config.DriverSQLite && db.DSN == "" {
		db.DSN = "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"
	}

	// 端口段：缺省项按当前生效值兜底，随后互异校验
	ports := SetupPorts{
		UI: orDefault(p.Ports.UI, sc.Cfg.UIPort), NPM: orDefault(p.Ports.NPM, sc.Cfg.NPMPort),
		Go: orDefault(p.Ports.Go, sc.Cfg.GoPort), Py: orDefault(p.Ports.Py, sc.Cfg.PyPort),
		MVN: orDefault(p.Ports.MVN, sc.Cfg.MVNPort),
	}
	merged := *sc.Cfg
	merged.UIPort, merged.NPMPort, merged.GoPort, merged.PyPort, merged.MVNPort = ports.UI, ports.NPM, ports.Go, ports.Py, ports.MVN
	if err := merged.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "端口校验失败: " + err.Error()})
		return
	}

	// 上游段：http(s):// 前缀（goFallback 允许为空）
	ups := SetupUpstreams{
		Go: strings.TrimSpace(p.Upstreams.Go), GoFallback: strings.TrimSpace(p.Upstreams.GoFallback),
		NPM: strings.TrimSpace(p.Upstreams.NPM), Py: strings.TrimSpace(p.Upstreams.Py),
		MVN: strings.TrimSpace(p.Upstreams.MVN),
	}
	for _, u := range []struct{ name, val string }{
		{"go 主上游", ups.Go}, {"go 备用上游", ups.GoFallback}, {"npm 上游", ups.NPM},
		{"py 上游", ups.Py}, {"mvn 上游", ups.MVN},
	} {
		if u.val == "" {
			if u.name == "go 备用上游" {
				continue // 备用上游允许为空
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": u.name + " 不能为空"})
			return
		}
		if !strings.HasPrefix(u.val, "http://") && !strings.HasPrefix(u.val, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": u.name + " 必须以 http:// 或 https:// 开头"})
			return
		}
	}

	// 管理员密码（长度与确认校验与 /api/auth/setup 同口径）
	password := strings.TrimSpace(p.Password)
	if len(password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码长度至少为 6 位"})
		return
	}
	if len(password) > utils.MaxPasswordBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码长度不能超过 " + strconv.Itoa(utils.MaxPasswordBytes) + " 字节"})
		return
	}
	if password != strings.TrimSpace(p.Confirm) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "两次输入的密码不一致"})
		return
	}
	hash, err := utils.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 数据库连通性（写配置文件前先确认可连，避免写回后立刻无法启动）
	if err := probeDatabase(db); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "数据库连接失败: " + err.Error()})
		return
	}

	// 进程内原地启动完整后端（不重启进程：容器内 nginx 常驻，re-exec 不可靠）
	if sc.Boot == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化失败: 后端装配函数未注入"})
		return
	}

	// 原子写回配置文件：填充 [database] 段 + 更新 [ports]，其余内容保留
	content, err := os.ReadFile(sc.Cfg.ConfigPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取配置文件失败: " + err.Error()})
		return
	}
	cfgPorts := config.PortsConfig{UI: ports.UI, NPM: ports.NPM, Go: ports.Go, Py: ports.Py, MVN: ports.MVN}
	filled := config.FillDatabaseSection(string(content), db, cfgPorts)
	filled = config.FillPortsSection(filled, cfgPorts)
	if err := config.WriteFileAtomic(sc.Cfg.ConfigPath, []byte(filled)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入配置文件失败: " + err.Error()})
		return
	}

	// 进程内原地启动完整后端（不重启进程：容器内 nginx 常驻，re-exec 不可靠）
	if err := sc.Boot(SetupRequest{
		Driver: db.NormalizedDriver(), DSN: db.DSN, PG: db,
		Ports: ports, Upstreams: ups, AdminHash: hash,
	}); err != nil {
		// 配置文件已写回：错误原因持续存在时下次重启会以明确日志拒绝启动
		c.JSON(http.StatusInternalServerError, gin.H{"error": "初始化失败: " + err.Error()})
		return
	}
	sc.done.Store(true)
	logSetupInitialized(db, ports)
	c.JSON(http.StatusOK, gin.H{"ok": true, "ports": gin.H{
		"ui": ports.UI, "npm": ports.NPM, "go": ports.Go, "py": ports.Py, "mvn": ports.MVN,
	}})
}

// logSetupInitialized 初始化完成日志（五端口与数据库类型）。
func logSetupInitialized(db config.DatabaseConfig, ports SetupPorts) {
	log.Printf("[setup] 初始化完成：数据库 %s，控制台 :%d / npm :%d / GOPROXY :%d / PyPI :%d / Maven :%d（全部端口已就绪）",
		db.NormalizedDriver(), ports.UI, ports.NPM, ports.Go, ports.Py, ports.MVN)
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
