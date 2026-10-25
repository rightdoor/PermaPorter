package models

import "time"

// Settings 生态配置（单库 ecosystem_settings 表，ecosystem='go' 行）。
type Settings struct {
	UpstreamURL   string        // 主上游 GOPROXY
	FallbackURL   string        // 备用上游 GOPROXY（主上游网络错误/5xx/403 时切换；404/410 不切换）
	OutboundProxy string        // 出站代理地址（http://、https://、socks5://，空为直连）
	StorageDir    string        // 模块产物存储目录
	MetaTTL       time.Duration // 元数据缓存刷新间隔
}
