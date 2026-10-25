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
	"permaporter/internal/mvnadapter/cleanup"
	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/preheat"
	"permaporter/internal/mvnadapter/proxy"
)

// 管理 JSON API：仪表盘 / 构件管理 / 预热 / 请求日志（均需登录）。

// preheatMaxTargets 单次预热构件数上限。
const preheatMaxTargets = 500

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
	ac, fc, size, hit := a.cachedStats()
	recent := a.cachedRecent()
	disk := a.dashboardDisk()

	rows, err := a.DB.DailyTrend(trendDays)
	trend := []gin.H{}
	if err == nil {
		for _, r := range rows {
			trend = append(trend, gin.H{
				"day":  r.Day,
				"meta": r.MetaTotal,
				"file": r.FileTotal,
				"hits": r.FileHits,
			})
		}
	}

	recentRows := make([]gin.H, 0, len(recent))
	for _, r := range recent {
		recentRows = append(recentRows, gin.H{
			"name":          database.ArtifactKey(r.GroupID, r.ArtifactID),
			"groupId":       r.GroupID,
			"artifactId":    r.ArtifactID,
			"version":       r.Version,
			"filename":      r.Filename,
			"fileSizeBytes": r.FileSizeBytes,
			"cachedAt":      r.CachedAt,
		})
	}

	okMsg(c, gin.H{
		"artifactCount": ac,
		"fileCount":     fc,
		"totalSize":     size,
		"hitRate":       hit,
		"recent":        recentRows,
		"disk":          disk,
		"trend":         trend,
	})
}

// ---------- 构件列表 ----------

// AdminMvnArtifactsAPI 后台构件列表：搜索 + 表头排序（服务端白名单）+ 分页（20/页）。
func (a *App) AdminMvnArtifactsAPI(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	sortKey, dir := normalizeSort(c.Query("sort"), strings.ToLower(c.Query("dir")))
	page := atoiDefault(c.DefaultQuery("page", "1"), 1)
	total, _ := a.countArtifactsCached(q)
	totalPages := int((total + adminPageSize - 1) / adminPageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	items, err := a.listArtifactsSortedCached(q, sortKey, dir, (page-1)*adminPageSize, adminPageSize)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询失败: "+err.Error())
		return
	}
	rows := make([]dtoArtifactRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, toArtifactRow(r))
	}
	okMsg(c, gin.H{"items": rows, "total": total, "page": page, "totalPages": totalPages})
}

// normalizeSort 排序白名单校验（与生态语义无关，已下沉 core/webutil）。
var normalizeSort = webutil.NormalizeSort

// ---------- 构件文件管理 ----------

// MvnArtifactFilesAPI 构件的已缓存文件列表（不显示未缓存文件）。
func (a *App) MvnArtifactFilesAPI(c *gin.Context) {
	coords := strings.TrimSpace(c.Query("coords"))
	groupID, artifactID, ok := validCoords(coords)
	if !ok {
		failMsg(c, http.StatusBadRequest, "构件坐标格式无效")
		return
	}
	art, err := a.DB.GetArtifact(groupID, artifactID)
	if err != nil {
		failMsg(c, http.StatusNotFound, "构件不存在")
		return
	}
	files, err := a.DB.ListArtifactFiles(art.ID)
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "查询文件清单失败: "+err.Error())
		return
	}
	rows := make([]dtoMvnFile, 0, len(files))
	for _, f := range files {
		if f.FileSizeBytes <= 0 {
			continue
		}
		rows = append(rows, dtoMvnFile{
			ID:            f.ID,
			RepoPath:      f.RepoPath,
			Version:       f.Version,
			Filename:      f.Filename,
			FileSizeBytes: f.FileSizeBytes,
			Cached:        true,
			SHA1:          f.SHA1,
			CachedAt:      f.CachedAt,
		})
	}
	okMsg(c, gin.H{"name": coords, "files": rows})
}

