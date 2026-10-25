// 服务器配置文件层：CONFIG_DIR/permaporter.toml。
//
// 只存两类启动必需项：数据库（[database]，driver 缺失或为空即"数据库未配置"）
// 与五端口（[ports]）。取值优先级：配置文件 > 环境变量 > 内置默认值——
// 文件中已填写的项生效，未填写的项按环境变量、默认值兜底，环境变量取值路径保持不变。
//
// 文件写回一律走临时文件 + 原子改名（与产物不可变约束同一手法）；
// postgresql 密码为明文存储，文件权限 0600（模板注释中提醒）。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// 数据库驱动取值。
const (
	DriverSQLite     = "sqlite"
	DriverPostgreSQL = "postgresql"
)

// ConfigDir 配置目录（CONFIG_DIR，默认 ./config，容器内 /app/config）。
func ConfigDir() string { return envStr("CONFIG_DIR", "./config") }

// ConfigFilePath 配置文件完整路径。
func ConfigFilePath() string { return filepath.Join(ConfigDir(), "permaporter.toml") }

// DatabaseConfig [database] 段：driver 缺失或为空即"数据库未配置"（唯一判定依据，
// 不引入额外 initialized 标志）。sqlite 只用 DSN；postgresql 用连接字段。
type DatabaseConfig struct {
	Driver   string `toml:"driver"`   // "sqlite" | "postgresql"；空 = 未配置
	DSN      string `toml:"dsn"`      // sqlite 连接 DSN
	Host     string `toml:"host"`     // postgresql 主机
	Port     int    `toml:"port"`     // postgresql 端口（默认 5432）
	User     string `toml:"user"`     // postgresql 用户名
	Password string `toml:"password"` // postgresql 密码（明文存储，文件权限 0600）
	Database string `toml:"database"` // postgresql 库名
	SSLMode  string `toml:"sslmode"`  // postgresql sslmode（默认 prefer）
}

// Configured 数据库是否已配置：driver 非空（大小写不敏感，允许 SQLite/postgresql 等写法）。
func (d DatabaseConfig) Configured() bool { return strings.TrimSpace(d.Driver) != "" }

// NormalizedDriver 归一化驱动名（小写）；未配置时返回空串。
func (d DatabaseConfig) NormalizedDriver() string {
	return strings.ToLower(strings.TrimSpace(d.Driver))
}

// Validate 数据库段启动校验（仅在建库前做字段完整性检查；连通性由 Store 打开时验证）。
func (d DatabaseConfig) Validate() error {
	switch d.NormalizedDriver() {
	case "":
		return nil // 未配置：由启动判定进入初始化模式，不是错误
	case DriverSQLite:
		return nil // DSN 缺省在合并层用默认值兜底
	case DriverPostgreSQL:
		for _, f := range []struct{ key, val string }{
			{"host", d.Host}, {"user", d.User}, {"password", d.Password}, {"database", d.Database},
		} {
			if strings.TrimSpace(f.val) == "" {
				return &ValidationError{Item: "数据库",
					Detail: "postgresql 缺少必填字段 " + f.key + "（编辑 " + ConfigFilePath() + " 的 [database] 段补齐后重启）"}
			}
		}
		if d.Port < 0 || d.Port > 65535 {
			return &ValidationError{Item: "数据库", Detail: "postgresql port 必须在 0-65535 范围内"}
		}
		return nil
	default:
		return &ValidationError{Item: "数据库",
			Detail: "driver 取值 " + strconv.Quote(d.Driver) + " 无效，只支持 sqlite / postgresql"}
	}
}

// PostgresDSN 把连接字段拼为 pgx URL（Port 缺省 5432，sslmode 缺省 prefer）。
func (d DatabaseConfig) PostgresDSN() string {
	port := d.Port
	if port == 0 {
		port = 5432
	}
	sslmode := d.SSLMode
	if sslmode == "" {
		sslmode = "prefer"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		urlEncode(d.User), urlEncode(d.Password), d.Host, port, d.Database, urlEncode(sslmode))
}

// urlEncode 对 URL 组件做百分号编码（密码常含特殊字符）。
func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// PortsConfig [ports] 段：五端口（0 视为未填写，按环境变量/默认值兜底）。
type PortsConfig struct {
	UI  int `toml:"ui"`
	NPM int `toml:"npm"`
	Go  int `toml:"go"`
	Py  int `toml:"py"`
	MVN int `toml:"mvn"`
}

// FileConfig permaporter.toml 全部内容。
type FileConfig struct {
	Database DatabaseConfig `toml:"database"`
	Ports    PortsConfig    `toml:"ports"`
}

