package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/utils"
	"permaporter/internal/goadapter/database"
	"permaporter/internal/goadapter/proxy"
)

// 公开 JSON API：SPA 公开端数据源。

const homePageSize = 24

// dtoModuleRow 模块列表行（模块库 / 后台列表共用形态）。
type dtoModuleRow struct {
	ModulePath    string `json:"modulePath"`
	Summary       string `json:"summary"`
	LatestVersion string `json:"latestVersion"`
	VersionCount  int64  `json:"versionCount"`
	TotalSize     int64  `json:"totalSize"`
	LastCached    string `json:"lastCached"`
}

func toModuleRow(r database.ModuleRow) dtoModuleRow {
	return dtoModuleRow{
		ModulePath:    r.ModulePath,
		Summary:       r.Summary,
		LatestVersion: r.LatestVersion,
		VersionCount:  r.VersionCount,
		TotalSize:     r.CachedTotalSize,
		LastCached:    r.LastCachedAt,
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
	mc, vc, size, hit := a.cachedStats()
	okMsg(c, gin.H{
		"moduleCount": mc,
		"verCount":    vc,
		"totalSize":   size,
		"hitRate":     hit,
	})
}

// ModulesAPI 模块库列表：已收录模块分页（24/页，路径/摘要模糊过滤）。
func (a *App) ModulesAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countModulesCached(q)
	totalPages := int((total + homePageSize - 1) / homePageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listModulesCached(q, (page-1)*homePageSize, homePageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败")
		return
	}
	rows := make([]dtoModuleRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toModuleRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// dtoVersion 版本行（公开详情/后台版本列表共用）。
type dtoVersion struct {
	ID           int64  `json:"id"`
	Version      string `json:"version"`
	Cached       bool   `json:"cached"`
	ZipSizeBytes int64  `json:"zipSizeBytes"`
	ModSizeBytes int64  `json:"modSizeBytes"`
	PublishedAt  string `json:"publishedAt"`
	CachedAt     string `json:"cachedAt"`
	Pseudo       bool   `json:"pseudo"`
}

// detailVersionLimit 未缓存版本最多返回的条数（已缓存版本总是全部返回）。
// 超大模块（如 k8s.io/kubernetes）有上千个版本，全量返回会把响应体撑到几 MB。
const detailVersionLimit = 200

// ModuleDetailAPI 模块详情：元数据、go.mod 源码、README、版本列表、当前版本依赖与缓存统计。
// 重活（README 渲染 + 版本表）整体缓存 30s，登录态相关的 showRefresh 在响应时注入。
func (a *App) ModuleDetailAPI(c *gin.Context) {
	modulePath := strings.TrimPrefix(c.Param("rest"), "/")
	if modulePath == "" || proxy.ValidateModuleInput(modulePath) != nil {
		failMsg(c, http.StatusNotFound, "模块不存在")
		return
	}
	ver := c.Query("v")
	key := "go:detail:" + modulePath + "|" + ver
	cached, ok := a.DetailCache.Get(key, cacheData)
	if !ok {
		view, err := a.buildModuleDetail(modulePath, ver, a.protocolBase(c))
		if err != nil {
			if errors.Is(err, errDetailQuery) {
				failMsg(c, http.StatusInternalServerError, "查询失败")
			} else {
				failMsg(c, http.StatusNotFound, "模块不存在")
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

// errDetailQuery 详情数据依赖的查询失败（区别于"模块不存在"，应返回 500 而非 404）。
var errDetailQuery = errors.New("详情数据查询失败")

// buildModuleDetail 组装模块详情响应体（不含登录态字段）。
func (a *App) buildModuleDetail(modulePath, ver, protoBase string) (gin.H, error) {
	mod, err := a.DB.GetModule(modulePath)
	if err != nil {
		return nil, err
	}
	// 版本表只取轻量字段：不为上千个版本读取 mod_content。
	// 查询失败必须报错：空版本表会被当真数据缓存 30s。
	items, err := a.DB.ListVersionItems(mod.ID)
	if err != nil {
		return nil, errDetailQuery
	}
	if ver == "" {
		ver = defaultVersionItem(items, mod.LatestVersion)
	}

	rows := make([]dtoVersion, 0, len(items))
	cachedCount, uncachedShown := 0, 0
	var cachedSize int64
	for _, v := range items {
		cached := v.ZipSizeBytes > 0
		if cached {
			cachedCount++
			cachedSize += v.ZipSizeBytes
		} else {
			if uncachedShown >= detailVersionLimit {
				continue
			}
			uncachedShown++
		}
		rows = append(rows, dtoVersion{
			ID:           v.ID,
			Version:      v.Version,
			Cached:       cached,
			ZipSizeBytes: v.ZipSizeBytes,
			ModSizeBytes: v.ModSizeBytes,
			PublishedAt:  v.InfoTime,
			CachedAt:     firstNonEmpty(v.ZipCachedAt, v.ModCachedAt, v.InfoCachedAt),
			Pseudo:       v.IsPseudo,
		})
	}

	var cur *database.VersionItem
	for i := range items {
		if items[i].Version == ver {
			cur = &items[i]
			break
		}
	}
	if cur == nil && len(items) > 0 {
		cur = &items[0]
	}

	goMod := ""
	var curReqs []Requirement
	curGoVersion := ""
	if cur != nil {
		if full, ferr := a.DB.GetVersion(modulePath, cur.Version); ferr == nil {
			goMod = full.ModContent
			curReqs = ParseRequirements(full.RequirementsJSON)
			curGoVersion = full.GoVersion
		}
	}

	// 下载链接使用 GOPROXY 编码形式
	escModule := proxy.EscapePath(modulePath)
	zipURL, modURL, infoURL := "", "", ""
	if cur != nil {
		escVer := proxy.EscapeVersion(cur.Version)
		// 下载链接由服务端注入 GOPROXY 协议端口（9092）绝对地址
		base := protoBase + "/" + escModule + "/@v/" + escVer
		zipURL = base + ".zip"
		modURL = base + ".mod"
		infoURL = base + ".info"
	}

	pkgGoDevURL := ""
	if cur != nil {
		pkgGoDevURL = "https://pkg.go.dev/" + modulePath + "@" + strings.TrimPrefix(cur.Version, "v")
	} else {
		pkgGoDevURL = "https://pkg.go.dev/" + modulePath
	}

	modView := gin.H{
		"modulePath":     mod.ModulePath,
		"latestVersion":  mod.LatestVersion,
		"latestTime":     mod.LatestTime,
		"metaCachedAt":   mod.MetaCachedAt,
		"lastAccessedAt": mod.LastAccessedAt,
	}

	out := gin.H{
		"module":     modView,
		"readmeHTML": string(utils.RenderMarkdown(mod.ReadmeContent)),
		"goMod":      goMod,
		"versions":   rows,
		// versionTotal 为真实版本总数：versions 可能被截断（未缓存版本只保留最近的若干个）
		"versionTotal":      len(items),
		"versionsTruncated": len(rows) < len(items),
		"current": gin.H{
			"version":      versionOf(cur),
			"zipSizeBytes": zipSizeOf(cur),
			"modSizeBytes": modSizeOf(cur),
			"publishedAt":  publishedOf(cur),
			"cachedAt":     cachedOf(cur),
			"goVersion":    curGoVersion,
			"requirements": curReqs,
			"pseudo":       pseudoOf(cur),
		},
		"cachedCount": cachedCount,
		"cachedSize":  cachedSize,
		"zipURL":      zipURL,
		"modURL":      modURL,
		"infoURL":     infoURL,
		"pkgGoDevURL": pkgGoDevURL,
	}
	return out, nil
}

// SearchAPI 与搜索页已随「独立搜索页下线」移除：列表页用工具栏直搜（ModulesAPI 的 q 参数）。

// defaultVersionItem 返回默认查看版本：优先已缓存 zip 的最新版本，否则最新元数据版本。
// items 已按版本号降序排列。
func defaultVersionItem(items []database.VersionItem, latest string) string {
	for _, v := range items {
		if v.ZipSizeBytes > 0 {
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func versionOf(v *database.VersionItem) string {
	if v == nil {
		return ""
	}
	return v.Version
}
func zipSizeOf(v *database.VersionItem) int64 {
	if v == nil {
		return 0
	}
	return v.ZipSizeBytes
}
func modSizeOf(v *database.VersionItem) int64 {
	if v == nil {
		return 0
	}
	return v.ModSizeBytes
}
func publishedOf(v *database.VersionItem) string {
	if v == nil {
		return ""
	}
	return v.InfoTime
}
func cachedOf(v *database.VersionItem) string {
	if v == nil {
		return ""
	}
	return firstNonEmpty(v.ZipCachedAt, v.ModCachedAt, v.InfoCachedAt)
}
func pseudoOf(v *database.VersionItem) bool {
	return v != nil && v.IsPseudo
}
