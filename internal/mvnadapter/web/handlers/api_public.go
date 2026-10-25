package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/mvnadapter/database"
)

// 公开 JSON API：SPA 公开端数据源。

const homePageSize = 24

// dtoArtifactRow 构件列表行（JAVA 页 / 后台列表共用形态）。
type dtoArtifactRow struct {
	Name             string `json:"name"` // group:artifact
	GroupID          string `json:"groupId"`
	ArtifactID       string `json:"artifactId"`
	LatestVersion    string `json:"latestVersion"`
	VersionCount     int64  `json:"versionCount"`
	FileCount        int64  `json:"fileCount"`
	CachedFileCount  int64  `json:"cachedFileCount"`
	TotalSize        int64  `json:"totalSize"`
	LastCached       string `json:"lastCached"`
}

func toArtifactRow(r database.ArtifactRow) dtoArtifactRow {
	return dtoArtifactRow{
		Name:            database.ArtifactKey(r.GroupID, r.ArtifactID),
		GroupID:         r.GroupID,
		ArtifactID:      r.ArtifactID,
		LatestVersion:   firstNonEmptyStr(r.ReleaseVersion, r.LatestVersion),
		VersionCount:    r.VersionCount,
		FileCount:       r.FileCount,
		CachedFileCount: r.CachedFileCount,
		TotalSize:       r.CachedTotalSize,
		LastCached:      r.LastCachedAt,
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// State SPA 启动状态：初始化/登录状态与版本号（路由守卫与导航共用）。
func (a *App) State(c *gin.Context) {
	okMsg(c, gin.H{
		"initialized": a.DB.IsInitialized(),
		"authed":      a.IsAuthed(c),
		"version":     config.Version,
	})
}

// Summary 首页 hero 统计（mvn 段）。
func (a *App) Summary(c *gin.Context) {
	ac, fc, size, hit := a.cachedStats()
	okMsg(c, gin.H{
		"artifactCount": ac,
		"fileCount":     fc,
		"totalSize":     size,
		"hitRate":       hit,
	})
}

// MvnArtifactsAPI JAVA 页列表：已收录构件分页（24/页，groupId/artifactId 模糊过滤）。
func (a *App) MvnArtifactsAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countArtifactsCached(q)
	totalPages := int((total + homePageSize - 1) / homePageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listArtifactsSortedCached(q, "", "", (page-1)*homePageSize, homePageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rows := make([]dtoArtifactRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toArtifactRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// dtoMvnFile 文件行（公开详情/后台文件列表共用形态）。
type dtoMvnFile struct {
	ID            int64  `json:"id"`
	RepoPath      string `json:"repoPath"`
	Version       string `json:"version"`
	Filename      string `json:"filename"`
	FileSizeBytes int64  `json:"fileSizeBytes"`
	Cached        bool   `json:"cached"`
	SHA1          string `json:"sha1"`
	CachedAt      string `json:"cachedAt"`
	DownloadURL   string `json:"downloadURL,omitempty"`
}

// MvnArtifactDetailAPI 构件详情：文件清单与缓存状态。
func (a *App) MvnArtifactDetailAPI(c *gin.Context) {
	coords := strings.TrimPrefix(c.Param("rest"), "/")
	groupID, artifactID, ok := validCoords(coords)
	if !ok {
		failMsg(c, http.StatusNotFound, "构件不存在")
		return
	}
	art, err := a.DB.GetArtifact(groupID, artifactID)
	if err != nil {
		failMsg(c, http.StatusNotFound, "构件不存在")
		return
	}
	files, err := a.DB.ListArtifactFiles(art.ID)
	if err != nil {
		// 查询失败必须报错：空文件清单会被当真数据渲染为"0 文件"
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	base := a.protocolBase(c)
	rows := make([]dtoMvnFile, 0, len(files))
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
			dl = base + "/" + f.RepoPath
		}
		rows = append(rows, dtoMvnFile{
			ID:            f.ID,
			RepoPath:      f.RepoPath,
			Version:       f.Version,
			Filename:      f.Filename,
			FileSizeBytes: f.FileSizeBytes,
			Cached:        cached,
			SHA1:          f.SHA1,
			CachedAt:      f.CachedAt,
			DownloadURL:   dl,
		})
	}
	okMsg(c, gin.H{
		"artifact": gin.H{
			"name":             database.ArtifactKey(groupID, artifactID),
			"groupId":          art.GroupID,
			"artifactId":       art.ArtifactID,
			"latestVersion":    firstNonEmptyStr(art.ReleaseVersion, art.LatestVersion),
			"latestVersionRaw": art.LatestVersion,
			"versionCount":     art.VersionCount,
			"lastUpdated":      art.LastUpdated,
			"metaCachedAt":     art.MetaCachedAt,
			"lastAccessedAt":   art.LastAccessedAt,
			"fileCount":        art.FileCount,
			"cachedFileCount":  art.CachedFileCount,
			"cachedTotalSize":  art.CachedTotalSize,
		},
		"files":       rows,
		"cachedCount": cachedCount,
		"cachedSize":  cachedSize,
		"showRefresh": a.IsAuthed(c),
	})
}
