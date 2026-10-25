package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/webutil"
	"permaporter/internal/npmadapter/cleanup"
	"permaporter/internal/npmadapter/preheat"
	"permaporter/internal/npmadapter/proxy"
)

// 管理 JSON API：仪表盘 / 包管理 / 预热 / 请求日志（均需登录）。

// preheatMaxNames 单次预热包数上限。
const preheatMaxNames = 500

// diskWarnBytes 磁盘剩余空间告警阈值（5GB）。
const diskWarnBytes = 5 << 30

// trendDays 仪表盘趋势图展示天数。
const trendDays = 14

const adminPageSize = 20

// ---------- 健康检查（无需登录） ----------

// Healthz 轻量健康检查：DB 可用性 + 运行时长 + 当前回源数。
func (a *App) Healthz(c *gin.Context) {
	dbOK := a.DB.Ping() == nil
	code := http.StatusOK
	if !dbOK {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{
		"ok":       dbOK,
		"db":       map[bool]string{true: "ok", false: "fail"}[dbOK],
		"ready":    a.DB.Ready(), // 启动期后台回填是否完成（不影响代理主流程）
		"uptime":   time.Since(a.Started).Round(time.Second).String(),
		"inflight": a.Engine.Inflight(),
	})
}

// ---------- 仪表盘 ----------

// diskView 磁盘水位 + 回源计数（结构已下沉 core/webutil）。
type diskView = webutil.DiskView

// dashboardDisk 读取存储目录所在磁盘水位与回源计数。
func (a *App) dashboardDisk() diskView {
	settings, err := a.DB.GetSettings()
	if err != nil {
		return webutil.DiskViewError("读取配置失败", a.Engine.Inflight())
	}
	return webutil.ReadDiskView(settings.StorageDir, a.Engine.Inflight(), diskWarnBytes)
}

// DashboardAPI 仪表盘数据：统计、最近活动、磁盘水位、每日趋势（原始行，前端计算柱高）。
func (a *App) DashboardAPI(c *gin.Context) {
	pc, vc, size, hit := a.cachedStats()
	recent := a.cachedRecent()
	disk := a.dashboardDisk()

	rows, err := a.DB.DailyTrend(trendDays)
	trend := []gin.H{}
	if err == nil {
		for _, r := range rows {
			trend = append(trend, gin.H{
				"day":     r.Day,
				"meta":    r.MetaTotal,
				"tarball": r.TarballTotal,
				"hits":    r.TarballHits,
			})
		}
	}

	recentRows := make([]gin.H, 0, len(recent))
	for _, r := range recent {
		recentRows = append(recentRows, gin.H{
			"pkgName":       r.PkgName,
			"version":       r.Version,
			"fileSizeBytes": r.FileSizeBytes,
			"cachedAt":      r.CachedAt,
		})
	}

	okMsg(c, gin.H{
		"packageCount": pc,
		"versionCount": vc,
		"totalSize":    size,
		"hitRate":      hit,
		"recent":       recentRows,
		"disk":         disk,
		"trend":        trend,
	})
}

// ---------- 包列表 ----------