// DeleteMvnFileAPI 删除已缓存文件：移除磁盘文件与 DB 记录。
// 构件记录保留——Maven 仓库协议下仅元数据构件是合法缓存。
// 顺序上先删 DB 记录再删文件：即使文件删除失败，也只会留下孤立文件，
// 由清理任务回收；反过来则会留下"有记录无文件"的坏记录。
func (a *App) DeleteMvnFileAPI(c *gin.Context) {
	repoPath := strings.TrimSpace(c.Query("path"))
	if _, ok := proxy.ParseFileRepoPath(repoPath); !ok {
		failMsg(c, http.StatusBadRequest, "文件路径格式无效")
		return
	}
	rec, err := a.DB.GetFileByRepoPath(repoPath)
	if err != nil {
		failMsg(c, http.StatusNotFound, "文件记录不存在")
		return
	}
	settings, _ := a.DB.GetSettings()
	if err := a.DB.DeleteFileByRepoPath(repoPath); err != nil {
		failMsg(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	// 记录已删除，再清理磁盘文件；失败也只留孤立文件（cleanup 会回收）
	// 路径必须经越界断言：历史库中的非法 file_path 不得删除存储树外文件
	if settings != nil && rec.FileSizeBytes > 0 {
		if full, ferr := proxy.StorageFilePath(settings.StorageDir, filepath.FromSlash(rec.FilePath)); ferr == nil {
			_ = os.Remove(full)
		} else {
			log.Printf("[mvn-admin] 文件记录路径越界，跳过文件删除 %s: %v", repoPath, ferr)
		}
	}
	okMsg(c, nil)
}

// RefreshMvnMetaAPI 强制刷新单构件元数据（忽略 META_TTL 与失败冷却）。
func (a *App) RefreshMvnMetaAPI(c *gin.Context) {
	coords := strings.TrimSpace(c.Query("coords"))
	groupID, artifactID, ok := validCoords(coords)
	if !ok {
		failMsg(c, http.StatusBadRequest, "构件坐标格式无效")
		return
	}
	if err := a.Engine.ForceRefreshArtifactMeta(groupID, artifactID); err != nil {
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

// PreheatStartAPI 提交预热任务：解析 pom.xml <dependency> 块、Gradle 坐标串
// 或每行 groupId:artifactId[:version][:classifier] 坐标。
func (a *App) PreheatStartAPI(c *gin.Context) {
	var req struct {
		Deps      string `json:"deps"`
		WithFiles bool   `json:"withFiles"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	targets, invalid := parseMavenTargets(req.Deps)
	if len(targets) == 0 {
		failMsg(c, http.StatusBadRequest, "未解析出有效构件坐标，请粘贴 pom.xml 依赖块、Gradle 坐标或每行一个 groupId:artifactId[:version]")
		return
	}
	if len(invalid) > 0 {
		failMsg(c, http.StatusBadRequest, "以下构件坐标无效："+strings.Join(invalid, ", "))
		return
	}
	if len(targets) > preheatMaxTargets {
		targets = targets[:preheatMaxTargets]
	}
	a.Preheat.Start(targets, req.WithFiles)
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
		"withFiles": j.WithFiles,
		"createdAt": j.CreatedAt.Format(time.RFC3339),
		"finished":  j.Finished,
		"done":      j.Done,
		"failed":    j.Failed,
		"total":     j.Total,
		"items":     items,
	}
}

// dependencyBlockRe pom.xml 的 <dependency> 块。
var dependencyBlockRe = regexp.MustCompile(`(?s)<dependency\b[^>]*>(.*?)</dependency>`)

// elemRe 提取 XML 元素文本。
func elemRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)<` + name + `>\s*(.*?)\s*</` + name + `>`)
}

var (
	groupIDRe    = elemRe("groupId")
	artifactIDRe = elemRe("artifactId")
	versionRe    = elemRe("version")
)

// coordTokenRe 行内 Maven 坐标（g:a[:v][:c]，支持 Gradle 字符串里的引号包裹与 @ext 后缀）。
var coordTokenRe = regexp.MustCompile(`([A-Za-z0-9][A-Za-z0-9._-]*):([A-Za-z0-9][A-Za-z0-9._-]*)(?::([A-Za-z0-9][A-Za-z0-9._-]*))?(?::([A-Za-z0-9][A-Za-z0-9._-]*))?(?:@[A-Za-z0-9]+)?`)

// versionReUsable 版本段是否可用：属性引用（${…}）与区间（[1.0,2.0)）无法直接回源。
func versionUsable(v string) bool {
	if v == "" {
		return true // 空版本 = 取 latest
	}
	return !strings.ContainsAny(v, "${}[(")
}

// parseMavenTargets 从粘贴内容解析构件坐标：支持 pom.xml（<dependency> 块）、
// Gradle 脚本行（implementation "g:a:v"）或每行一个 g:a[:v][:c]。
// 返回去重后的坐标列表与无法识别的行列表。
func parseMavenTargets(raw string) (targets []preheat.Target, invalid []string) {
	seen := map[string]bool{}
	add := func(t preheat.Target) {
		key := t.Group + ":" + t.Artifact + ":" + t.Version + ":" + t.Classifier
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, t)
	}
	// pom.xml：dependency 块内的 groupId/artifactId/version
	for _, block := range dependencyBlockRe.FindAllStringSubmatch(raw, -1) {
		g := firstSubmatch(groupIDRe, block[1])
		a := firstSubmatch(artifactIDRe, block[1])
		v := firstSubmatch(versionRe, block[1])
		if g == "" || a == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if !versionUsable(v) {
			v = "" // 属性引用/区间版本回退 latest
		}
		if strings.Contains(g, "${") || strings.Contains(a, "${") {
			continue
		}
		if proxy.ValidGroupArtifact(g, a) {
			add(preheat.Target{Group: g, Artifact: a, Version: v})
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		// 行注释与 XML 行跳过（pom 块已在上面处理；此处也会命中行内的坐标 token，
		// 靠 seen 去重不会重复登记）
		if i := strings.Index(line, "<"); i >= 0 {
			continue
		}
		if i := strings.IndexAny(line, "#//"); i >= 0 {
			// "#" "//" 注释前缀截断；单独的 "/" 不截断（坐标不含 /）
			if line[i] == '#' || (i+1 < len(line) && line[i+1] == '/') {
				line = line[:i]
			}
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 无冒号的行不可能是 Maven 坐标（结构噪音如 "dependencies {"）：
		// 静默跳过；只有"长得像坐标但解析失败"的行才报给用户。
		if !strings.Contains(line, ":") {
			continue
		}
		m := coordTokenRe.FindStringSubmatch(line)
		if m == nil {
			invalid = append(invalid, line)
			continue
		}
		g, art, ver, cls := m[1], m[2], m[3], m[4]
		// 四段形态 g:a:v:c；三段无法区分 v/c，按 Maven 惯例视为版本
		if !versionUsable(ver) {
			ver = ""
		}
		if !proxy.ValidGroupArtifact(g, art) {
			invalid = append(invalid, line)
			continue
		}
		add(preheat.Target{Group: g, Artifact: art, Version: ver, Classifier: cls})
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Group+":"+targets[i].Artifact < targets[j].Group+":"+targets[j].Artifact
	})
	return targets, invalid
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
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
			"临时残留 " + strconv.Itoa(res.RemovedTmpFiles) + " 个、" +
			"空目录 " + strconv.Itoa(res.PrunedDirs) + " 个"
		log.Printf("[mvn-cleanup] %s", msg)
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
