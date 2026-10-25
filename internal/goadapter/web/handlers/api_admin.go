package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"golang.org/x/mod/semver"

	"permaporter/internal/core/webutil"
	"permaporter/internal/goadapter/cleanup"
	"permaporter/internal/goadapter/preheat"
	"permaporter/internal/goadapter/proxy"
)

// 管理 JSON API：仪表盘 / 模块管理 / 预热 / 请求日志（均需登录）。

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
	mc, vc, size, hit := a.cachedStats()
	recent := a.cachedRecent()
	disk := a.dashboardDisk()

	rows, err := a.DB.DailyTrend(trendDays)
	trend := []gin.H{}
	if err == nil {
		for _, r := range rows {
			trend = append(trend, gin.H{
				"day":  r.Day,
				"meta": r.MetaTotal,
				"zip":  r.ZipTotal,
				"hits": r.ZipHits,
			})
		}
	}

	recentRows := make([]gin.H, 0, len(recent))
	for _, r := range recent {
		recentRows = append(recentRows, gin.H{
			"modulePath": r.ModulePath,
			"version":    r.Version,
			"zipSize":    r.ZipSize,
			"cachedAt":   r.CachedAt,
		})
	}

	okMsg(c, gin.H{
		"moduleCount":  mc,
		"versionCount": vc,
		"totalSize":    size,
		"hitRate":      hit,
		"recent":       recentRows,
		"disk":         disk,
		"trend":        trend,
	})
}

// ---------- 模块列表 ----------

// AdminModulesAPI 后台模块列表：搜索 + 表头排序（服务端白名单）+ 分页（20/页）。
func (a *App) AdminModulesAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	sortKey, dir := normalizeSort(c.Query("sort"), strings.ToLower(c.Query("dir")))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countModulesCached(q)
	totalPages := int((total + adminPageSize - 1) / adminPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listModulesSortedCached(q, sortKey, dir, (page-1)*adminPageSize, adminPageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	rows := make([]dtoModuleRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toModuleRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// normalizeSort 排序白名单校验（与生态语义无关，已下沉 core/webutil）。
var normalizeSort = webutil.NormalizeSort

// ---------- 模块版本管理 ----------

// validModuleInput 校验管理接口的 module 参数。这些接口会用它拼磁盘路径，
// 必须挡住 `../` 之类输入（否则可越出存储目录写文件）。
func validModuleInput(name string) bool {
	return name != "" && len(name) <= 512 && proxy.ValidateModuleInput(name) == nil
}

// ModuleVersionsAPI 已缓存版本列表（任何产物，含 zipCached 状态）。
func (a *App) ModuleVersionsAPI(c *gin.Context) {
	modulePath := strings.TrimSpace(c.Query("module"))
	if !validModuleInput(modulePath) {
		failMsg(c, http.StatusBadRequest, "模块路径格式无效")
		return
	}
	mod, err := a.DB.GetModule(modulePath)
	if err != nil {
		failMsg(c, http.StatusNotFound, "模块不存在")
		return
	}
	versions, err := a.DB.ListVersionItems(mod.ID)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询版本列表失败: "+err.Error())
		return
	}
	rows := make([]gin.H, 0, len(versions))
	for _, v := range versions {
		// 管理视图：有任何产物缓存（info/mod/zip）的版本才可管理
		if v.InfoCachedAt == "" && v.ModCachedAt == "" && v.ZipSizeBytes == 0 {
			continue
		}
		rows = append(rows, gin.H{
			"version":      v.Version,
			"zipSizeBytes": v.ZipSizeBytes,
			"modSizeBytes": v.ModSizeBytes,
			"zipCached":    v.ZipSizeBytes > 0,
			"modCached":    v.ModCachedAt != "",
			"infoCached":   v.InfoCachedAt != "",
			"cachedAt":     firstNonEmpty(v.ZipCachedAt, v.ModCachedAt, v.InfoCachedAt),
			"pseudo":       v.IsPseudo,
		})
	}
	okMsg(c, gin.H{"modulePath": modulePath, "versions": rows})
}

// DeleteVersionAPI 删除版本产物：移除磁盘文件与 DB 记录。
// 若删除后该模块不再有任何版本记录，则一并删除模块记录并清理其 @v 目录。
// 注意：模块路径可嵌套，只能删除该模块的 @v 目录再向上剪枝空目录，
// 禁止 RemoveAll(moduleDir)（会连带删除嵌套子模块）。
// 顺序上先删 DB 记录再删文件：即使文件删除失败，也只会留下孤立文件，
// 由清理任务回收；反过来则会留下"有记录无文件"的坏记录。
func (a *App) DeleteVersionAPI(c *gin.Context) {
	modulePath := strings.TrimSpace(c.Query("module"))
	version := strings.TrimSpace(c.Query("version"))
	if !validModuleInput(modulePath) || version == "" || !semver.IsValid(version) {
		failMsg(c, http.StatusBadRequest, "参数格式无效")
		return
	}
	settings, err := a.DB.GetSettings()
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "读取配置失败")
		return
	}
	remaining, err := a.DB.DeleteVersionByPath(modulePath, version)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	// 记录已删除，再清理磁盘文件；失败也只留孤立文件（cleanup 会回收）
	escDir := filepath.Join(settings.StorageDir, "modules", proxy.EscapePath(modulePath), "@v")
	escVer := proxy.EscapeVersion(version)
	for _, ext := range []string{".zip", ".ziphash", ".mod", ".info"} {
		_ = os.Remove(filepath.Join(escDir, escVer+ext))
	}
	if remaining == 0 {
		if err := a.DB.DeleteModule(modulePath); err != nil {
			failMsg(c, http.StatusInternalServerError, "删除模块失败: "+err.Error())
			return
		}
		// 删除该模块专属的 @v 目录（list/latest.json 都在其中），再向上剪枝空目录
		_ = os.RemoveAll(escDir)
		cleanup.PruneEmptyDirs(settings.StorageDir, filepath.Dir(escDir))
		okMsg(c, gin.H{"moduleRemoved": true})
		return
	}
	okMsg(c, gin.H{"moduleRemoved": false})
}

