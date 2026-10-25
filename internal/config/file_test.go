package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir 把 CONFIG_DIR 指向临时目录（测试结束后还原）。
func withConfigDir(t *testing.T, dir string) {
	t.Helper()
	old, had := os.LookupEnv("CONFIG_DIR")
	_ = os.Setenv("CONFIG_DIR", dir)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CONFIG_DIR", old)
		} else {
			_ = os.Unsetenv("CONFIG_DIR")
		}
	})
}

// parseOK 断言解析成功。
func parseOK(t *testing.T, content string) *FileConfig {
	t.Helper()
	fc, err := ParseConfigFile([]byte(content))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return fc
}

// TestParseTemplate 模板内容（[database] 全注释态）应解析为"数据库未配置"。
func TestParseTemplate(t *testing.T) {
	fc := parseOK(t, TemplateContent(PortsConfig{UI: 9090, NPM: 9091, Go: 9092, Py: 9093, MVN: 9094}))
	if fc.Database.Configured() {
		t.Fatalf("模板不应配置数据库: %+v", fc.Database)
	}
	if fc.Ports.UI != 9090 || fc.Ports.MVN != 9094 {
		t.Fatalf("模板端口预填错误: %+v", fc.Ports)
	}
}

// TestParseDatabaseVariants driver 与连接字段的解析。
func TestParseDatabaseVariants(t *testing.T) {
	fc := parseOK(t, `
[database]
driver = "PostgreSQL"
host = "db.local"
user = "pp"
password = "p@ss:word"
database = "permaporter"
port = 5433

[ports]
ui = 10090
`)
	if !fc.Database.Configured() {
		t.Fatal("driver 非空应视为已配置")
	}
	if fc.Database.NormalizedDriver() != DriverPostgreSQL {
		t.Fatalf("驱动应归一为 postgresql: %q", fc.Database.NormalizedDriver())
	}
	if fc.Ports.UI != 10090 || fc.Ports.NPM != 0 {
		t.Fatalf("端口解析错误: %+v", fc.Ports)
	}
	dsn := fc.Database.PostgresDSN()
	want := "postgres://pp:p%40ss%3Aword@db.local:5433/permaporter?sslmode=prefer"
	if dsn != want {
		t.Fatalf("PG DSN 拼接错误:\n got %s\nwant %s", dsn, want)
	}
}

// TestParseError TOML 语法错误必须返回含解析错误的错误（拒绝启动依据）。
func TestParseError(t *testing.T) {
	if _, err := ParseConfigFile([]byte("not a toml [")); err == nil {
		t.Fatal("语法错误应返回错误")
	} else if !strings.Contains(err.Error(), "TOML 解析错误") {
		t.Fatalf("错误应标注解析错误: %v", err)
	}
}

// TestDatabaseValidate PG 必填字段与非法 driver。
func TestDatabaseValidate(t *testing.T) {
	cases := []struct {
		name string
		db   DatabaseConfig
		ok   bool
	}{
		{"未配置", DatabaseConfig{}, true},
		{"sqlite", DatabaseConfig{Driver: "sqlite"}, true},
		{"pg 完整", DatabaseConfig{Driver: "postgresql", Host: "h", User: "u", Password: "p", Database: "d"}, true},
		{"pg 缺 host", DatabaseConfig{Driver: "postgresql", User: "u", Password: "p", Database: "d"}, false},
		{"pg 缺 password", DatabaseConfig{Driver: "postgresql", Host: "h", User: "u", Database: "d"}, false},
		{"非法 driver", DatabaseConfig{Driver: "mysql"}, false},
	}
	for _, c := range cases {
		err := c.db.Validate()
		if c.ok && err != nil {
			t.Fatalf("%s: 不应失败: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: 应失败", c.name)
		}
	}
}

// TestWriteFileAtomic 原子写入与 0600 权限。
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permaporter.toml")
	if err := WriteFileAtomic(path, []byte("x = 1\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "x = 1\n" {
		t.Fatalf("写入内容错误: %q %v", data, err)
	}
	// 0600 权限只在支持 POSIX 权限位的平台可见（Windows 仅映射只读位）
	if !runtimeWindows() {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("权限应为 0600: %v", fi.Mode())
		}
	}
	// 无残留临时文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("临时文件残留: %d 个条目", len(entries))
	}
}

