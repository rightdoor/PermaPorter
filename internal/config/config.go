// Package config 加载环境变量配置并执行启动校验。
package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version 服务版本号。由 Makefile 的 VERSION 统一经
// go build -ldflags "-X permaporter/internal/config.Version=…" 注入；
// 直接用 go build/go run 时为 dev。
var Version = "dev"

// Config 多端口服务的全部启动配置。
// 端口与存储目录按生态隔离；upstream/fallback/outbound_proxy/meta_ttl
// 存在单库 ecosystem_settings，不出现在进程配置中。
type Config struct {
	UIPort  int    // 9090 控制台（SPA + /api + /admin + 聚合健康/指标）
	NPMPort int    // 9091 npm registry
	GoPort  int    // 9092 GOPROXY
	PyPort  int    // 9093 PyPI Simple（PEP 503）
	MVNPort int    // 9094 Maven 仓库（Java 构件，Gradle 同协议）
	DBDSN   string // 单库（平台 + 全部生态数据）：sqlite DSN 或 postgresql URL

	GoStorageDir  string // go 存储树（modules/ tmp/）
	NPMStorageDir string // npm 存储树（meta/ packages/）
	PyStorageDir  string // py 存储树（packages/）
	MVNStorageDir string // mvn 存储树（镜像 Maven 仓库目录布局）

	InitPassword string        // 仅写 platform_settings（首次初始化）
	GoUpstream   string        // go 生态默认主上游（初始化时写入 ecosystem_settings）
	GoFallback   string        // go 生态默认备用上游（仅 go 侧有回退链语义）
	NPMUpstream  string        // npm 生态默认主上游
	PyUpstream   string        // py 生态默认主上游（PyPI Simple 根）
	MVNUpstream  string        // mvn 生态默认主上游（Maven 仓库根）
	MetaTTL      time.Duration // 元数据缓存刷新间隔初始值（初始化时写入 ecosystem_settings）

	// ---------- 配置文件层（file.go）----------
	ConfigPath         string         // permaporter.toml 路径（启动时填充）
	Database           DatabaseConfig // [database] 段原文（未配置时 Driver 为空）
	DatabaseConfigured bool           // driver 非空（未初始化模式的唯一判定依据）
	Unattended         bool           // UNATTENDED=1：无头模式（CI/测试/脚本）
	InitToken          string         // INIT_TOKEN：初始化向导第一步的访问令牌（可选）
}

// EffectiveDBDriver 返回生效的数据库驱动：未配置时按 sqlite 处理（无头模式回落
// 默认 SQLite DSN，与配置文件层引入前的行为一致）。
func (c *Config) EffectiveDBDriver() string {
	if c.DatabaseConfigured {
		return c.Database.NormalizedDriver()
	}
	return DriverSQLite
}

// UnattendedMode 是否处于无头模式（UNATTENDED=1，供 CI/测试/脚本：跳过初始化向导，
// 数据库未配置时回落默认 SQLite 行为，与配置文件层引入前一致）。
func (c *Config) UnattendedMode() bool { return c.Unattended }

// Load 从环境变量读取配置，未设置时使用默认值。
// DSN 形态为 mattn 风格参数（DELETE 回滚日志模式：
// 运行期只存在单个 .db 文件，不生成 -wal/-shm）。
func Load() *Config {
	return &Config{
		UIPort:  envInt("UI_PORT", 9090),
		NPMPort: envInt("NPM_PORT", 9091),
		GoPort:  envInt("GO_PORT", 9092),
		PyPort:  envInt("PY_PORT", 9093),
		MVNPort: envInt("MVN_PORT", 9094),

		DBDSN: envStr("DB_DSN", "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"),

		GoStorageDir:  envStr("GO_STORAGE_DIR", "./data/storage-go"),
		NPMStorageDir: envStr("NPM_STORAGE_DIR", "./data/storage-npm"),
		PyStorageDir:  envStr("PY_STORAGE_DIR", "./data/storage-py"),
		MVNStorageDir: envStr("MVN_STORAGE_DIR", "./data/storage-mvn"),

		InitPassword: os.Getenv("INIT_PASSWORD"),
		GoUpstream:   envStr("DEFAULT_GO_UPSTREAM", "https://proxy.golang.org"),
		GoFallback:   envStr("DEFAULT_GO_FALLBACK_UPSTREAM", ""),
		NPMUpstream:  envStr("DEFAULT_NPM_UPSTREAM", "https://registry.npmjs.org"),
		PyUpstream:   envStr("DEFAULT_PY_UPSTREAM", "https://pypi.org/simple/"),
		MVNUpstream:  envStr("DEFAULT_MVN_UPSTREAM", "https://repo.maven.apache.org/maven2"),
		MetaTTL:      envDuration("META_TTL", 2*time.Minute),

		Unattended: os.Getenv("UNATTENDED") == "1",
		InitToken:  os.Getenv("INIT_TOKEN"),
	}
}

