package models

import "time"

// Settings 生态配置（单库 ecosystem_settings 表，ecosystem='mvn' 行）。
type Settings struct {
	UpstreamURL   string        // 主上游 Maven 仓库根（如 https://repo.maven.apache.org/maven2）
	FallbackURL   string        // 备用上游（主上游网络错误/5xx/403 时切换；404/410 不切换）
	OutboundProxy string        // 出站代理地址（http://、https://、socks5://，空为直连）
	StorageDir    string        // 存储路径
	MetaTTL       time.Duration // maven-metadata.xml 缓存刷新间隔
}