// RefreshModuleMetaAPI 强制刷新单模块元数据（@v/list 与 @latest，忽略 META_TTL 与失败冷却）。
func (a *App) RefreshModuleMetaAPI(c *gin.Context) {
	modulePath := strings.TrimSpace(c.Query("module"))
	if !validModuleInput(modulePath) {
		failMsg(c, http.StatusBadRequest, "模块路径格式无效")
		return
	}
	if err := a.Engine.ForceRefreshMeta(modulePath); err != nil {
		failMsg(c, http.StatusInternalServerError, "刷新失败: "+err.Error())
		return
	}
	okMsg(c, nil)
}

// ---------- 预热 ----------

// PreheatAPI 最近一次预热任务快照（前端进行中每 2s 轮询）。
func (a *App) PreheatAPI(c *gin.Context) {
	okMsg(c, gin.H{"job": jobPayload(a.Preheat.LatestView())})
}

// PreheatStartAPI 提交预热任务：解析 go.mod / go.sum / 逐行 module@version。
func (a *App) PreheatStartAPI(c *gin.Context) {
	var req struct {
		Deps    string `json:"deps"`
		WithZip bool   `json:"withZip"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	targets, invalid := preheat.ParseInput(req.Deps)
	if len(targets) == 0 {
		failMsg(c, http.StatusBadRequest, "未解析出有效模块，请粘贴 go.mod、go.sum 或每行一个 module@version")
		return
	}
	if len(invalid) > 0 {
		failMsg(c, http.StatusBadRequest, "以下模块路径格式无效："+strings.Join(invalid, ", "))
		return
	}
	a.Preheat.Start(targets, req.WithZip)
	okMsg(c, gin.H{"total": len(targets), "job": jobPayload(a.Preheat.LatestView())})
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
		"id":        j.ID,
		"withZip":   j.WithZip,
		"createdAt": j.CreatedAt.Format(time.RFC3339),
		"finished":  j.Finished,
		"done":      j.Done,
		"failed":    j.Failed,
		"total":     j.Total,
		"items":     items,
	}
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
			"复位缺失 zip 状态 " + strconv.Itoa(res.ResetZipRecords) + " 条、" +
			"孤立文件 " + strconv.Itoa(res.RemovedFiles) + " 个、" +
			"临时文件 " + strconv.Itoa(res.RemovedTmpFiles) + " 个" +
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
