package models

// Package 包索引（packages 表）。
type Package struct {
	ID             int64
	PkgName        string
	Description    string
	LatestVersion  string
	RepositoryURL  string
	HomepageURL    string
	License        string
	ReadmeContent  string // README Markdown 原文
	MetaCachedAt   string
	MetaETag       string // 上游 packument 的 ETag（用于条件刷新，避免重复下载）
	LastAccessedAt string
}
