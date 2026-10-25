package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bootstrapEnv 在隔离环境中执行 Bootstrap：CONFIG_DIR 指向临时目录、
// UNATTENDED/INIT_TOKEN 清空，返回合并后的配置与判定结果。
func bootstrapEnv(t *testing.T, files map[string]string, env map[string]string) (*Config, *BootstrapOutcome, error) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withConfigDir(t, dir)
	for _, k := range []string{"UNATTENDED", "INIT_TOKEN", "UI_PORT", "DB_DSN"} {
		_ = os.Unsetenv(k)
	}
	for k, v := range env {
		_ = os.Setenv(k, v)
		if v == "" {
			_ = os.Unsetenv(k)
		}
	}
	t.Cleanup(func() {
		for k := range env {
			_ = os.Unsetenv(k)
		}
	})
	cfg := Load()
	out, err := Bootstrap(cfg)
	return cfg, out, err
}

// TestBootstrapNoFile 无配置文件首启：生成模板 + 进入初始化模式。
func TestBootstrapNoFile(t *testing.T) {
	cfg, out, err := bootstrapEnv(t, nil, nil)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if out.Mode != BootModeSetup {
		t.Fatalf("数据库未配置应进入初始化模式: %s", out.Mode)
	}
	if !out.TemplateWritten || cfg.DatabaseConfigured {
		t.Fatalf("应写模板且未配置: %+v", out)
	}
}

// TestBootstrapUnattended UNATTENDED=1：无头正常启动，未配置回落 sqlite。
func TestBootstrapUnattended(t *testing.T) {
	cfg, out, err := bootstrapEnv(t, nil, map[string]string{"UNATTENDED": "1"})
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if out.Mode != BootModeNormal || cfg.DatabaseConfigured || cfg.EffectiveDBDriver() != DriverSQLite {
		t.Fatalf("无头模式判定错误: mode=%s driver=%s", out.Mode, cfg.EffectiveDBDriver())
	}
	if !strings.Contains(cfg.DBDSN, "permaporter.db") {
		t.Fatalf("应回落默认 SQLite DSN: %s", cfg.DBDSN)
	}
}

// TestBootstrapConfigured 数据库已配置：正常启动。
func TestBootstrapConfigured(t *testing.T) {
	file := "[database]\ndriver = \"sqlite\"\ndsn = \"file:./x.db\"\n"
	cfg, out, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, nil)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if out.Mode != BootModeNormal || !cfg.DatabaseConfigured || cfg.DBDSN != "file:./x.db" {
		t.Fatalf("已配置判定错误: mode=%s dsn=%s", out.Mode, cfg.DBDSN)
	}
	if out.TemplateWritten {
		t.Fatal("文件已存在不应写模板")
	}
}

// TestBootstrapParseError 解析失败：返回错误（调用方拒绝启动）。
func TestBootstrapParseError(t *testing.T) {
	_, _, err := bootstrapEnv(t, map[string]string{"permaporter.toml": "broken [ ["}, nil)
	if err == nil {
		t.Fatal("解析失败应返回错误")
	}
	if !strings.Contains(err.Error(), "TOML 解析错误") || !strings.Contains(err.Error(), "permaporter.toml") {
		t.Fatalf("错误应含文件路径与解析错误: %v", err)
	}
}

// TestBootstrapIncompletePG 配置不完整（PG 缺字段）→ 校验失败，绝不静默进入初始化模式。
func TestBootstrapIncompletePG(t *testing.T) {
	file := "[database]\ndriver = \"postgresql\"\nhost = \"h\"\n"
	_, out, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, nil)
	if err == nil {
		t.Fatal("PG 字段缺失应失败")
	}
	var be *BootstrapError
	if !asBootstrapError(err, &be) || !strings.Contains(be.Detail, "必填字段") {
		t.Fatalf("错误形态错误: %v", err)
	}
	if out != nil && out.Mode == BootModeSetup {
		t.Fatal("配置不完整不得进入初始化模式")
	}
}

// TestBootstrapBadDriver driver 非法 → 拒绝。
func TestBootstrapBadDriver(t *testing.T) {
	file := "[database]\ndriver = \"mysql\"\n"
	_, _, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, nil)
	if err == nil || !strings.Contains(err.Error(), "sqlite / postgresql") {
		t.Fatalf("非法 driver 应拒绝: %v", err)
	}
}

// TestBootstrapPortPriorityFromPortConflict 端口互异校验覆盖配置文件取值：
// 文件把两端口写成同值 → Validate 失败（main 在 Bootstrap 后调用 Validate）。
func TestBootstrapPortPriorityFromPortConflict(t *testing.T) {
	file := "[ports]\nui = 9099\ngo = 9099\n[database]\ndriver = \"sqlite\"\n"
	cfg, out, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, nil)
	if err != nil {
		t.Fatalf("判定阶段不应失败: %v", err)
	}
	if out.Mode != BootModeNormal || cfg.UIPort != 9099 || cfg.GoPort != 9099 {
		t.Fatalf("端口合并错误: %+v", cfg)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "互不相同") {
		t.Fatalf("文件端口冲突应被 Validate 拒绝: %v", err)
	}
}

// TestBootstrapUnattendedIncompletePG 无头模式同样做字段完整性校验：
// PG 缺字段 → 拒绝启动（错误指明具体字段，而不是留给连接层报晦涩错误）。
func TestBootstrapUnattendedIncompletePG(t *testing.T) {
	file := "[database]\ndriver = \"postgresql\"\nhost = \"h\"\n"
	_, _, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, map[string]string{"UNATTENDED": "1"})
	if err == nil || !strings.Contains(err.Error(), "必填字段") {
		t.Fatalf("无头模式 PG 缺字段应拒绝并指明字段: %v", err)
	}
}

// TestBootstrapFileOverridesEnv 文件 > 环境变量。
func TestBootstrapFileOverridesEnv(t *testing.T) {
	file := "[ports]\nui = 10090\n[database]\ndriver = \"sqlite\"\ndsn = \"file:./f.db\"\n"
	cfg, _, err := bootstrapEnv(t, map[string]string{"permaporter.toml": file}, map[string]string{"UI_PORT": "8090", "DB_DSN": "file:./env.db"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UIPort != 10090 || cfg.DBDSN != "file:./f.db" {
		t.Fatalf("文件应覆盖环境变量: ui=%d dsn=%s", cfg.UIPort, cfg.DBDSN)
	}
}

// asBootstrapError 辅助类型断言。
func asBootstrapError(err error, target **BootstrapError) bool {
	if e, ok := err.(*BootstrapError); ok {
		*target = e
		return true
	}
	return false
}
