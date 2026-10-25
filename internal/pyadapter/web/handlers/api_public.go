package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/pyadapter/database"
)

// 公开 JSON API：SPA 公开端数据源。

const homePageSize = 24

// dtoProjectRow 项目列表行（PYTHON 页 / 后台列表共用形态）。
type dtoProjectRow struct {
	ProjectName     string `json:"projectName"`
	DisplayName     string `json:"displayName"`
	LatestVersion   string `json:"latestVersion"`
	Summary         string `json:"summary"`
	FileCount       int64  `json:"fileCount"`
	CachedFileCount int64  `json:"cachedFileCount"`
	TotalSize       int64  `json:"totalSize"`
	LastCached      string `json:"lastCached"`
}

func toProjectRow(r database.ProjectRow) dtoProjectRow {
	return dtoProjectRow{
		ProjectName:     r.ProjectName,
		DisplayName:     r.DisplayName,
		LatestVersion:   r.LatestVersion,
		Summary:         r.Summary,
		FileCount:       r.FileCount,
		CachedFileCount: r.CachedFileCount,
		TotalSize:       r.CachedTotalSize,
		LastCached:      r.LastCachedAt,
	}
}

// State SPA 启动状态：初始化/登录状态与版本号（路由守卫与导航共用）。
func (a *App) State(c *gin.Context) {
	okMsg(c, gin.H{
		"initialized": a.DB.IsInitialized(),
		"authed":      a.IsAuthed(c),
		"version":     config.Version,
	})
}

// Summary 首页 hero 统计（py 段）。
func (a *App) Summary(c *gin.Context) {
	pc, fc, size, hit := a.cachedStats()
	okMsg(c, gin.H{
		"projectCount": pc,
		"fileCount":    fc,
		"totalSize":    size,
		"hitRate":      hit,
	})
}

// PyProjectsAPI PYTHON 页列表：已收录项目分页（24/页，名称/摘要模糊过滤）。
func (a *App) PyProjectsAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countProjectsCached(q)
	totalPages := int((total + homePageSize - 1) / homePageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listProjectsCached(q, (page-1)*homePageSize, homePageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rows := make([]dtoProjectRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toProjectRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// dtoPyFile 版本行（公开详情/后台文件列表共用形态）。
type dtoPyFile struct {
	ID             int64  `json:"id"`
	Version        string `json:"version"`
	Filename       string `json:"filename"`
	FileSizeBytes  int64  `json:"fileSizeBytes"`
	Cached         bool   `json:"cached"`
	SHA256         string `json:"sha256"`
	RequiresPython string `json:"requiresPython"`
	UploadTime     string `json:"uploadTime"`
	CachedAt       string `json:"cachedAt"`
	DownloadURL    string `json:"downloadURL,omitempty"`
}

// PyProjectDetailAPI 项目详情：文件清单与缓存状态。
func (a *App) PyProjectDetailAPI(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("rest"), "/")
	if !validProjectName(name) {
		failMsg(c, http.StatusNotFound, "项目不存在")
		return
	}
	proj, err := a.DB.GetProject(name)
	if err != nil {
		failMsg(c, http.StatusNotFound, "项目不存在")
		return
	}
	files, err := a.DB.ListProjectFiles(proj.ID)
	if err != nil {
		// 查询失败必须报错：空文件清单会被当真数据渲染为"0 文件"
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	base := a.protocolBase(c)
	rows := make([]dtoPyFile, 0, len(files))
	cachedCount := 0
	var cachedSize int64
	for _, f := range files {
		cached := f.FileSizeBytes > 0
		if cached {
			cachedCount++
			cachedSize += f.FileSizeBytes
		}
		dl := ""
		if cached {
			dl = base + "/packages/" + f.Filename
		}
		rows = append(rows, dtoPyFile{
			ID:             f.ID,
			Version:        f.Version,
			Filename:       f.Filename,
			FileSizeBytes:  f.FileSizeBytes,
			Cached:         cached,
			SHA256:         f.SHA256,
			RequiresPython: f.RequiresPython,
			UploadTime:     f.UploadTime,
			CachedAt:       f.CachedAt,
			DownloadURL:    dl,
		})
	}
	displayName := proj.DisplayName
	if displayName == "" {
		displayName = proj.ProjectName
	}
	okMsg(c, gin.H{
		"project": gin.H{
			"projectName":     proj.ProjectName,
			"displayName":     displayName,
			"latestVersion":   proj.LatestVersion,
			"summary":         proj.Summary,
			"metaCachedAt":    proj.MetaCachedAt,
			"lastAccessedAt":  proj.LastAccessedAt,
			"fileCount":       proj.FileCount,
			"cachedFileCount": proj.CachedFileCount,
			"cachedTotalSize": proj.CachedTotalSize,
		},
		"files":       rows,
		"cachedCount": cachedCount,
		"cachedSize":  cachedSize,
		"showRefresh": a.IsAuthed(c),
	})
}
