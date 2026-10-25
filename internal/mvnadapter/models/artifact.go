// Package models 定义 mvn 生态的领域模型（单库 mvn_* 前缀表）。
package models

// Artifact mvn 构件（group:artifact，mvn_artifacts 表）。
// 行由构件级 maven-metadata.xml 解析（XML 内 groupId/artifactId）或首个构件文件
// 下载成功时按需创建；固定版本构建不请求 metadata 也会出现。
type Artifact struct {
	ID              int64
	GroupID         string // Maven groupId（点分，如 com.google.guava）
	ArtifactID      string // Maven artifactId（如 guava）
	LatestVersion   string // 构件级 metadata 的 <latest>（无 metadata 时为空）
	ReleaseVersion  string // 构件级 metadata 的 <release>
	VersionCount    int64  // 构件级 metadata 的 <versions> 数量
	LastUpdated     string // 构件级 metadata 的 <lastUpdated>（yyyyMMddHHmmss）
	MetaCachedAt    string // 最近一次构件级 metadata 成功刷新（TTL 判定口径）
	LastAccessedAt  string
	FileCount       int64
	CachedFileCount int64
	CachedTotalSize int64
	LastCachedAt    string
}

// MetaEntry 两级 maven-metadata.xml 的 raw 缓存（mvn_metadata 表，按仓库相对路径）。
// XML 不含绝对地址，原样透传即协议安全，不做重生成。
type MetaEntry struct {
	ID       int64
	RepoPath string // 仓库相对路径（/ 分隔），如 com/google/guava/guava/maven-metadata.xml
	XML      string
	ETag     string
	CachedAt string
}

// File mvn 构件文件（mvn_files 表：jar/pom/sources/module/asc 等）。
// file_size_bytes > 0 表示本体已缓存（0 = 清理复位态，记录保留待重新回源）。
type File struct {
	ID            int64
	ArtifactID    int64
	RepoPath      string // 仓库相对路径（/ 分隔，也是全局唯一键）
	Version       string
	Filename      string
	FileSizeBytes int64
	SHA1          string // 下载时边下边算的 sha1（内容校验与 .sha1 端点共用）
	FilePath      string // 相对存储目录的落盘路径（/ 分隔，镜像仓库布局）
	CachedAt      string
}