// TestApplyFileConfigPriority 取值优先级：文件 > 环境变量 > 默认值。
func TestApplyFileConfigPriority(t *testing.T) {
	cfg := &Config{UIPort: 9090, NPMPort: 9091, GoPort: 9092, PyPort: 9093, MVNPort: 9094,
		DBDSN: "file:env.db"}

	// 文件端口覆盖、未填端口保留环境变量值
	ApplyFileConfig(cfg, &FileConfig{Ports: PortsConfig{UI: 10090}})
	if cfg.UIPort != 10090 || cfg.NPMPort != 9091 {
		t.Fatalf("端口优先级错误: %+v", cfg)
	}
	if cfg.DatabaseConfigured {
		t.Fatal("无数据库段不应视为已配置")
	}

	// sqlite：文件 DSN 覆盖
	ApplyFileConfig(cfg, &FileConfig{Database: DatabaseConfig{Driver: "sqlite", DSN: "file:file.db"}})
	if cfg.DBDSN != "file:file.db" || !cfg.DatabaseConfigured || cfg.EffectiveDBDriver() != DriverSQLite {
		t.Fatalf("sqlite 合并错误: %+v", cfg)
	}

	// postgresql：DSN 由连接字段拼出
	pg := DatabaseConfig{Driver: "postgresql", Host: "h", User: "u", Password: "p", Database: "d", Port: 5432}
	ApplyFileConfig(cfg, &FileConfig{Database: pg})
	if !strings.HasPrefix(cfg.DBDSN, "postgres://u:p@h:5432/d?sslmode=prefer") {
		t.Fatalf("pg DSN 错误: %s", cfg.DBDSN)
	}

	// 未配置时生效驱动回落 sqlite
	ApplyFileConfig(cfg, &FileConfig{})
	if cfg.EffectiveDBDriver() != DriverSQLite {
		t.Fatalf("未配置应回落 sqlite: %s", cfg.EffectiveDBDriver())
	}
}

// TestFillDatabaseSection 向导写回：[database] 段填充、其余内容逐行保留。
func TestFillDatabaseSection(t *testing.T) {
	ports := PortsConfig{UI: 9090, NPM: 9091, Go: 9092, Py: 9093, MVN: 9094}
	base := TemplateContent(ports)

	// sqlite 填充
	got := FillDatabaseSection(base, DatabaseConfig{Driver: "sqlite", DSN: "file:./data/x.db"}, ports)
	if !strings.Contains(got, `driver = "sqlite"`) || !strings.Contains(got, `dsn = "file:./data/x.db"`) {
		t.Fatalf("sqlite 段填充错误:\n%s", got)
	}
	if !strings.Contains(got, "ui  = 9090") || !strings.Contains(got, "mvn = 9094") {
		t.Fatalf("端口段应保留:\n%s", got)
	}
	// 手工注释保留
	custom := strings.Replace(base, "# 安全：", "# 我的手工注释\n# 安全：", 1)
	filled := FillDatabaseSection(custom, DatabaseConfig{Driver: "sqlite"}, ports)
	if !strings.Contains(filled, "# 我的手工注释") {
		t.Fatalf("手工注释应保留:\n%s", filled)
	}
	// PG 填充 + 重新解析应得到等价配置
	pg := DatabaseConfig{Driver: "postgresql", Host: "pg", User: "u", Password: "p", Database: "d", SSLMode: "disable"}
	filledPG := FillDatabaseSection(base, pg, ports)
	fc := parseOK(t, filledPG)
	if fc.Database.NormalizedDriver() != DriverPostgreSQL || fc.Database.Host != "pg" ||
		fc.Database.SSLMode != "disable" || fc.Ports.UI != 9090 {
		t.Fatalf("PG 填充后回读不一致:\n%s\n%+v", filledPG, fc.Database)
	}
	// 形态异常（无 [database] 段）→ 整体按模板重写
	fallback := FillDatabaseSection("[ports]\nui = 1\n", DatabaseConfig{Driver: "sqlite"}, ports)
	if !strings.Contains(fallback, "[database]") || !strings.Contains(fallback, `driver = "sqlite"`) {
		t.Fatalf("异常形态应回退模板重写:\n%s", fallback)
	}
}

// TestEnsureConfigFile 模板生成：目录缺失创建、文件缺失写模板、已存在不动。
func TestEnsureConfigFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "conf", "deep")
	withConfigDir(t, dir)
	ports := PortsConfig{UI: 9090, NPM: 9091, Go: 9092, Py: 9093, MVN: 9094}

	path, written, err := EnsureConfigFile(ports)
	if err != nil || !written {
		t.Fatalf("首次应生成模板: written=%v err=%v", written, err)
	}
	if path != ConfigFilePath() {
		t.Fatalf("路径错误: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("模板文件应存在: %v", err)
	}
	// 再次调用：已存在不重写
	before, _ := os.ReadFile(path)
	_, written, err = EnsureConfigFile(ports)
	if err != nil || written {
		t.Fatalf("已存在不应重写: written=%v err=%v", written, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("已存在的文件不应被改动")
	}
}
