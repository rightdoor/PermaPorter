package models

import "time"

// Settings 生态配置（单库 ecosystem_settings 表，ecosystem='py' 行）。
type Settings struct {
	UpstreamURL   string        // 主上游 PyPI Simple 根
	FallbackURL   string        // 备用上游（主上游网络错误/5xx/403 时切换；404/410 不切换）
	OutboundProxy string        // 出站代理地址（http://、https://、socks5://，空为直连）
	StorageDir    string        // 存储路径
	MetaTTL       time.Duration // simple 锚点页缓存刷新间隔
}
