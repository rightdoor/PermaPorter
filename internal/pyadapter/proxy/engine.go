// Package proxy 实现核心代理引擎：simple 锚点页与发行文件按需懒加载与缓存。
// 协议为 PEP 503：/simple/<project>/ 锚点页（元数据，TTL + ETag 条件刷新）、
// /packages/<filename> 发行文件（不可变，永久缓存，sha256 校验链）。
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"

	"permaporter/internal/core/logring"
	"permaporter/internal/core/upstream"
	"permaporter/internal/core/utils"
	"permaporter/internal/core/webutil"
	"permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/models"
)

// maxIndexSize 锚点页响应体大小上限，防止异常上游拖垮内存。
// 真实锚点页最大约 1MB 量级（torch 等多文件项目），16MB 留足余量。
const maxIndexSize = 16 << 20

// metaUpdateTimeout 锚点页拉取的整体超时。
const metaUpdateTimeout = 30 * time.Second

// defaultMetaRefreshTTL 锚点页刷新间隔默认值：新版本最迟在该时长后可见。
// 过期后先做条件请求（ETag），上游未变化时只需一次 304，不会重写文件表。
const defaultMetaRefreshTTL = 2 * time.Minute

// metaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先），
// 后台刷新继续执行；下次请求即可拿到新数据。
const metaRefreshWait = 5 * time.Second

// metaRefreshCooldown 刷新失败后的冷却时间，避免上游故障期间每个请求都等待刷新超时。
const metaRefreshCooldown = time.Minute

// metaFailMapMax 失败冷却表条目上限：超过后先清理已过期条目，防止大量一次性失败项目长期占用内存。
const metaFailMapMax = 4096

// fileDownloadTimeout 单个发行文件下载的整体超时（大 wheel 可达数百 MB）。
const fileDownloadTimeout = 30 * time.Minute

// upstreamMaxConcurrent 上游并发拉取上限：突发批量安装时限流对上游的请求，避免互相拖垮。
const upstreamMaxConcurrent = 16

// dbTimeFormat DB 时间串格式（UTC），与数据库层口径一致。
const dbTimeFormat = "2006-01-02 15:04:05"

var errUpstreamNotFound = errors.New("上游不存在该项目或文件")

// errMetaRefreshTimeout 缓存过期后的同步刷新等待超时（此时会先返回旧缓存）。
var errMetaRefreshTimeout = errors.New("元数据刷新超时")

// Engine 代理引擎。
type Engine struct {
	DB      *database.DB
	Stats   *Stats
	clients *upstream.ClientCache
	sf      singleflight.Group // 并发控制：同一资源仅首个请求触发上游拉取
	gate    chan struct{}      // 上游并发闸
	Log     *logring.LogRing   // 最近代理请求日志（内存环形缓冲）

	// MetaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先）。
	MetaRefreshWait time.Duration

	// 元数据刷新失败冷却：项目名 -> 最近一次失败时间
	failMu    sync.Mutex
	failUntil map[string]time.Time

	// bgWG 后台任务（后台元数据刷新、文件下载）等待组：
	// 优雅退出时先等它们结束，再关闭数据库，避免在途下载写库报错。
	bgWG sync.WaitGroup
}

func dbTimeFormatNow() string { return time.Now().UTC().Format(dbTimeFormat) }

// NewEngine 创建代理引擎。锚点页刷新间隔（MetaTTL）读自生态配置行
// ecosystem_settings.meta_ttl（每次请求随 settings 取得，改配置即生效）。
func NewEngine(db *database.DB, stats *Stats) *Engine {
	return &Engine{
		DB:              db,
		Stats:           stats,
		clients:         upstream.NewClientCache(),
		gate:            make(chan struct{}, upstreamMaxConcurrent),
		Log:             logring.NewLogRing(logring.LogCapacity),
		MetaRefreshWait: metaRefreshWait,
		failUntil:       make(map[string]time.Time),
	}
}

// Inflight 返回当前正在从上游拉取的资源数（含排队）。
func (e *Engine) Inflight() int { return len(e.gate) }

// Shutdown 等待后台任务（后台元数据刷新、在途文件下载）结束，
// 最多等到 ctx 超时。调用方应在此之前停止接收新请求（http.Server.Shutdown）。
func (e *Engine) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		e.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("[proxy] 退出时仍有后台任务未完成，超时放弃等待")
	}
}

