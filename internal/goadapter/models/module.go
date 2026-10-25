package models

// Module 模块索引（modules 表）。
type Module struct {
	ID             int64
	ModulePath     string // 规范模块路径（大写字母原样保留，非 GOPROXY 编码形式）
	LatestVersion  string // 最新版本（优先 @latest，其次 @v/list 推导）
	LatestTime     string // 最新版本发布时间（RFC3339）
	MetaCachedAt   string // @v/list 最近刷新时间
	LatestCachedAt string // @latest 最近刷新时间
	ListETag       string // 上游 @v/list 的 ETag（条件刷新）
	ReadmeContent  string // 从 zip 提取的 README 原文
	ReadmeVersion  string // README 对应的 zip 版本
	Summary        string // README 纯文本摘要（列表/搜索展示）
	LastAccessedAt string
	// 物化统计列：由 zip 落盘、删除、清理任务维护
	VersionCount       int64 // 已知版本总数（versions 行数）
	CachedVersionCount int64 // zip 已缓存版本数
	CachedTotalSize    int64 // zip 总字节
	LastCachedAt       string
}
