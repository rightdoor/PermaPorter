package models

// Version 版本明细（versions 表）。
// 各 CachedAt/Size 为 0 表示对应产物尚未缓存；产物一旦落盘永久保留，
// 只有管理员删除或文件丢失（清理任务复位状态）才会变化。
type Version struct {
	ID int64
	// ModuleID 归属模块 ID
	ModuleID     int64
	Version      string // 规范版本（@v/list 中的版本或伪版本）
	InfoTime     string // .info 中的发布时间（RFC3339，可空）
	InfoCachedAt string
	ModCachedAt  string
	ZipCachedAt  string
	ZipSizeBytes int64
	ModSizeBytes int64
	ZipPath      string // zip 相对存储目录的路径（/ 分隔）
	Ziphash      string // 本地计算的 h1: 校验和
	// ModContent go.mod 原文（上限 16MB）
	ModContent string
	// RequirementsJSON require 列表：[{"path":"...","version":"...","indirect":false}]
	RequirementsJSON string
	GoVersion        string // go.mod 的 go 指令版本
	ListedAt         string // 出现在 @v/list 中的时间
	IsPseudo         bool   // 伪版本（vX.Y.Z-0.yyyymmddhhmmss-hash 形式）
}
