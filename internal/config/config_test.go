package config

import (
	"runtime"
	"testing"
)

// validCfg 返回一份通过校验的基准配置。
func validCfg() *Config {
	return &Config{
		UIPort: 9090, NPMPort: 9091, GoPort: 9092, PyPort: 9093, MVNPort: 9094,
		DBDSN:        "file:./data/permaporter.db",
		GoStorageDir: "./data/storage-go", NPMStorageDir: "./data/storage-npm", PyStorageDir: "./data/storage-py", MVNStorageDir: "./data/storage-mvn",
	}
}

// TestValidateOk 基准配置应通过。
func TestValidateOk(t *testing.T) {
	if err := validCfg().Validate(); err != nil {
		t.Fatalf("基准配置不应失败: %v", err)
	}
}

// TestValidateDuplicatePort 端口重复必须拒绝启动（含 PY_PORT / MVN_PORT）。
func TestValidateDuplicatePort(t *testing.T) {
	for _, m := range []func(*Config){
		func(c *Config) { c.NPMPort = c.UIPort },
		func(c *Config) { c.GoPort = c.UIPort },
		func(c *Config) { c.GoPort = c.NPMPort },
		func(c *Config) { c.PyPort = c.UIPort },
		func(c *Config) { c.PyPort = c.NPMPort },
		func(c *Config) { c.PyPort = c.GoPort },
		func(c *Config) { c.MVNPort = c.UIPort },
		func(c *Config) { c.MVNPort = c.NPMPort },
		func(c *Config) { c.MVNPort = c.GoPort },
		func(c *Config) { c.MVNPort = c.PyPort },
	} {
		c := validCfg()
		m(c)
		if err := c.Validate(); err == nil {
			t.Fatalf("端口重复应校验失败: %+v", c)
		}
	}
}

// TestValidateSameStorage 存储目录相同必须拒绝启动（四棵树两两互异）。
func TestValidateSameStorage(t *testing.T) {
	for _, m := range []func(*Config){
		func(c *Config) { c.NPMStorageDir = c.GoStorageDir },
		func(c *Config) { c.PyStorageDir = c.GoStorageDir },
		func(c *Config) { c.PyStorageDir = c.NPMStorageDir },
		func(c *Config) { c.MVNStorageDir = c.GoStorageDir },
		func(c *Config) { c.MVNStorageDir = c.NPMStorageDir },
		func(c *Config) { c.MVNStorageDir = c.PyStorageDir },
	} {
		c := validCfg()
		m(c)
		if err := c.Validate(); err == nil {
			t.Fatal("存储目录相同应校验失败")
		}
	}
	// Windows 语义：大小写不同也应视为相同（Linux 路径大小写敏感，仅 Windows 断言）
	if runtime.GOOS == "windows" {
		c := validCfg()
		c.NPMStorageDir = "./DATA/STORAGE-GO"
		if err := c.Validate(); err == nil {
			t.Fatal("Windows 下存储目录仅大小写不同应校验失败")
		}
	}
}

// TestValidateNestedStorage 存储目录互为父子必须拒绝启动。
func TestValidateNestedStorage(t *testing.T) {
	for _, pair := range [][2]string{
		{"./data/storage", "./data/storage-npm-sub"}, // 兄弟目录：应通过
	} {
		c := validCfg()
		c.GoStorageDir, c.NPMStorageDir = pair[0], pair[1]
		if err := c.Validate(); err != nil {
			t.Fatalf("兄弟目录 %v 不应失败: %v", pair, err)
		}
	}
	for _, pair := range [][2]string{
		{"./data/storage", "./data/storage/npm"},
		{"./data/storage/npm", "./data/storage"},
		{"./data/storage-py", "./data/storage-py/sub"},
		{"./data/storage-mvn", "./data/storage-mvn/sub"},
	} {
		c := validCfg()
		c.GoStorageDir, c.NPMStorageDir = pair[0], pair[1]
		if err := c.Validate(); err == nil {
			t.Fatalf("嵌套目录 %v 应校验失败", pair)
		}
	}
}