// ServeSimpleIndex 处理 GET /simple/：本地已收录项目的根索引（字典序）。
// 只反映本地已请求过的项目，不回源拉取 PyPI 全量清单。
func (e *Engine) ServeSimpleIndex(c *gin.Context) {
	start := time.Now()
	names, err := e.DB.AllProjectNames()
	if err != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取项目索引失败")
		e.logReq(c, "index", "-", false, start, err.Error())
		return
	}
	e.Stats.RecordIndexHit(true)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Data(http.StatusOK, "text/html; charset=utf-8", GenerateSimpleIndexHTML(names))
	e.logReq(c, "index", "-", true, start, "")
}

// ServeProjectPage 处理 GET /simple/<project>/：
// 本地命中且未过期直接返回；已过期时先同步刷新再返回（保证刚发布的版本第一次请求即可见），
// 刷新失败或超过 MetaRefreshWait 时回退旧缓存，后台刷新继续执行。
// 锚点页内容由 py_files 表生成（相对路径锚点），不做哨兵改写。
func (e *Engine) ServeProjectPage(c *gin.Context, rawName string) {
	start := time.Now()
	name := NormalizeProjectName(rawName)
	if !ValidProjectName(name) {
		e.proxyError(c, http.StatusBadRequest, "非法的项目名")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "index", name, false, start, "未初始化")
		return
	}
	if proj, perr := e.DB.GetProjectLite(name); perr == nil {
		if metaStale(proj.MetaCachedAt, settings.MetaTTL) && !e.refreshCoolingDown(name) {
			// 缓存过期：先刷新再返回，避免客户端拿到不含新版本的旧锚点页。
			// 失败/超时则回退旧缓存，并在冷却期内跳过重复刷新。
			if _, rerr := e.refreshProjectPage(name, settings, e.MetaRefreshWait); rerr == nil {
				e.serveIndexPageWith(c, proj, true, start)
				return
			}
		}
		// 命中判定复用同一份文件清单（serveIndexPage 不再重复查询）
		if files, ferr := e.DB.ListProjectFiles(proj.ID); ferr == nil && len(files) > 0 {
			e.serveIndexPage(c, proj, files, true, start)
			return
		}
	}
	// 冷缓存（或 DB 有记录但文件表为空）：同步拉取。
	// 命中/未命中计数由 serveIndexPage 统一记录，此处不再显式计数（避免双计）。
	if _, err := e.refreshProjectPage(name, settings, 0); err != nil {
		e.logReq(c, "index", name, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, http.StatusNotFound, err.Error())
		} else {
			e.proxyError(c, http.StatusBadGateway, err.Error())
		}
		return
	}
	proj, err := e.DB.GetProjectLite(name)
	if err != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取项目失败")
		return
	}
	e.serveIndexPageWith(c, proj, false, start)
}

// serveIndexPageWith 查询文件清单后返回锚点页（清单未知或刚刷新、需重新读取时使用）。
func (e *Engine) serveIndexPageWith(c *gin.Context, proj *models.Project, hit bool, start time.Time) {
	files, ferr := e.DB.ListProjectFiles(proj.ID)
	if ferr != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取文件清单失败")
		e.logReq(c, "index", proj.ProjectName, hit, start, ferr.Error())
		return
	}
	e.serveIndexPage(c, proj, files, hit, start)
}

// serveIndexPage 生成并返回锚点页（内容由 py_files 表派生，锚点为绝对地址）。
// files 由调用方提供（命中判定与展示共用一份，避免热路径重复查询）。
func (e *Engine) serveIndexPage(c *gin.Context, proj *models.Project, files []database.FileItem, hit bool, start time.Time) {
	e.Stats.RecordIndexHit(hit)
	if hit {
		e.DB.TouchProject(proj.ProjectName)
	}
	views := make([]FileItemView, 0, len(files))
	for _, f := range files {
		views = append(views, FileItemView{Filename: f.Filename, SHA256: f.SHA256, RequiresPython: f.RequiresPython})
	}
	displayName := proj.DisplayName
	if displayName == "" {
		displayName = proj.ProjectName
	}
	c.Header("X-Proxy-Cache", map[bool]string{true: "HIT", false: "MISS"}[hit])
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", GenerateProjectPage(displayName, views, requestBase(c)))
	e.logReq(c, "index", proj.ProjectName, hit, start, "")
}

