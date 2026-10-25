package handlers

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/webutil"
	"permaporter/internal/pyadapter/cleanup"
	"permaporter/internal/pyadapter/preheat"
	"permaporter/internal/pyadapter/proxy"
)

// 管理 JSON API：仪表盘 / 项目管理 / 预热 / 请求日志（均需登录）。

// preheatMaxNames 单次预热项目数上限。
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
	pc, fc, size, hit := a.cachedStats()
	recent := a.cachedRecent()
	disk := a.dashboardDisk()

	rows, err := a.DB.DailyTrend(trendDays)
	trend := []gin.H{}
	if err == nil {
		for _, r := range rows {
			trend = append(trend, gin.H{
				"day":   r.Day,
				"index": r.IndexTotal,
				"file":  r.FileTotal,
				"hits":  r.FileHits,
			})
		}
	}

	recentRows := make([]gin.H, 0, len(recent))
	for _, r := range recent {
		recentRows = append(recentRows, gin.H{
			"projectName":   r.ProjectName,
			"version":       r.Version,
			"filename":      r.Filename,
			"fileSizeBytes": r.FileSizeBytes,
			"cachedAt":      r.CachedAt,
		})
	}

	okMsg(c, gin.H{
		"projectCount": pc,
		"fileCount":    fc,
		"totalSize":    size,
		"hitRate":      hit,
		"recent":       recentRows,
		"disk":         disk,
		"trend":        trend,
	})
}

// ---------- 项目列表 ----------

