package models

// Version 版本明细（versions 表）。
// FileSizeBytes 为 0 表示 tarball 尚未下载缓存。
type Version struct {
	ID                   int64
	PkgID                int64
	Version              string
	TarballPath          string // 相对于存储目录的磁盘路径
	TarballURL           string // 上游 tarball 原始地址
	FileSizeBytes        int64
	DependenciesJSON     string
	DevDependenciesJSON  string
	PeerDependenciesJSON string
	Integrity            string // 上游 dist.integrity（如 sha512-xxx），用于下载后校验
	PublishedAt          string
	CachedAt             string
}
