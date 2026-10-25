package models

// Project py 项目（单库 py_projects 表，PEP 503 规范化名）。
type Project struct {
	ID              int64
	ProjectName     string // PEP 503 规范化名（URL 与唯一键口径）
	DisplayName     string
	LatestVersion   string
	Summary         string
	MetaCachedAt    string
	MetaETag        string
	LastAccessedAt  string
	FileCount       int64
	CachedFileCount int64
	CachedTotalSize int64
	LastCachedAt    string
}

// File py 发行文件（单库 py_files 表）。file_size_bytes > 0 表示本体已缓存。
type File struct {
	ID             int64
	ProjectID      int64
	Version        string
	Filename       string
	FileSizeBytes  int64
	SHA256         string
	FilePath       string // 相对存储目录的路径（/ 分隔）
	SourceURL      string // 上游锚点页给出的原始下载地址（未命中时回源用）
	RequiresPython string
	UploadTime     string
	CachedAt       string
}