// requestBase 推断客户端可访问的 Simple 端点基地址（兼容反代）。
// 反代头仅在直连 peer 为本机可信代理时采信（见 webutil.ExternalScheme）。
func requestBase(c *gin.Context) string {
	scheme := webutil.ExternalScheme(c)
	host := webutil.ForwardedHost(c)
	return scheme + "://" + host
}

// ServePackageFile 处理 GET /packages/<filename>：
// 本地命中零拷贝返回；未命中按记录的上游地址回源（sha256 校验链），永久缓存。
func (e *Engine) ServePackageFile(c *gin.Context, filename string) {
	start := time.Now()
	if !ValidFilename(filename) {
		e.proxyError(c, http.StatusBadRequest, "非法的文件名")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "file", filename, false, start, "未初始化")
		return
	}
	rec, err := e.DB.GetFileByName(filename)
	if err != nil {
		// 无文件记录（锚点页从未缓存或上游未收录）：pip 总是先查锚点页，此处多为异常请求
		e.Stats.RecordFileHit(false)
		e.logReq(c, "file", filename, false, start, "无文件记录")
		e.proxyError(c, http.StatusNotFound, "上游不存在该文件")
		return
	}
	// 平铺布局：storage/packages/<filename>；已缓存记录按其登记路径。
	// DB 记录拼路径必须经越界断言：历史库中的非法路径不得读/写存储树外文件（对齐 npm 侧）。
	rel := rec.FilePath
	if rel == "" {
		rel = "packages/" + filename
	}
	fullPath, perr := StorageFilePath(settings.StorageDir, filepath.FromSlash(rel))
	if perr != nil {
		e.logReq(c, "file", filename, false, start, "文件记录路径非法")
		e.proxyError(c, http.StatusInternalServerError, "文件记录路径非法")
		return
	}
	if rec.FilePath != "" {
		if fi, serr := os.Stat(fullPath); serr == nil && !fi.IsDir() {
			e.Stats.RecordFileHit(true)
			e.DB.TouchProject(rec.ProjectName)
			c.Header("X-Proxy-Cache", "HIT")
			e.logReq(c, "file", filename, true, start, "")
			e.serveFile(c, fullPath, filename)
			return
		}
	}
	e.Stats.RecordFileHit(false)
	key := "file:" + filename
	_, err, _ = e.sf.Do(key, func() (any, error) {
		// 下载与请求生命周期解耦，整体超时预算，失败不再无限挂起
		ctx, cancel := context.WithTimeout(context.Background(), fileDownloadTimeout)
		defer cancel()
		return nil, e.downloadPyFile(ctx, settings, rec, fullPath)
	})
	if err != nil {
		e.logReq(c, "file", filename, false, start, err.Error())
		e.proxyError(c, http.StatusBadGateway, "文件拉取失败: "+err.Error())
		return
	}
	c.Header("X-Proxy-Cache", "MISS")
	e.logReq(c, "file", filename, false, start, "")
	e.serveFile(c, fullPath, filename)
}