// ParseConfigFile 解析配置文件内容。TOML 格式错误时返回含具体解析错误的错误
// （调用方据此输出文件路径与解析错误并拒绝启动）。
func ParseConfigFile(data []byte) (*FileConfig, error) {
	var fc FileConfig
	if err := toml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("TOML 解析错误: %w", err)
	}
	return &fc, nil
}

// LoadConfigFile 读取并解析配置文件。
func LoadConfigFile(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfigFile(data)
}

// WriteFileAtomic 临时文件 + 原子改名写入（权限 0600：postgresql 密码明文存储）。
// 临时文件写在同目录（保证与目标同一文件系统，rename 原子生效）。
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".permaporter.toml-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // 改名成功后 Remove 报错被忽略
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置文件权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("原子改名失败: %w", err)
	}
	return nil
}

// EnsureConfigFile 启动第一步：确保配置目录与配置文件存在。
// 目录不存在 → 按默认路径创建；文件缺失 → 写入模板（端口预填当前生效值——
// 即环境变量/默认值合并结果，容器内 ENV 端口因此天然保持生效）并记一条 INFO 日志。
// 返回值 templateWritten 表示本次是否新生成了模板。
func EnsureConfigFile(effectivePorts PortsConfig) (path string, templateWritten bool, err error) {
	path = ConfigFilePath()
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return path, false, fmt.Errorf("创建配置目录 %s 失败: %w", ConfigDir(), err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return path, false, nil
	} else if !os.IsNotExist(statErr) {
		return path, false, fmt.Errorf("读取配置文件状态失败: %w", statErr)
	}
	if err := WriteFileAtomic(path, []byte(TemplateContent(effectivePorts))); err != nil {
		return path, false, fmt.Errorf("写入配置模板失败: %w", err)
	}
	return path, true, nil
}

// TemplateContent 配置文件模板：[database] 段全注释态（driver 不预填，天然"未配置"），
// [ports] 预填当前生效端口。文件顶部注释说明用途、重启生效与明文密码提醒。
func TemplateContent(ports PortsConfig) string {
	return fmt.Sprintf(`# PermaPorter 服务器配置文件
#
# 用途：存放启动必需项——数据库连接与五端口监听地址。上游源、出站代理、TTL 等
#       运行时配置在控制台「系统配置」按生态修改（存于数据库 ecosystem_settings）。
# 生效：本文件修改后需重启进程才生效（取值优先级：本文件 > 环境变量 > 内置默认值；
#       本文件中已填写的项优先生效，注释/缺省的项按环境变量与默认值兜底）。
# 安全：[database] 中的 postgresql 密码为明文存储，请保持本文件权限 0600，
#       并确保配置目录不对外暴露。

# ---------- 数据库（driver 缺失或为空 = 数据库未配置：首次启动进入初始化向导） ----------
# driver 只支持两种取值（去掉行首 # 并取消相应行的注释后生效）：
#   driver = "sqlite"         # 单文件库（默认形态，数据在 DSN 指向的 .db 文件）
#   driver = "postgresql"     # 外部 PostgreSQL（连接字段如下）
[database]
# driver = "sqlite"
# dsn = "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"
# driver = "postgresql"
# host = "127.0.0.1"        # PostgreSQL 主机（容器 compose 内填服务名 postgres）
# port = 5432               # PostgreSQL 端口（缺省 5432）
# user = "permaporter"      # 用户名（必填）
# password = "change-me"    # 密码（必填，明文存储，注意文件权限 0600）
# database = "permaporter"  # 库名（必填）
# sslmode = "prefer"        # prefer（默认）/ disable / require / verify-ca / verify-full

# ---------- 五端口（互不相同，违反即拒绝启动；修改后需同步客户端与防火墙） ----------
[ports]
ui  = %d # 控制台（SPA + /api + 聚合 healthz/metrics）
npm = %d # npm Registry
go  = %d # GOPROXY
py  = %d # PyPI Simple
mvn = %d # Maven 仓库
`, ports.UI, ports.NPM, ports.Go, ports.Py, ports.MVN)
}

// ApplyFileConfig 把配置文件内容叠加到环境变量配置之上（文件 > 环境变量 > 默认值）。
// 只覆盖文件中实际填写（非零值）的项；Database 段整体采用文件的值（driver 为空的
// 模板天然不影响任何数据库行为）。返回 DatabaseConfigured 与生效驱动。
func ApplyFileConfig(c *Config, fc *FileConfig) {
	if fc == nil {
		return
	}
	if p := fc.Ports; p.UI != 0 {
		c.UIPort = p.UI
	}
	if p := fc.Ports; p.NPM != 0 {
		c.NPMPort = p.NPM
	}
	if p := fc.Ports; p.Go != 0 {
		c.GoPort = p.Go
	}
	if p := fc.Ports; p.Py != 0 {
		c.PyPort = p.Py
	}
	if p := fc.Ports; p.MVN != 0 {
		c.MVNPort = p.MVN
	}
	c.Database = fc.Database
	c.DatabaseConfigured = fc.Database.Configured()
	if !c.DatabaseConfigured {
		return
	}
	switch fc.Database.NormalizedDriver() {
	case DriverSQLite:
		if fc.Database.DSN != "" {
			c.DBDSN = fc.Database.DSN
		}
	case DriverPostgreSQL:
		c.DBDSN = fc.Database.PostgresDSN()
	}
}

