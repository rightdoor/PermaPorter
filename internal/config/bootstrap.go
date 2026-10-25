// 启动判定顺序（严格按一.4 实现，收敛在此便于单测全分支）：
//
//  1. 读取配置文件：不存在 → 生成模板并记 INFO；解析失败 → ParseError（拒绝启动）；
//  2. UNATTENDED=1 → 无头正常启动（数据库未配置回落默认 SQLite）；
//  3. 数据库已配置 → 正常启动（字段完整性/端口互异/连通性校验失败 → FatalError）；
//  4. 数据库未配置 → 初始化模式（仅监听 9090，合法状态而非错误）。
//
// "配置不完整"（driver 已填但必填字段缺失）属于第 3 步的校验失败，
// 绝不静默进入初始化模式——两者必须可区分。
package config

import (
	"fmt"
	"log"
)

// BootMode 启动模式。
type BootMode string

const (
	// BootModeNormal 正常启动：五端口全部监听。
	BootModeNormal BootMode = "normal"
	// BootModeSetup 未初始化模式：仅监听 9090（SPA + 向导 API），不连接数据库。
	BootModeSetup BootMode = "setup"
)

// BootstrapError 启动判定失败：进程必须以非零码退出、不监听任何端口。
type BootstrapError struct {
	Item   string // 出错环节（配置文件解析 / 数据库校验 / …）
	Detail string // 具体原因与修复建议
}

func (e *BootstrapError) Error() string { return e.Item + "失败: " + e.Detail }

// BootstrapOutcome 启动判定结果。
type BootstrapOutcome struct {
	Mode            BootMode
	ConfigFilePath  string // 配置文件路径（日志与向导展示）
	TemplateWritten bool   // 本次启动是否新生成了模板文件
}

// Bootstrap 执行启动判定：确保配置文件存在 → 解析 → 合并（文件 > 环境变量 > 默认值）
// → 确定启动模式。cfg 传入 Load() 的结果（环境变量层），函数原地叠加文件层。
func Bootstrap(cfg *Config) (*BootstrapOutcome, error) {
	// ① 配置文件：目录缺失按默认路径创建；文件缺失写模板（端口预填当前生效值）
	path, templateWritten, err := EnsureConfigFile(PortsConfig{
		UI: cfg.UIPort, NPM: cfg.NPMPort, Go: cfg.GoPort, Py: cfg.PyPort, MVN: cfg.MVNPort,
	})
	cfg.ConfigPath = path
	out := &BootstrapOutcome{ConfigFilePath: path, TemplateWritten: templateWritten}
	if templateWritten {
		log.Printf("[config] 配置文件不存在，已生成模板: %s（[database] 未配置时进入初始化模式，浏览器打开控制台完成初始化）", path)
	}
	if err != nil {
		return nil, &BootstrapError{Item: "配置文件写入",
			Detail: fmt.Sprintf("%s（CONFIG_DIR=%s 为相对路径时按进程工作目录解析，运行用户须对该目录可写；systemd 部署检查 WorkingDirectory 属主，或改用绝对路径 CONFIG_DIR）", err.Error(), ConfigDir())}
	}

	// ② 解析：TOML 格式错误 → 拒绝启动（输出文件路径与具体解析错误）
	fc, err := LoadConfigFile(path)
	if err != nil {
		return nil, &BootstrapError{Item: "配置文件解析",
			Detail: fmt.Sprintf("%s: %v（修正 TOML 语法后重启；模板形态见文档 configuration.md）", path, err)}
	}

	// ③ 合并优先级：文件 > 环境变量 > 默认值
	ApplyFileConfig(cfg, fc)

	// ④ 数据库已配置时的字段完整性校验（无头与正常启动共用：无头只是跳过向导判定，
	// 不是跳过校验——缺字段应在启动期给出具体字段名，而不是留给连接层报晦涩错误）
	if cfg.DatabaseConfigured {
		if err := cfg.Database.Validate(); err != nil {
			return nil, &BootstrapError{Item: "数据库校验", Detail: err.Error()}
		}
	}

	// ⑤ 无头模式：CI/测试/脚本，数据库未配置时回落默认 SQLite DSN（现状行为）
	if cfg.UnattendedMode() {
		if !cfg.DatabaseConfigured {
			log.Printf("[config] 无头模式（UNATTENDED=1）：数据库未配置，回落默认 SQLite（%s）", DriverSQLite)
		} else {
			log.Printf("[config] 无头模式（UNATTENDED=1）：数据库类型 %s", cfg.EffectiveDBDriver())
		}
		out.Mode = BootModeNormal
		return out, nil
	}

	if cfg.DatabaseConfigured {
		out.Mode = BootModeNormal
		return out, nil
	}

	// ⑥ 数据库未配置：初始化模式（合法状态，非错误；醒目 INFO 提示）
	log.Printf("[config] 数据库未配置（%s 的 [database].driver 为空），进入初始化模式：仅监听 %d，请通过浏览器完成初始化向导",
		path, cfg.UIPort)
	out.Mode = BootModeSetup
	return out, nil
}
