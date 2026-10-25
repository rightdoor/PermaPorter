package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/utils"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/models"
)

// errDetailQuery 详情数据依赖的查询失败（区别于"包不存在"，应返回 500 而非 404）。
var errDetailQuery = errors.New("详情数据查询失败")

// 公开 JSON API：SPA 公开端数据源。

const homePageSize = 24

// dtoPkgRow 包列表行（探索包 / 后台列表共用形态）。
type dtoPkgRow struct {
	PkgName       string `json:"pkgName"`
	Description   string `json:"description"`
	LatestVersion string `json:"latestVersion"`
	VersionCount  int64  `json:"versionCount"`
	TotalSize     int64  `json:"totalSize"`
	LastCached    string `json:"lastCached"`
}

func toPkgRow(r database.PkgRow) dtoPkgRow {
	return dtoPkgRow{
		PkgName:       r.PkgName,
		Description:   r.Description,
		LatestVersion: r.LatestVersion,
		VersionCount:  r.VersionCount,
		TotalSize:     r.TotalSize,
		LastCached:    r.LastCached,
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

// Summary 首页 hero 统计。
func (a *App) Summary(c *gin.Context) {
	pc, vc, size, hit := a.cachedStats()
	okMsg(c, gin.H{
		"pkgCount":  pc,
		"verCount":  vc,
		"totalSize": size,
		"hitRate":   hit,
	})
}

// PackagesAPI 探索包列表：已缓存包分页（24/页，名称模糊过滤）。
func (a *App) PackagesAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countPackagesCached(q)
	totalPages := int((total + homePageSize - 1) / homePageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listPackagesCached(q, (page-1)*homePageSize, homePageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rows := make([]dtoPkgRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toPkgRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// dtoVersion 版本行（公开详情/后台版本列表共用）。
type dtoVersion struct {
	ID            int64  `json:"id"`
	Version       string `json:"version"`
	FileSizeBytes int64  `json:"fileSizeBytes"`
	PublishedAt   string `json:"publishedAt"`
	CachedAt      string `json:"cachedAt"`
}

// detailVersionLimit 未缓存版本最多返回的条数（已缓存版本总是全部返回）。
// @types/node 这类包有上千个版本，全量返回会把响应体撑到几 MB。
const detailVersionLimit = 200

// PackageDetailAPI 包详情：元数据、README、版本列表、当前版本依赖与缓存统计。
// 重活（README 渲染 + 版本表）整体缓存 30s，登录态相关的 showRefresh 在响应时注入。
func (a *App) PackageDetailAPI(c *gin.Context) {
	pkgName := strings.TrimPrefix(c.Param("rest"), "/")
	if pkgName == "" {
		failMsg(c, http.StatusNotFound, "包不存在")
		return
	}
	ver := c.Query("v")
	key := "npm:detail:" + pkgName + "|" + ver
	cached, ok := a.DetailCache.Get(key, cacheData)
	if !ok {
		view, err := a.buildPackageDetail(pkgName, ver, a.protocolBase(c))
		if err != nil {
			if errors.Is(err, errDetailQuery) {
				failMsg(c, http.StatusInternalServerError, "查询失败")
			} else {
				failMsg(c, http.StatusNotFound, "包不存在")
			}
			return
		}
		cached = view
		a.DetailCache.Set(key, view, cacheData, viewCacheTTL)
	}
	// 浅拷贝后注入登录态：缓存体本身与请求方身份无关
	payload := cached.(gin.H)
	out := make(gin.H, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	out["showRefresh"] = a.IsAuthed(c)
	okMsg(c, out)
}

// buildPackageDetail 组装包详情响应体（不含登录态字段）。
func (a *App) buildPackageDetail(pkgName, ver, protoBase string) (gin.H, error) {
	pkg, err := a.DB.GetPackage(pkgName)
	if err != nil {
		return nil, err
	}
	// 版本表只取轻量字段：不再为上千个版本读取 dependencies_json。
	// 查询失败必须报错：空版本表会被当真数据缓存 30s。
	items, err := a.DB.ListVersionItems(pkg.ID)
	if err != nil {
		return nil, errDetailQuery
	}
	if ver == "" {
		ver = defaultVersionItem(items, pkg.LatestVersion)
	}

	rows := make([]dtoVersion, 0, len(items))
	cachedCount, uncachedShown := 0, 0
	var cachedSize int64
	for _, v := range items {
		if v.FileSizeBytes > 0 {
			cachedCount++
			cachedSize += v.FileSizeBytes
		} else {
			if uncachedShown >= detailVersionLimit {
				continue
			}
			uncachedShown++
		}
		rows = append(rows, dtoVersion{
			ID:            v.ID,
			Version:       v.Version,
			FileSizeBytes: v.FileSizeBytes,
			PublishedAt:   v.PublishedAt,
			CachedAt:      v.CachedAt,
		})
	}

	curVersion, curSize, curPublished, curCached := "", int64(0), "", ""
	for _, v := range items {
		if v.Version == ver {
			curVersion, curSize, curPublished, curCached = v.Version, v.FileSizeBytes, v.PublishedAt, v.CachedAt
			break
		}
	}
	if curVersion == "" && len(items) > 0 {
		curVersion, curSize, curPublished, curCached = items[0].Version, items[0].FileSizeBytes, items[0].PublishedAt, items[0].CachedAt
	}

	// 当前版本的依赖按需单独查询（一行），避免为全部版本读取依赖大字段
	var cur *models.Version
	if curVersion != "" {
		cur, _ = a.DB.GetVersionByName(pkgName, curVersion)
	}
	if cur == nil {
		cur = &models.Version{}
	}

	downloadURL := ""
	if curSize > 0 && curVersion != "" {
		base := pkgName
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		// 下载链接由服务端注入 npm Registry 协议端口（9091）绝对地址
		downloadURL = protoBase + "/" + pkgName + "/-/" + base + "-" + curVersion + ".tgz"
	}

	return gin.H{
		"pkg": gin.H{
			"pkgName":        pkg.PkgName,
			"description":    pkg.Description,
			"latestVersion":  pkg.LatestVersion,
			"license":        pkg.License,
			"homepageURL":    pkg.HomepageURL,
			"repositoryURL":  pkg.RepositoryURL,
			"metaCachedAt":   pkg.MetaCachedAt,
			"lastAccessedAt": pkg.LastAccessedAt,
		},
		"readmeHTML": string(utils.RenderMarkdown(pkg.ReadmeContent)),
		"versions":   rows,
		// versionTotal 为真实版本总数：versions 可能被截断（未缓存版本只保留最近的若干个）
		"versionTotal":      len(items),
		"versionsTruncated": len(rows) < len(items),
		"current": gin.H{
			"version":       curVersion,
			"fileSizeBytes": curSize,
			"publishedAt":   curPublished,
			"cachedAt":      curCached,
			"deps":          ParseDeps(cur.DependenciesJSON),
			"devDeps":       ParseDeps(cur.DevDependenciesJSON),
			"peerDeps":      ParseDeps(cur.PeerDependenciesJSON),
		},
		"cachedCount": cachedCount,
		"cachedSize":  cachedSize,
		"downloadURL": downloadURL,
	}, nil
}

// SearchAPI 与搜索页已随「独立搜索页下线」移除：列表页用工具栏直搜（PackagesAPI 的 q 参数），
// 不再向上游 /-/v1/search 汇总候选。

// defaultVersionItem 返回默认查看版本：优先已缓存版本中的最新版本，否则最新元数据版本。
// items 已按版本号降序排列。
func defaultVersionItem(items []database.VersionItem, latest string) string {
	for _, v := range items {
		if v.FileSizeBytes > 0 {
			return v.Version
		}
	}
	if latest != "" {
		return latest
	}
	if len(items) > 0 {
		return items[0].Version
	}
	return ""
}