// AdminPyProjectsAPI 后台项目列表：搜索 + 表头排序（服务端白名单）+ 分页（20/页）。
func (a *App) AdminPyProjectsAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	sortKey, dir := normalizeSort(c.Query("sort"), strings.ToLower(c.Query("dir")))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countProjectsCached(q)
	totalPages := int((total + adminPageSize - 1) / adminPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listProjectsSortedCached(q, sortKey, dir, (page-1)*adminPageSize, adminPageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	rows := make([]dtoProjectRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toProjectRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// normalizeSort 排序白名单校验（与生态语义无关，已下沉 core/webutil）。
var normalizeSort = webutil.NormalizeSort

// ---------- 项目文件管理 ----------

// PyProjectFilesAPI 项目的已缓存文件列表（不显示未缓存文件）。
func (a *App) PyProjectFilesAPI(c *gin.Context) {
	name := strings.TrimSpace(c.Query("project"))
	if !validProjectName(name) {
		failMsg(c, http.StatusBadRequest, "项目名格式无效")
		return
	}
	proj, err := a.DB.GetProject(name)
	if err != nil {
		failMsg(c, http.StatusNotFound, "项目不存在")
		return
	}
	files, err := a.DB.ListProjectFiles(proj.ID)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询文件清单失败: "+err.Error())
		return
	}
	rows := make([]dtoPyFile, 0, len(files))
	for _, f := range files {
		if f.FileSizeBytes <= 0 {
			continue
		}
		rows = append(rows, dtoPyFile{
			ID:             f.ID,
			Version:        f.Version,
			Filename:       f.Filename,
			FileSizeBytes:  f.FileSizeBytes,
			Cached:         true,
			SHA256:         f.SHA256,
			RequiresPython: f.RequiresPython,
			UploadTime:     f.UploadTime,
			CachedAt:       f.CachedAt,
		})
	}
	okMsg(c, gin.H{"projectName": name, "files": rows})
}

// DeletePyFileAPI 删除已缓存文件：移除磁盘文件与 DB 记录。
// 项目记录保留——Simple 协议下仅元数据项目是合法缓存（锚点页仍可服务）。
// 顺序上先删 DB 记录再删文件：即使文件删除失败，也只会留下孤立文件，
// 由清理任务回收；反过来则会留下"有记录无文件"的坏记录。
func (a *App) DeletePyFileAPI(c *gin.Context) {
	filename := strings.TrimSpace(c.Query("filename"))
	if !validFilenameParam(filename) {
		failMsg(c, http.StatusBadRequest, "文件名格式无效")
		return
	}
	rec, err := a.DB.GetFileByName(filename)
	if err != nil {
		failMsg(c, http.StatusNotFound, "文件记录不存在")
		return
	}
	settings, _ := a.DB.GetSettings()
	if err := a.DB.DeleteFileByID(rec.ID); err != nil {
		failMsg(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	// 记录已删除，再清理磁盘文件；失败也只留孤立文件（cleanup 会回收）
	// 路径必须经越界断言：历史库中的非法 file_path 不得删除存储树外文件
	if settings != nil && rec.FileSizeBytes > 0 {
		if full, ferr := proxy.StorageFilePath(settings.StorageDir, filepath.FromSlash(rec.FilePath)); ferr == nil {
			_ = os.Remove(full)
		} else {
			log.Printf("[admin] 文件记录路径越界，跳过文件删除 %s: %v", filename, ferr)
		}
	}
	okMsg(c, nil)
}

// RefreshPyProjectAPI 强制刷新单项目锚点页（忽略 META_TTL 与失败冷却）。
func (a *App) RefreshPyProjectAPI(c *gin.Context) {
	name := strings.TrimSpace(c.Query("project"))
	if !validProjectName(name) {
		failMsg(c, http.StatusBadRequest, "项目名格式无效")
		return
	}
	if err := a.Engine.ForceRefreshProject(name); err != nil {
		failMsg(c, http.StatusInternalServerError, "刷新失败: "+err.Error())
		return
	}
	okMsg(c, nil)
}

// validFilenameParam 校验管理接口的 filename 参数（发行文件名字符集）。
func validFilenameParam(name string) bool {
	if name == "" || len(name) > 220 || strings.ContainsAny(name, "/\\") {
		return false
	}
	return pyFileParamRe.MatchString(name)
}

var pyFileParamRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+!-]*$`)

// ---------- 预热 ----------

// PreheatAPI 最近一次预热任务快照（前端进行中每 2s 轮询）。
func (a *App) PreheatAPI(c *gin.Context) {
	okMsg(c, gin.H{"job": jobPayload(a.Preheat.LatestView())})
}

// PreheatStartAPI 提交预热任务：解析 requirements.txt 或逐行项目名。
func (a *App) PreheatStartAPI(c *gin.Context) {
	var req struct {
		Deps      string `json:"deps"`
		WithFiles bool   `json:"withFiles"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	names, invalid := parsePyRequirementNames(req.Deps)
	if len(names) == 0 {
		failMsg(c, http.StatusBadRequest, "未解析出有效项目名，请粘贴 requirements.txt 或每行一个项目名")
		return
	}
	if len(invalid) > 0 {
		failMsg(c, http.StatusBadRequest, "以下项目名格式无效："+strings.Join(invalid, ", "))
		return
	}
	if len(names) > preheatMaxNames {
		names = names[:preheatMaxNames]
	}
	a.Preheat.Start(names, req.WithFiles)
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
		"id":        j.ID,
		"withFiles": j.WithFiles,
		"createdAt": j.CreatedAt.Format(time.RFC3339),
		"finished":  j.Finished,
		"done":      j.Done,
		"failed":    j.Failed,
		"total":     j.Total,
		"items":     items,
	}
}

// pyReqNameRe requirements 行中的项目名段（PEP 508 名字字符集）。
var pyReqNameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)`)

// parsePyRequirementNames 从粘贴内容解析项目名：支持 requirements.txt
// （含 `name==1.0`、`name>=1.0,<2`、`name[extra]`、行注释）或每行一个项目名。
// 返回规范化去重后的有效项目名与无法识别的行列表。
func parsePyRequirementNames(raw string) (names, invalid []string) {
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			return
		}
		if validProjectName(n) {
			seen[n] = true
			names = append(names, n)
		} else {
			invalid = append(invalid, n)
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		// 行注释与 pip 选项行跳过
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		// 去掉环境标记（; python_version >= "3.8"）；整行只有环境标记时截断后为空，直接跳过
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		// 项目名取行首 PEP 508 名字段（后跟 extras/版本说明符）
		m := pyReqNameRe.FindStringSubmatch(line)
		if m == nil {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			invalid = append(invalid, fields[0])
			continue
		}
		add(proxy.NormalizeProjectName(m[1]))
	}
	sort.Strings(names)
	return names, invalid
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
			"复位缺失文件状态 " + strconv.Itoa(res.ResetFileRecords) + " 条、" +
			"孤立文件 " + strconv.Itoa(res.RemovedFiles) + " 个、" +
			"临时残留 " + strconv.Itoa(res.RemovedTmpFiles) + " 个"
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