// AdminPackagesAPI 后台包列表：搜索 + 表头排序（服务端白名单）+ 分页（20/页）。
func (a *App) AdminPackagesAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	sortKey, dir := normalizeSort(c.Query("sort"), strings.ToLower(c.Query("dir")))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countPackagesCached(q)
	totalPages := int((total + adminPageSize - 1) / adminPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listPackagesSortedCached(q, sortKey, dir, (page-1)*adminPageSize, adminPageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	rows := make([]dtoPkgRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toPkgRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// normalizeSort 排序白名单校验（与生态语义无关，已下沉 core/webutil）。
var normalizeSort = webutil.NormalizeSort

// ---------- 包版本管理 ----------

// validPkgName 校验管理接口的 pkg 参数。这些接口会用它拼磁盘路径，
// 必须挡住 `../` 之类输入（否则可越出存储目录写文件）；"." / ".." 段同样拒绝。
func validPkgName(name string) bool {
	return ValidPkgName(name)
}

// PackageVersionsAPI 已缓存版本列表（不显示未缓存版本）。
func (a *App) PackageVersionsAPI(c *gin.Context) {
	pkgName := strings.TrimSpace(c.Query("pkg"))
	if !validPkgName(pkgName) {
		failMsg(c, http.StatusBadRequest, "包名格式无效")
		return
	}
	pkg, err := a.DB.GetPackage(pkgName)
	if err != nil {
		failMsg(c, http.StatusNotFound, "包不存在")
		return
	}
	versions, err := a.DB.CachedVersionsLite(pkg.ID)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询版本列表失败: "+err.Error())
		return
	}
	rows := make([]dtoVersion, 0, len(versions))
	for _, v := range versions {
		rows = append(rows, dtoVersion{
			ID:            v.ID,
			Version:       v.Version,
			FileSizeBytes: v.FileSizeBytes,
			CachedAt:      v.CachedAt,
		})
	}
	okMsg(c, gin.H{"pkgName": pkgName, "versions": rows})
}

// DeleteVersionAPI 删除已缓存版本：移除磁盘文件与 DB 记录。
// 若删除后该包不再有任何已缓存版本，则一并删除整个包及关联文件。
// 顺序上先删 DB 记录再删文件：即使文件删除失败，也只会留下孤立文件，
// 由清理任务回收；反过来则会留下"有记录无文件"的坏记录。
func (a *App) DeleteVersionAPI(c *gin.Context) {
	pkgName := strings.TrimSpace(c.Query("pkg"))
	version := strings.TrimSpace(c.Query("version"))
	if !validPkgName(pkgName) || version == "" {
		failMsg(c, http.StatusBadRequest, "参数格式无效")
		return
	}
	v, err := a.DB.GetVersionByName(pkgName, version)
	if err != nil {
		failMsg(c, http.StatusNotFound, "版本不存在")
		return
	}
	pkg, err := a.DB.GetPackage(pkgName)
	if err != nil {
		failMsg(c, http.StatusNotFound, "包不存在")
		return
	}
	settings, _ := a.DB.GetSettings()
	if err := a.DB.DeleteVersion(v.ID); err != nil {
		failMsg(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	// 记录已删除，再清理磁盘文件；失败也只留孤立文件（cleanup 会回收）。
	// 路径必须经越界断言：历史库中的非法版本键不得删除存储树外文件。
	if settings != nil && v.FileSizeBytes > 0 {
		if full, ferr := proxy.StorageFilePath(settings.StorageDir, filepath.FromSlash(v.TarballPath)); ferr == nil {
			_ = os.Remove(full)
		} else {
			log.Printf("[admin] 版本记录路径越界，跳过文件删除 %s@%s: %v", pkgName, version, ferr)
		}
	}
	// 检查剩余已缓存版本；查询失败时中止（误判为空会导致整包误删），无则删除整个包
	remaining, rerr := a.DB.CachedVersionsLite(pkg.ID)
	if rerr != nil {
		failMsg(c, http.StatusInternalServerError, "检查剩余版本失败: "+rerr.Error())
		return
	}
	if len(remaining) == 0 {
		if err := a.DB.DeletePackage(pkgName); err != nil {
			failMsg(c, http.StatusInternalServerError, "删除包失败: "+err.Error())
			return
		}
		if settings != nil {
			removePackageFiles(settings.StorageDir, pkgName)
		}
		okMsg(c, gin.H{"pkgRemoved": true})
		return
	}
	okMsg(c, gin.H{"pkgRemoved": false})
}

// RefreshMetaAPI 强制刷新单包元数据（忽略 META_TTL 与失败冷却）。
func (a *App) RefreshMetaAPI(c *gin.Context) {
	pkgName := strings.TrimSpace(c.Query("pkg"))
	if !validPkgName(pkgName) {
		failMsg(c, http.StatusBadRequest, "包名格式无效")
		return
	}
	if err := a.Engine.ForceRefreshMeta(pkgName); err != nil {
		failMsg(c, http.StatusInternalServerError, "刷新失败: "+err.Error())
		return
	}
	okMsg(c, nil)
}

// removePackageFiles 删除包的元数据文件与 tarball 目录。
func removePackageFiles(storageDir, pkgName string) {
	_ = os.Remove(filepath.Join(storageDir, "meta", pkgName+".json"))
	_ = os.RemoveAll(filepath.Join(storageDir, "packages", filepath.FromSlash(pkgName)))
}

// ---------- 预热 ----------

// PreheatAPI 最近一次预热任务快照（前端进行中每 2s 轮询）。
func (a *App) PreheatAPI(c *gin.Context) {
	okMsg(c, gin.H{"job": jobPayload(a.Preheat.LatestView())})
}

// PreheatStartAPI 提交预热任务：解析 package.json 或逐行包名。
func (a *App) PreheatStartAPI(c *gin.Context) {
	var req struct {
		Deps        string `json:"deps"`
		WithTarball bool   `json:"withTarball"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	names, invalid := parseDepNames(req.Deps)
	if len(names) == 0 {
		failMsg(c, http.StatusBadRequest, "未解析出有效包名，请粘贴 package.json 或每行一个包名")
		return
	}
	if len(invalid) > 0 {
		failMsg(c, http.StatusBadRequest, "以下包名格式无效："+strings.Join(invalid, ", "))
		return
	}
	if len(names) > preheatMaxNames {
		names = names[:preheatMaxNames]
	}
	a.Preheat.Start(names, req.WithTarball)
	okMsg(c, gin.H{"total": len(names), "job": jobPayload(a.Preheat.LatestView())})
}

// jobPayload 预热任务的 JSON 形态。
func jobPayload(j *preheat.JobView) any {
	if j == nil {
		return nil
	}
	items := make([]gin.H, 0, len(j.Items))
	for _, it := range j.Items {
		items = append(items, gin.H{
			"name":    it.Name,
			"state":   it.State,
			"version": it.Version,
			"size":    it.Size,
			"err":     it.Err,
		})
	}
	return gin.H{
		"id":          j.ID,
		"withTarball": j.WithTarball,
		"createdAt":   j.CreatedAt.Format(time.RFC3339),
		"finished":    j.Finished,
		"done":        j.Done,
		"failed":      j.Failed,
		"total":       j.Total,
		"items":       items,
	}
}

// parseDepNames 从粘贴内容解析包名：支持 package.json（dependencies/devDependencies）
// 或每行一个包名。返回去重后的有效包名与无效名列表。
func parseDepNames(raw string) (names, invalid []string) {
	raw = strings.TrimSpace(raw)
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			return
		}
		if pkgNameRe.MatchString(n) {
			seen[n] = true
			names = append(names, n)
		} else if !isLikelyVersioned(n) {
			invalid = append(invalid, n)
		}
	}
	if doc := tryParsePackageJSON(raw); doc != nil {
		for _, k := range sortedKeys(doc.Dependencies) {
			add(k)
		}
		for _, k := range sortedKeys(doc.DevDependencies) {
			add(k)
		}
		sort.Strings(names) // 全局排序保证输出稳定
		return names, invalid
	}
	for _, line := range strings.Split(raw, "\n") {
		// 行内注释：从 # 或 // 起截断
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '"'
		}) {
			// 去掉 "name@version" / "@scope/name@version" 的版本后缀
			add(stripVersionSuffix(tok))
		}
	}
	sort.Strings(names)
	return names, invalid
}

type depDoc struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func tryParsePackageJSON(raw string) *depDoc {
	if !strings.HasPrefix(raw, "{") {
		return nil
	}
	var doc depDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil
	}
	if len(doc.Dependencies) == 0 && len(doc.DevDependencies) == 0 {
		return nil
	}
	return &doc
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stripVersionSuffix 去掉行尾的 @version（@scope/name 的首个 @ 除外）。
func stripVersionSuffix(name string) string {
	if strings.HasPrefix(name, "@") {
		if i := strings.LastIndex(name, "@"); i > 0 {
			return name[:i]
		}
		return name
	}
	if i := strings.LastIndex(name, "@"); i > 0 {
		return name[:i]
	}
	return name
}

func isLikelyVersioned(n string) bool {
	// "react@18.2.0" 这类被 strip 后不会到这里；仅用于避免把明显含版本号的行报为无效
	return strings.Count(n, "@") > 1 && !strings.HasPrefix(n, "@")
}

// ---------- 请求日志 ----------

// LogsAPI 最近 200 条代理请求日志（内存环形缓冲，前端每 5s 轮询）。
func (a *App) LogsAPI(c *gin.Context) {
	entries := a.Engine.Log.Recent(200)
	rows := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, gin.H{
			"time":  e.Time.Format(time.RFC3339Nano),
			"ip":    e.IP,
			"kind":  e.Kind,
			"pkg":   e.Pkg,
			"hit":   e.Hit,
			"durMs": e.Dur.Milliseconds(),
			"err":   e.Err,
		})
	}
	okMsg(c, gin.H{"entries": rows})
}

// ---------- 设置 ----------

// SettingsAPI 读取系统配置（不含密码哈希）。
func (a *App) SettingsAPI(c *gin.Context) {
	settings, err := a.DB.GetSettings()
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "读取配置失败")
		return
	}
	okMsg(c, gin.H{
		"upstreamUrl":   settings.UpstreamURL,
		"fallbackUrl":   settings.FallbackURL,
		"outboundProxy": settings.OutboundProxy,
		"storageDir":    settings.StorageDir,
	})
}

// cleanupJob 手动清理任务状态机（与生态语义无关，已下沉 core/webutil；
// 清理规则与完成消息的拼装保留在本生态）。
var cleanupJob webutil.CleanupJob

// StartCleanupJSON 提交一次清理任务（后台执行），已在运行时返回 409。
func (a *App) StartCleanupJSON(c *gin.Context) {
	if cleanupJob.Running() {
		failMsg(c, http.StatusConflict, "清理任务正在执行，请稍候")
		return
	}
	settings, err := a.DB.GetSettings()
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "读取配置失败")
		return
	}
	storageDir := settings.StorageDir
	db := a.DB
	if !cleanupJob.Submit(func() string {
		res := cleanup.Run(db, storageDir)
		msg := "清理完成：" +
			"孤立压缩包 " + strconv.Itoa(res.RemovedTarballs) + " 个、" +
			"失效版本记录 " + strconv.Itoa(res.RemovedVersions) + " 条、" +
			"孤立版本 " + strconv.Itoa(res.RemovedOrphanVer) + " 条、" +
			"孤立元数据 " + strconv.Itoa(res.RemovedMetas) + " 个、" +
			"仅元数据包 " + strconv.Itoa(res.RemovedMetaOnly) + " 个" +
			metaTmpSuffix(res.RemovedMetaTmp) +
			cleanupDirSuffix(res.RemovedEmptyDirs)
		log.Printf("[cleanup] %s", msg)
		return msg
	}) {
		failMsg(c, http.StatusConflict, "清理任务正在执行，请稍候")
		return
	}
	okMsg(c, cleanupJob.Payload())
}

// CleanupStatusJSON 查询清理任务状态（前端轮询）。
func (a *App) CleanupStatusJSON(c *gin.Context) {
	okMsg(c, cleanupJob.Payload())
}

func cleanupDirSuffix(n int) string {
	if n > 0 {
		return "、空目录 " + strconv.Itoa(n) + " 个"
	}
	return ""
}

func metaTmpSuffix(n int) string {
	if n > 0 {
		return "、元数据临时残留 " + strconv.Itoa(n) + " 个"
	}
	return ""
}