// Validate 启动校验：违反任意一条即返回错误，调用方必须拒绝启动、不监听任何端口。
// ① 五端口互不相同；② 四棵存储目录两两不同且互不为父子。
// 存储目录互异是生态隔离的结构性防线（数据库已合并为单库，无需路径互异校验）。
func (c *Config) Validate() error {
	ports := []struct {
		name string
		port int
	}{
		{"UI_PORT", c.UIPort}, {"NPM_PORT", c.NPMPort}, {"GO_PORT", c.GoPort}, {"PY_PORT", c.PyPort}, {"MVN_PORT", c.MVNPort},
	}
	for _, p := range ports {
		// 端口 0 会静默绑定随机端口、>65535 无法监听：启动即拒绝
		if p.port < 1 || p.port > 65535 {
			return &ValidationError{Item: "端口",
				Detail: p.name + " 必须在 1-65535 范围内，当前为 " + itoa(p.port)}
		}
	}
	for i := 0; i < len(ports); i++ {
		for j := i + 1; j < len(ports); j++ {
			if ports[i].port == ports[j].port {
				return &ValidationError{Item: "端口",
					Detail: "五端口必须互不相同: " + ports[i].name + " 与 " + ports[j].name + " 均为 " + itoa(ports[i].port)}
			}
		}
	}
	dirs := []struct{ name, dir string }{
		{"GO_STORAGE_DIR", c.GoStorageDir},
		{"NPM_STORAGE_DIR", c.NPMStorageDir},
		{"PY_STORAGE_DIR", c.PyStorageDir},
		{"MVN_STORAGE_DIR", c.MVNStorageDir},
	}
	for i := 0; i < len(dirs); i++ {
		for j := i + 1; j < len(dirs); j++ {
			if sameOrNested(dirs[i].dir, dirs[j].dir) {
				return &ValidationError{Item: "存储目录",
					Detail: dirs[i].name + " 与 " + dirs[j].name + " 相同或互为嵌套: " + dirs[i].dir + " / " + dirs[j].dir}
			}
		}
	}
	return nil
}

// ValidationError 启动校验失败。
type ValidationError struct {
	Item   string
	Detail string
}

func (e *ValidationError) Error() string { return e.Item + "校验失败: " + e.Detail }

// sameOrNested 判断两个目录相同或互为父子。
// 比较基于绝对真实路径：字符串前缀比较会漏判真实嵌套
// （如 "." 与 "./data/storage-npm"——Clean 后无前缀关系，但后者物理上在前者内，
// 清理器会按库外文件误删对方产物）。目录尚未创建时 EvalSymlinks 失败，
// 退回 Abs 结果（展开工作目录前缀已足够拦截此类配置错误）。
// Windows 路径大小写不敏感，统一按大小写折叠比较。
func sameOrNested(a, b string) bool {
	a = absDir(a)
	b = absDir(b)
	if runtimeWindows() {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// absDir 归一为绝对路径，并尽力解析符号链接（目录可能尚未创建）。
func absDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func runtimeWindows() bool { return os.PathSeparator == '\\' }

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		// 非法值（如端口打错字）静默回退会让实际监听端口与配置不符，留下告警痕迹
		log.Printf("[config] %s=%q 不是合法整数，回退默认值 %d", key, v, def)
	}
	return def
}

// envDuration 读取时长配置（如 30s、5m、1h），非法值回退默认值。
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func itoa(n int) string { return strconv.Itoa(n) }