// PreheatProject 预热单个项目：确保锚点页已缓存；wantFiles 时再下载 version
// （空串取最新版）的全部发行文件。返回 state："cached"（原本就已就绪）或 "done"
// （本次新拉取），以及版本号与该版本文件总大小。
func (e *Engine) PreheatProject(name, version string, wantFiles bool) (state, resolvedVersion string, size int64, err error) {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return "", "", 0, err
	}
	norm := NormalizeProjectName(name)
	metaReady := false
	if _, gerr := e.DB.GetProject(norm); gerr == nil {
		metaReady = true
	}
	if !metaReady {
		if _, err := e.refreshProjectPage(norm, settings, 0); err != nil {
			return "", "", 0, err
		}
	}
	proj, err := e.DB.GetProject(norm)
	if err != nil {
		return "", "", 0, err
	}
	if version == "" {
		version = proj.LatestVersion
	}
	if version == "" {
		return "", "", 0, errors.New("上游锚点页无可用版本")
	}
	resolvedVersion = version
	files, err := e.DB.ListProjectFilesForVersion(proj.ID, version)
	if err != nil {
		return "", version, 0, fmt.Errorf("读取版本文件失败: %w", err)
	}
	if len(files) == 0 {
		return "", version, 0, fmt.Errorf("上游锚点页无版本 %s 的文件", version)
	}
	if !wantFiles {
		if metaReady {
			return "cached", version, 0, nil
		}
		return "done", version, 0, nil
	}
	// 逐文件确保本体已缓存（引擎内 singleflight + 上游并发闸限流）。
	// 平铺布局统一落盘 storage/packages/<filename>（与正常请求路径一致，清理器只扫描
	// packages/）：以 DB 缓存状态（已登记路径且大小 > 0）判定是否已缓存，
	// 不再对存储根做 Stat（历史预热误写根目录的记录重新下载到正确位置）。
	for _, f := range files {
		rec, ferr := e.DB.GetFileByName(f.Filename)
		if ferr != nil {
			return "", version, size, fmt.Errorf("文件记录缺失: %s", f.Filename)
		}
		fullPath, aerr := StorageFilePath(settings.StorageDir, "packages", rec.Filename)
		if aerr != nil {
			return "", version, size, fmt.Errorf("文件记录路径非法: %s", f.Filename)
		}
		if rec.FileSizeBytes > 0 && rec.FilePath != "" {
			if st, serr := os.Stat(fullPath); serr == nil && !st.IsDir() {
				size += st.Size()
				continue
			}
		}
		key := "file:" + f.Filename
		if _, err, _ := e.sf.Do(key, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), fileDownloadTimeout)
			defer cancel()
			return nil, e.downloadPyFile(ctx, settings, rec, fullPath)
		}); err != nil {
			return "", version, size, fmt.Errorf("下载 %s 失败: %w", f.Filename, err)
		}
		// 下载后体积取实际落盘大小（rec.FileSizeBytes 是下载前的旧快照，可能为 0）
		if st, serr := os.Stat(fullPath); serr == nil && !st.IsDir() {
			size += st.Size()
		}
	}
	if metaReady {
		return "cached", version, size, nil
	}
	return "done", version, size, nil
}

// ForceRefreshProject 强制从上游刷新项目锚点页（忽略 TTL 与失败冷却）。
func (e *Engine) ForceRefreshProject(name string) error {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return err
	}
	// 手动刷新不受失败冷却限制
	e.clearRefreshFailure(NormalizeProjectName(name))
	_, err = e.refreshProjectPage(NormalizeProjectName(name), settings, 0)
	return err
}