// FillDatabaseSection 把向导收集的数据库配置写入配置文件文本的 [database] 段，
// 其余内容（[ports] 段、全部注释与手工修改）逐行保留。文件形态与模板不一致时
// 回退为按模板结构整体重写（仅初始化向导调用，此时文件应仍为模板态）。
func FillDatabaseSection(content string, db DatabaseConfig, ports PortsConfig) string {
	lines := strings.Split(content, "\n")
	inDatabase := false
	dbWritten := false
	var out []string
	appendFilled := func() {
		out = append(out, filledDatabaseLines(db)...)
		dbWritten = true
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inDatabase {
				appendFilled()
				inDatabase = false
			}
			if trimmed == "[database]" {
				inDatabase = true
				out = append(out, line)
				continue
			}
		}
		if !inDatabase {
			out = append(out, line)
		}
		// [database] 段内的行全部丢弃（模板中的注释示例由 filledDatabaseLines 重建）
	}
	if inDatabase && !dbWritten {
		appendFilled()
	}
	if !dbWritten {
		// 文件里没有 [database] 段（形态异常）：整体按模板重写，保证语义完整
		return TemplateContent(ports)
	}
	return strings.Join(out, "\n")
}

// FillPortsSection 更新 [ports] 段的五端口值（逐行保留行尾注释），其余内容不动。
// 段内一行都没匹配到时追加一个完整段（仅向导写回调用）。
func FillPortsSection(content string, ports PortsConfig) string {
	lines := strings.Split(content, "\n")
	values := map[string]int{"ui": ports.UI, "npm": ports.NPM, "go": ports.Go, "py": ports.Py, "mvn": ports.MVN}
	inPorts := false
	updated := map[string]bool{}
	out := make([]string, 0, len(lines)+8)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inPorts = trimmed == "[ports]"
			out = append(out, line)
			continue
		}
		if inPorts && !strings.HasPrefix(trimmed, "#") {
			if key, rest, ok := strings.Cut(trimmed, "="); ok {
				name := strings.ToLower(strings.TrimSpace(key))
				if _, isPort := values[name]; isPort {
					comment := ""
					if i := strings.Index(rest, "#"); i >= 0 {
						comment = " " + strings.TrimSpace(rest[i:])
					}
					out = append(out, fmt.Sprintf("%s = %d%s", name, values[name], comment))
					updated[name] = true
					continue
				}
			}
		}
		out = append(out, line)
	}
	if len(updated) == 0 {
		out = append(out, "",
			"[ports]",
			fmt.Sprintf("ui  = %d", ports.UI),
			fmt.Sprintf("npm = %d", ports.NPM),
			fmt.Sprintf("go  = %d", ports.Go),
			fmt.Sprintf("py  = %d", ports.Py),
			fmt.Sprintf("mvn = %d", ports.MVN))
	}
	return strings.Join(out, "\n")
}

// filledDatabaseLines 生成已填充的 [database] 段内容（含驱动相关的字段取舍）。
// 段前的说明注释（模板自带）在 FillDatabaseSection 中逐行保留，此处不再重复。
func filledDatabaseLines(db DatabaseConfig) []string {
	switch db.NormalizedDriver() {
	case DriverPostgreSQL:
		port := db.Port
		if port == 0 {
			port = 5432
		}
		sslmode := db.SSLMode
		if sslmode == "" {
			sslmode = "prefer"
		}
		return []string{
			`# postgresql 连接（密码明文存储，请保持本文件权限 0600）`,
			fmt.Sprintf("driver = %q", DriverPostgreSQL),
			fmt.Sprintf("host = %q", db.Host),
			fmt.Sprintf("port = %d", port),
			fmt.Sprintf("user = %q", db.User),
			fmt.Sprintf("password = %q", db.Password),
			fmt.Sprintf("database = %q", db.Database),
			fmt.Sprintf("sslmode = %q", sslmode),
			``,
		}
	default:
		dsn := db.DSN
		if dsn == "" {
			dsn = "file:./data/permaporter.db?_busy_timeout=30000&_foreign_keys=1&_journal_mode=DELETE"
		}
		return []string{
			fmt.Sprintf("driver = %q", DriverSQLite),
			fmt.Sprintf("dsn = %q", dsn),
			``,
		}
	}
}
