package models

import "time"

// Settings 生态配置（单库 ecosystem_settings 表，ecosystem='npm' 行）。
type Settings struct {
	UpstreamURL   string        // 主上游源
	FallbackURL   string        // 备用上游源（npm 侧写空串，暂无回退链语义）
	OutboundProxy string        // 出站代理地址（http://、https://、socks5://，空为直连）
	StorageDir    string        // 存储路径
	MetaTTL       time.Duration // 元数据缓存刷新间隔
}