// TestUpstream 测试出站代理连通性，返回延迟。
// 带整体超时：Transport 只限制到响应头，响应体 stalled 时管理端测试请求不能无限挂起。
func (e *Engine) TestUpstream(proxyURL, targetURL string) (time.Duration, error) {
	client := e.clients.Get(proxyURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 400 {
		return time.Since(start), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// logReq 记录一条代理请求日志（环形缓冲，重启清空）。
func (e *Engine) logReq(c *gin.Context, kind, name string, hit bool, start time.Time, errMsg string) {
	e.Log.Add(logring.LogEntry{
		Time: time.Now(),
		IP:   c.ClientIP(),
		Kind: kind,
		Pkg:  name,
		Hit:  hit,
		Dur:  time.Since(start),
		Err:  errMsg,
	})
}

// proxyError 统一协议错误输出：9093 一律 text/plain; charset=utf-8。
func (e *Engine) proxyError(c *gin.Context, code int, msg string) {
	c.Data(code, "text/plain; charset=utf-8", []byte(msg))
}

// ---------- 内部实现 ----------

// metaStale 判断锚点页缓存时间是否超过刷新 TTL。
func metaStale(metaCachedAt string, ttl time.Duration) bool {
	if metaCachedAt == "" {
		return true
	}
	if ttl <= 0 {
		ttl = defaultMetaRefreshTTL
	}
	for _, f := range []string{dbTimeFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, metaCachedAt); err == nil {
			return time.Since(t) >= ttl
		}
	}
	return true
}

// acquireUpstream 获取上游并发额度，ctx 取消时放弃排队。
func (e *Engine) acquireUpstream(ctx context.Context) (func(), error) {
	select {
	case e.gate <- struct{}{}:
		return func() { <-e.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetchAndCacheProjectPage 拉取并缓存单项目锚点页（条件请求 + 解析入库）。
// settings.UpstreamURL 为 PyPI Simple 根（以 /simple/ 结尾），项目页路径相对该根拼接。
func (e *Engine) fetchAndCacheProjectPage(ctx context.Context, settings *models.Settings, name string) error {
	// 缓存中记录了上游 ETag 时做条件请求：未变化（304）只更新时间戳，避免重写文件表。
	etag := ""
	if p, err := e.DB.GetProjectLite(name); err == nil {
		etag = p.MetaETag
	}
	raw, status, newETag, pageURL, err := e.fetchIndex(ctx, settings, "/"+name+"/", etag)
	if status == http.StatusNotModified {
		e.DB.TouchMetaCachedAt(name)
		return nil
	}
	if err != nil {
		if status == http.StatusNotFound {
			return errUpstreamNotFound
		}
		return fmt.Errorf("上游请求失败: %w", err)
	}
	return e.cacheProjectPage(settings, name, raw, newETag, pageURL)
}

// refreshProjectPage 同步刷新单项目锚点页，并用 singleflight 归并同项目并发请求。
// wait > 0 时最多等待 wait 时长：超时返回 errMetaRefreshTimeout，但后台刷新继续执行，
// 调用方可先回退旧缓存；wait <= 0 表示一直等到刷新完成（受 metaUpdateTimeout 约束）。
func (e *Engine) refreshProjectPage(name string, settings *models.Settings, wait time.Duration) (struct{}, error) {
	type result struct {
		err error
	}
	done := make(chan result, 1)
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		key := "index:" + name
		_, err, _ := e.sf.Do(key, func() (any, error) {
			// 与请求生命周期解耦：客户端断开不会连坐同项目全部等待者
			ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
			defer cancel()
			return nil, e.fetchAndCacheProjectPage(ctx, settings, name)
		})
		if err != nil {
			e.markRefreshFailed(name)
			done <- result{err: err}
			return
		}
		e.clearRefreshFailure(name)
		done <- result{}
	}()

	if wait <= 0 {
		r := <-done
		return struct{}{}, r.err
	}
	select {
	case r := <-done:
		return struct{}{}, r.err
	case <-time.After(wait):
		// 超时瞬间后台可能刚好完成：再取一次，避免把成功的刷新误标为失败（否则会白等一分钟冷却）。
		select {
		case r := <-done:
			return struct{}{}, r.err
		default:
		}
		// 本次先返回旧缓存；冷却一小段时间，避免后续请求继续排队等待同一个慢刷新
		//（后台刷新成功后会清除冷却标记）。
		e.markRefreshFailed(name)
		log.Printf("[proxy] 锚点页刷新超过 %s，先返回旧缓存: %s", wait, name)
		return struct{}{}, errMetaRefreshTimeout
	}
}

// refreshCoolingDown 判断该项目是否处于刷新失败冷却期。
func (e *Engine) refreshCoolingDown(name string) bool {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	until, ok := e.failUntil[name]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(e.failUntil, name)
		return false
	}
	return true
}

func (e *Engine) markRefreshFailed(name string) {
	now := time.Now()
	e.failMu.Lock()
	defer e.failMu.Unlock()
	// 一次性失败大量项目时避免 map 无限增长：达到上限先清掉已过期条目
	if len(e.failUntil) >= metaFailMapMax {
		for n, until := range e.failUntil {
			if now.After(until) {
				delete(e.failUntil, n)
			}
		}
	}
	e.failUntil[name] = now.Add(metaRefreshCooldown)
}

func (e *Engine) clearRefreshFailure(name string) {
	e.failMu.Lock()
	delete(e.failUntil, name)
	e.failMu.Unlock()
}

// cacheProjectPage 解析上游锚点页并入库（项目行 + 文件清单 upsert）。
// 锚点页本身不落盘：服务时由 py_files 表生成（相对路径锚点与 host 无关）。
// pageURL 为本次抓取命中的锚点页地址，作为相对锚点解析基准传给解析器。
func (e *Engine) cacheProjectPage(settings *models.Settings, name string, raw []byte, etag, pageURL string) error {
	proj, files, err := ParseSimpleIndex(raw, name, pageURL)
	if err != nil {
		return err
	}
	proj.MetaETag = etag
	projectID, err := e.DB.UpsertProject(proj)
	if err != nil {
		return err
	}
	for i := range files {
		files[i].ProjectID = projectID
	}
	return e.DB.UpsertFiles(projectID, files)
}

// fetchIndex 依次尝试主/备用上游，返回首个 200 响应。
// etag 非空时仅对主上游发送 If-None-Match：内容未变化时返回 304（body 为空），
// 避免重新解析并重写文件表。备用上游内容可能不同，不带条件头。
// fetchedURL 为实际命中上游的完整地址（相对锚点解析的基准页 URL）。
func (e *Engine) fetchIndex(ctx context.Context, settings *models.Settings, urlPath, etag string) (body []byte, status int, newETag, fetchedURL string, err error) {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "", "", fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	var lastBody []byte
	lastStatus := 0
	for i, u := range upstreamCandidates(settings, urlPath) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			log.Printf("[proxy] 构建请求失败 %s: %v", u, err)
			continue
		}
		if i == 0 && etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := client.Do(req)
		if err != nil {
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 请求上游失败 %s: %v", u, err)
			continue
		}
		if resp.StatusCode == http.StatusNotModified {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, http.StatusNotModified, etag, u, nil
		}
		// 读 max+1 字节判断是否超限：恰好 max 时无法区分"正好达到上限"与"被截断"
		var rerr error
		body, rerr = io.ReadAll(io.LimitReader(resp.Body, maxIndexSize+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			// 读取中断得到的是截断体：不能当成功返回（可能被解析入库）
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 读取上游响应失败 %s: %v", u, rerr)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		if int64(len(body)) > maxIndexSize {
			// 超限响应按上游错误处理：截断的锚点页会静默丢失尾部发行文件，绝不入库
			log.Printf("[proxy] 上游响应超过大小上限 %d: %s", maxIndexSize, u)
			e.Stats.RecordUpstreamError(errKindOversize)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		lastBody, lastStatus = body, resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			return body, resp.StatusCode, resp.Header.Get("ETag"), u, nil
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			e.Stats.RecordUpstreamError(errKindNotFound)
		} else {
			e.Stats.RecordUpstreamError(errKindStatus)
		}
		log.Printf("[proxy] 上游返回 %d: %s", resp.StatusCode, u)
	}
	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	return lastBody, lastStatus, "", "", fmt.Errorf("上游不可用 (status=%d)", lastStatus)
}

func upstreamCandidates(settings *models.Settings, urlPath string) []string {
	candidates := []string{strings.TrimSuffix(settings.UpstreamURL, "/") + urlPath}
	if settings.FallbackURL != "" {
		candidates = append(candidates, strings.TrimSuffix(settings.FallbackURL, "/")+urlPath)
	}
	return candidates
}

// mirrorCandidates 镜像根候选：simple 根去掉结尾的 /simple 后拼 urlPath
// （发行文件位于镜像根的 /packages/ 下，而非 /simple/ 之下）。
func mirrorCandidates(settings *models.Settings, urlPath string) []string {
	root := func(base string) string {
		base = strings.TrimSuffix(base, "/")
		return strings.TrimSuffix(base, "/simple")
	}
	candidates := []string{root(settings.UpstreamURL) + urlPath}
	if settings.FallbackURL != "" {
		candidates = append(candidates, root(settings.FallbackURL)+urlPath)
	}
	return candidates
}

// fileDownloadCandidates 组装发行文件回源候选地址（按尝试顺序）：
// 1. 锚点页记录的来源地址；存量库中的相对地址（历史缺陷落库值，如
//    ../../packages/<hash>/<file>）先按该项目当前上游锚点页解析为绝对地址、
//    排在原样值之前（历史值无需迁移即可自愈）；
// 2. 镜像根猜测兜底保持最后位。
func fileDownloadCandidates(settings *models.Settings, rec *database.FileWithProject) []string {
	candidates := make([]string, 0, 4)
	if rec.SourceURL != "" {
		if u, err := url.Parse(rec.SourceURL); err == nil && u.Scheme == "" {
			base := strings.TrimSuffix(settings.UpstreamURL, "/") + "/" + rec.ProjectName + "/"
			if b, err := url.Parse(base); err == nil {
				candidates = append(candidates, b.ResolveReference(u).String())
			}
		}
		candidates = append(candidates, rec.SourceURL)
	}
	candidates = append(candidates, mirrorCandidates(settings, "/packages/"+rec.Filename)...)
	return candidates
}

// downloadPyFile 从上游下载发行文件到磁盘（临时文件 + 原子重命名），并更新 DB。
// 优先使用锚点页记录的原始地址；sha256 已知时边下边算、落盘前比对（校验链不可省略）。
// ctx 控制整体超时，失败时由调用方 Forget 对应 flight。
func (e *Engine) downloadPyFile(ctx context.Context, settings *models.Settings, rec *database.FileWithProject, destPath string) error {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	candidates := fileDownloadCandidates(settings, rec)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	var lastErr error
	for _, u := range candidates {
		if strings.TrimSpace(u) == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			log.Printf("[proxy] 构建下载请求失败 %s: %v", u, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 下载文件失败 %s: %v", u, err)
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				e.Stats.RecordUpstreamError(errKindNotFound)
			} else {
				e.Stats.RecordUpstreamError(errKindStatus)
			}
			log.Printf("[proxy] 文件下载返回 %d: %s", resp.StatusCode, u)
			lastErr = fmt.Errorf("上游返回 %d", resp.StatusCode)
			continue
		}
		// 下载前磁盘余量预检（Content-Length + 256MB）：磁盘耗尽时尽早失败，
		// 不新增状态码，沿现有 502 文本错误形态返回
		expected := resp.ContentLength // 上游声明长度；被透明解压时为 -1
		if derr := utils.DiskHasRoom(settings.StorageDir, expected); derr != nil {
			resp.Body.Close()
			return derr
		}
		tmp, err := os.CreateTemp(filepath.Dir(destPath), ".pydl-*")
		if err != nil {
			resp.Body.Close()
			return err
		}
		// 已知 sha256 时边下边算，落盘前比对（与 pip 的锚点页校验口径一致）
		digest := sha256.New()
		mw := io.MultiWriter(tmp, digest)
		size, copyErr := io.Copy(mw, resp.Body)
		resp.Body.Close()
		// Close 的写缓存落盘失败（如磁盘满）与复制失败同等处理：损坏文件不得进入缓存
		closeErr := tmp.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			os.Remove(tmp.Name())
			return copyErr
		}
		// 截断检测：声明了长度但实际不符说明连接中断，不能落盘
		if expected > 0 && size != expected {
			os.Remove(tmp.Name())
			e.Stats.RecordUpstreamError(errKindVerify)
			log.Printf("[proxy] 文件长度不符（期望 %d，实际 %d）: %s", expected, size, u)
			lastErr = errors.New("响应长度不符")
			continue
		}
		// 内容校验：已知 sha256 时必须匹配
		if want := rec.SHA256; want != "" {
			if got := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(got, want) {
				os.Remove(tmp.Name())
				e.Stats.RecordUpstreamError(errKindVerify)
				log.Printf("[proxy] sha256 校验失败（期望 %s，实际 %s），已丢弃: %s", want, got, u)
				lastErr = errors.New("sha256 校验失败")
				continue
			}
		}
		if err := renameReplace(tmp.Name(), destPath); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		rel, rerr := filepath.Rel(settings.StorageDir, destPath)
		if rerr != nil {
			return rerr
		}
		return e.DB.SetFileCached(rec.ID, size, filepath.ToSlash(rel))
	}
	if lastErr == nil {
		lastErr = errors.New("所有候选地址均无法提供该文件")
	}
	return lastErr
}

// serveFile 零拷贝传输大文件，并带上强缓存响应头。
// 发行文件按文件名不可变（同一文件名内容恒定，sha256 已在落盘前校验），可以长缓存；
// ETag/Last-Modified 交给 http.ServeContent 处理条件请求，命中时返回 304。
func (e *Engine) serveFile(c *gin.Context, fullPath, filename string) {
	f, err := os.Open(fullPath)
	if err != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取缓存文件失败")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取缓存文件失败")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	// 传入真实 modtime：ServeContent 据此生成 Last-Modified 并处理 If-Modified-Since
	http.ServeContent(c.Writer, c.Request, filename, fi.ModTime(), f)
}

// renameReplace 用 src 覆盖 dst。Windows 下 rename 不能覆盖已存在文件，
// 因此先直接尝试，失败再删目标重试一次（Linux 下第一次即可成功）。
func renameReplace(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}
