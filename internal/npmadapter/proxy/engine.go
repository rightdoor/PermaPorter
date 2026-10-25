// Package proxy 实现核心代理引擎：元数据/tarball 按需懒加载与缓存。
package proxy

import (
	"bytes"
	"container/list"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
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
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/models"
)

// maxPackumentSize packument 响应体大小上限，防止异常上游拖垮内存。
// 真实 packument 最大约 10MB 量级（@types/node），32MB 留足余量；
// 原来的 128MB 在 16 并发下最坏会占用 2GB 内存。
const maxPackumentSize = 32 << 20

// metaUpdateTimeout 元数据拉取的整体超时。
const metaUpdateTimeout = 30 * time.Second

// defaultMetaRefreshTTL 元数据刷新间隔默认值：新版本最迟在该时长后可见。
// 过期后先做条件请求（ETag），上游未变化时只需一次 304，不会重写版本表。
const defaultMetaRefreshTTL = 2 * time.Minute

// metaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先），
// 后台刷新继续执行；下次请求即可拿到新数据。
const metaRefreshWait = 5 * time.Second

// metaRefreshCooldown 刷新失败后的冷却时间，避免上游故障期间每个请求都等待刷新超时。
const metaRefreshCooldown = time.Minute

// metaFailMapMax 失败冷却表条目上限：超过后先清理已过期条目，防止大量一次性失败包长期占用内存。
const metaFailMapMax = 4096

// tarballDownloadTimeout 单个 tarball 下载的整体超时（含候选上游切换），防止 body 传输无限挂起。
const tarballDownloadTimeout = 10 * time.Minute

// upstreamMaxConcurrent 上游并发拉取上限：突发批量安装时限流对上游的请求，避免互相拖垮。
const upstreamMaxConcurrent = 16

// packument 改写缓存上限（LRU）：命中路径直接返回改写结果，省去整份 JSON 的反序列化。
const (
	rewriteCacheMaxEntries   = 64
	rewriteCacheMaxBytes     = 64 << 20
	rewriteCacheMaxItemBytes = 8 << 20
)

var errUpstreamNotFound = errors.New("上游不存在该包")

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

	// MetaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先），
	// 后台刷新继续执行，下次请求即可拿到新数据。默认 metaRefreshWait。
	MetaRefreshWait time.Duration

	// 元数据刷新失败冷却：包名 -> 最近一次失败时间
	failMu    sync.Mutex
	failUntil map[string]time.Time

	// bgWG 后台任务（后台元数据刷新、tarball 下载）等待组：
	// 优雅退出时先等它们结束，再关闭数据库，避免在途下载写库报错。
	bgWG sync.WaitGroup

	// packument 改写结果 LRU 缓存：key 为 pkgName|scheme://host
	rwMu    sync.Mutex
	rwCache map[string]*list.Element
	rwList  *list.List
	rwBytes int
}

type rwEntry struct {
	key string
	out []byte
}

// NewEngine 创建代理引擎。元数据刷新间隔（MetaTTL）读自生态配置行
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
		rwCache:         make(map[string]*list.Element),
		rwList:          list.New(),
	}
}

// Inflight 返回当前正在从上游拉取的资源数（含排队）。
func (e *Engine) Inflight() int { return len(e.gate) }

// Shutdown 等待后台任务（后台元数据刷新、在途 tarball 下载）结束，
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

// ServePackument 处理 GET /{pkg} 元数据请求：
// 本地命中且未过期直接返回；已过期时先同步刷新再返回（保证刚发布的版本第一次请求即可见），
// 刷新失败或超过 MetaRefreshWait 时回退旧缓存，后台刷新继续执行。
func (e *Engine) ServePackument(c *gin.Context, pkgName string) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务未完成初始化"})
		return
	}
	// 先查库判断是否需要刷新，再决定读盘/改写缓存：
	// 过期时若先读盘，刷新成功后这份数据会被整体替换，读盘完全白费。
	if pkg, perr := e.DB.GetPackageLite(pkgName); perr == nil {
		if metaStale(pkg.MetaCachedAt, settings.MetaTTL) && !e.refreshCoolingDown(pkgName) {
			// 缓存过期：先刷新再返回，避免客户端拿到不含新版本的旧元数据（ETARGET）。
			// 失败/超时则回退旧缓存，并在冷却期内跳过重复刷新。
			if fresh, rerr := e.refreshPackument(pkgName, settings, e.MetaRefreshWait); rerr == nil {
				e.serveMetaHit(c, pkgName, fresh, start)
				return
			}
		}
		// 未过期，或刷新失败/超时：优先命中改写缓存，省去磁盘读与整份 JSON 改写。
		if out, ok := e.getRewritten(metaCacheKey(c, pkgName)); ok {
			e.Stats.RecordMeta(true)
			e.DB.TouchPackage(pkgName)
			c.Data(http.StatusOK, "application/json", out)
			e.logReq(c, "meta", pkgName, true, start, "")
			return
		}
		if data, rerr := os.ReadFile(e.metaPath(settings, pkgName)); rerr == nil {
			e.serveMetaHit(c, pkgName, data, start)
			return
		}
	}
	// 冷缓存（或 DB 有记录但文件丢失）：同步拉取
	e.Stats.RecordMeta(false)
	data, err := e.refreshPackument(pkgName, settings, 0)
	if err != nil {
		if errors.Is(err, errUpstreamNotFound) {
			e.logReq(c, "meta", pkgName, false, start, err.Error())
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		e.logReq(c, "meta", pkgName, false, start, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	e.writePackument(c, data, pkgName)
	e.logReq(c, "meta", pkgName, false, start, "")
}

// serveMetaHit 返回命中缓存的元数据（未改写的上游原始 JSON）。
func (e *Engine) serveMetaHit(c *gin.Context, pkgName string, raw []byte, start time.Time) {
	e.Stats.RecordMeta(true)
	e.DB.TouchPackage(pkgName)
	e.writePackument(c, raw, pkgName)
	e.logReq(c, "meta", pkgName, true, start, "")
}

// ServeTarball 处理 GET /{pkg}/-/{filename}.tgz：本地命中零拷贝返回，未命中从上游拉取缓存。
func (e *Engine) ServeTarball(c *gin.Context, pkgName, filename string) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务未完成初始化"})
		return
	}
	version := VersionFromFilename(pkgName, filename)
	var v *models.Version
	if version != "" {
		v, _ = e.DB.GetVersionByName(pkgName, version)
	}
	if v == nil && version != "" {
		// 客户端可能使用本地元数据缓存直接请求 tarball（服务端从未收到元数据请求），
		// 此时同步拉取一次元数据入库，确保该版本能落盘缓存而不是走不缓存的透传。
		if _, err := e.refreshPackument(pkgName, settings, 0); err == nil {
			v, _ = e.DB.GetVersionByName(pkgName, version)
		}
	}
	if v == nil {
		// 无版本记录（如自定义文件名），直接透传不落盘
		e.Stats.RecordTarball(false)
		e.passThrough(c, settings, c.Request.URL.Path)
		return
	}
	fullPath, perr := tarballFullPath(settings, v)
	if perr != nil {
		// 历史库中的越界路径（如被污染上游写入的版本键）：拒绝读取与写入，不做数据迁移
		log.Printf("[proxy] 版本记录路径非法，拒绝服务 %s@%s: %v", pkgName, version, perr)
		e.logReq(c, "tarball", pkgName, false, start, "版本记录路径非法")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "版本记录路径非法"})
		return
	}
	if fi, err := os.Stat(fullPath); err == nil && !fi.IsDir() {
		e.Stats.RecordTarball(true)
		e.DB.TouchPackage(pkgName)
		c.Header("X-Proxy-Cache", "HIT")
		e.logReq(c, "tarball", pkgName, true, start, "")
		e.serveFile(c, fullPath, filename)
		return
	}
	e.Stats.RecordTarball(false)
	key := "tarball:" + pkgName + "@" + version
	_, err, _ = e.sf.Do(key, func() (any, error) {
		// 下载与请求生命周期解耦，整体超时预算（含候选上游切换），失败不再无限挂起
		ctx, cancel := context.WithTimeout(context.Background(), tarballDownloadTimeout)
		defer cancel()
		return nil, e.downloadVersion(ctx, settings, pkgName, v, fullPath)
	})
	if err != nil {
		e.logReq(c, "tarball", pkgName, false, start, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "tarball 拉取失败: " + err.Error()})
		return
	}
	c.Header("X-Proxy-Cache", "MISS")
	e.logReq(c, "tarball", pkgName, false, start, "")
	e.serveFile(c, fullPath, filename)
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

// PassThroughRequest 通用请求透传（如 npm audit 的 POST 安全查询端点）：
// 依次尝试候选上游，收到首个响应（无论状态码）即原样回传、不再尝试后续候选；
// 仅网络层失败才切换下一候选。不落盘缓存。
func (e *Engine) PassThroughRequest(c *gin.Context, urlPath string) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务未完成初始化"})
		return
	}
	// 请求体需在多个候选上游间重放，先读入内存（安全查询请求体通常只有几 KB）
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	release, err := e.acquireUpstream(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "上游并发已满"})
		return
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	for _, base := range upstreamCandidates(settings, urlPath) {
		req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, base, bytes.NewReader(body))
		if err != nil {
			log.Printf("[proxy] 构建透传请求失败 %s: %v", base, err)
			continue
		}
		if ct := c.GetHeader("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if ac := c.GetHeader("Accept"); ac != "" {
			req.Header.Set("Accept", ac)
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[proxy] 透传请求失败 %s: %v", base, err)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			c.Header("Content-Type", ct)
		}
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
		resp.Body.Close()
		e.logReq(c, "audit", urlPath, false, start, "")
		return
	}
	e.logReq(c, "audit", urlPath, false, start, "上游不可用")
	c.JSON(http.StatusBadGateway, gin.H{"error": "上游不可用"})
}

// PreheatPackage 预热单个包：确保元数据已缓存；wantTarball 时再下载 latest 版本 tarball。
// 返回 state："cached"（原本就已就绪）或 "done"（本次新拉取），以及 latest 版本号与 tarball 大小。
func (e *Engine) PreheatPackage(pkgName string, wantTarball bool) (state, version string, size int64, err error) {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return "", "", 0, err
	}
	metaReady := false
	if _, serr := os.Stat(e.metaPath(settings, pkgName)); serr == nil {
		if _, gerr := e.DB.GetPackage(pkgName); gerr == nil {
			metaReady = true
		}
	}
	if !metaReady {
		if _, err := e.refreshPackument(pkgName, settings, 0); err != nil {
			return "", "", 0, err
		}
	}
	pkg, err := e.DB.GetPackage(pkgName)
	if err != nil {
		return "", "", 0, err
	}
	if pkg.LatestVersion == "" {
		return "", "", 0, errors.New("上游元数据无版本信息")
	}
	version = pkg.LatestVersion
	if !wantTarball {
		if metaReady {
			return "cached", version, 0, nil
		}
		return "done", version, 0, nil
	}
	v, err := e.DB.GetVersionByName(pkgName, version)
	if err != nil {
		return "", version, 0, fmt.Errorf("版本记录缺失: %w", err)
	}
	fullPath, perr := tarballFullPath(settings, v)
	if perr != nil {
		return "", version, 0, fmt.Errorf("版本记录路径非法: %w", perr)
	}
	if fi, serr := os.Stat(fullPath); serr == nil && !fi.IsDir() {
		if metaReady {
			return "cached", version, fi.Size(), nil
		}
		return "done", version, fi.Size(), nil
	}
	key := "tarball:" + pkgName + "@" + version
	_, err, _ = e.sf.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), tarballDownloadTimeout)
		defer cancel()
		return nil, e.downloadVersion(ctx, settings, pkgName, v, fullPath)
	})
	if err != nil {
		return "", version, 0, err
	}
	var sz int64
	if fi, serr := os.Stat(fullPath); serr == nil {
		sz = fi.Size()
	}
	return "done", version, sz, nil
}

// ForceRefreshMeta 强制从上游刷新单个包元数据（忽略 TTL），成功后失效改写缓存。
func (e *Engine) ForceRefreshMeta(pkgName string) error {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return err
	}
	// 手动刷新不受失败冷却限制
	e.clearRefreshFailure(pkgName)
	_, err = e.refreshPackument(pkgName, settings, 0)
	return err
}

// logReq 记录一条代理请求日志（环形缓冲，重启清空）。
func (e *Engine) logReq(c *gin.Context, kind, pkg string, hit bool, start time.Time, errMsg string) {
	e.Log.Add(logring.LogEntry{
		Time: time.Now(),
		IP:   c.ClientIP(),
		Kind: kind,
		Pkg:  pkg,
		Hit:  hit,
		Dur:  time.Since(start),
		Err:  errMsg,
	})
}

// ---------- 内部实现 ----------

func (e *Engine) metaPath(settings *models.Settings, pkgName string) string {
	return filepath.Join(settings.StorageDir, "meta", pkgName+".json")
}

// tarballFullPath 版本 tarball 的磁盘绝对路径：TarballPath 源自上游 packument 的
// 版本键，拼路径前必须经 StorageFilePath 兜底确认不越出存储目录（防路径穿越）。
func tarballFullPath(settings *models.Settings, v *models.Version) (string, error) {
	return StorageFilePath(settings.StorageDir, filepath.FromSlash(v.TarballPath))
}

// metaStale 判断元数据缓存时间是否超过刷新 TTL。
func metaStale(metaCachedAt string, ttl time.Duration) bool {
	if metaCachedAt == "" {
		return true
	}
	if ttl <= 0 {
		ttl = defaultMetaRefreshTTL
	}
	for _, f := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
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

func (e *Engine) fetchAndCachePackument(ctx context.Context, settings *models.Settings, pkgName string) ([]byte, error) {
	// 缓存中记录了上游 ETag 时做条件请求：未变化（304）只更新时间戳，避免重写版本表。
	etag := ""
	if p, err := e.DB.GetPackageLite(pkgName); err == nil {
		etag = p.MetaETag
	}
	raw, status, newETag, err := e.fetchJSON(ctx, settings, "/"+pkgName, etag)
	if status == http.StatusNotModified {
		if data, rerr := os.ReadFile(e.metaPath(settings, pkgName)); rerr == nil {
			e.DB.TouchMetaCachedAt(pkgName)
			return data, nil
		}
		// 缓存文件丢失（如被手工删除）：忽略条件头重新拉取
		raw, status, newETag, err = e.fetchJSON(ctx, settings, "/"+pkgName, "")
	}
	if err != nil {
		if status == http.StatusNotFound {
			return nil, errUpstreamNotFound
		}
		return nil, fmt.Errorf("上游请求失败: %w", err)
	}
	stored, err := e.cachePackument(settings, pkgName, raw, newETag)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// refreshPackument 同步刷新单个包元数据，并用 singleflight 归并同包并发请求。
// wait > 0 时最多等待 wait 时长：超时返回 errMetaRefreshTimeout，但后台刷新继续执行，
// 调用方可先回退旧缓存；wait <= 0 表示一直等到刷新完成（受 metaUpdateTimeout 约束）。
func (e *Engine) refreshPackument(pkgName string, settings *models.Settings, wait time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		key := "meta:" + pkgName
		val, err, _ := e.sf.Do(key, func() (any, error) {
			// 与请求生命周期解耦：客户端断开不会连坐同包全部等待者
			ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
			defer cancel()
			return e.fetchAndCachePackument(ctx, settings, pkgName)
		})
		if err != nil {
			e.markRefreshFailed(pkgName)
			done <- result{err: err}
			return
		}
		e.clearRefreshFailure(pkgName)
		data, _ := val.([]byte)
		done <- result{data: data}
	}()

	if wait <= 0 {
		r := <-done
		return r.data, r.err
	}
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(wait):
		// 超时瞬间后台可能刚好完成：再取一次，避免把成功的刷新误标为失败（否则会白等一分钟冷却）。
		select {
		case r := <-done:
			return r.data, r.err
		default:
		}
		// 本次先返回旧缓存；冷却一小段时间，避免后续请求继续排队等待同一个慢刷新
		// （后台刷新成功后会清除冷却标记）。
		e.markRefreshFailed(pkgName)
		log.Printf("[proxy] 元数据刷新超过 %s，先返回旧缓存: %s", wait, pkgName)
		return nil, errMetaRefreshTimeout
	}
}

// refreshCoolingDown 判断该包是否处于刷新失败冷却期。
func (e *Engine) refreshCoolingDown(pkgName string) bool {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	until, ok := e.failUntil[pkgName]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(e.failUntil, pkgName)
		return false
	}
	return true
}

func (e *Engine) markRefreshFailed(pkgName string) {
	now := time.Now()
	e.failMu.Lock()
	defer e.failMu.Unlock()
	// 一次性失败大量包时避免 map 无限增长：达到上限先清掉已过期条目
	if len(e.failUntil) >= metaFailMapMax {
		for name, until := range e.failUntil {
			if now.After(until) {
				delete(e.failUntil, name)
			}
		}
	}
	e.failUntil[pkgName] = now.Add(metaRefreshCooldown)
}

func (e *Engine) clearRefreshFailure(pkgName string) {
	e.failMu.Lock()
	delete(e.failUntil, pkgName)
	e.failMu.Unlock()
}

func (e *Engine) cachePackument(settings *models.Settings, pkgName string, raw []byte, etag string) ([]byte, error) {
	pkg, versions, err := ExtractPackument(raw, pkgName)
	if err != nil {
		return nil, err
	}
	pkg.MetaETag = etag
	metaFile := e.metaPath(settings, pkgName)
	if err := os.MkdirAll(filepath.Dir(metaFile), 0o755); err != nil {
		return nil, err
	}
	// 落盘内容把 dist.tarball 写成哨兵地址：响应时一次字节替换即可，
	// 不必每请求反序列化整份 JSON，也无需按 host 分别存储。
	stored := raw
	if s, serr := SentinelizePackument(raw, pkgName); serr == nil {
		stored = s
	}
	// 原子写：避免并发刷新时读到写了一半的 JSON（客户端会表现为解析失败/找不到版本）
	if err := writeFileAtomic(metaFile, stored, 0o644); err != nil {
		return nil, err
	}
	pkgID, err := e.DB.UpsertPackage(pkg)
	if err != nil {
		return nil, err
	}
	if err := e.DB.UpsertVersions(pkgID, versions); err != nil {
		return nil, err
	}
	// 元数据已更新，失效该包的改写缓存
	e.invalidateRewritten(pkgName)
	return stored, nil
}

// writeFileAtomic 先写临时文件再重命名，保证读方永远看到完整内容。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".meta-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 重命名成功后此处为无害的空操作
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return renameReplace(tmpName, path)
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

// metaCacheKey 生成改写缓存 key：同一包按请求方 host 分别缓存（tarball 地址与 host 相关）。
func metaCacheKey(c *gin.Context, pkgName string) string {
	return pkgName + "|" + requestBase(c)
}

// requestBase 推断客户端可访问的站点根地址（兼容反代）。
// 反代头仅在直连 peer 为本机可信代理时采信（见 webutil.ExternalScheme）。
func requestBase(c *gin.Context) string {
	scheme := webutil.ExternalScheme(c)
	host := webutil.ForwardedHost(c)
	return scheme + "://" + host
}

func (e *Engine) writePackument(c *gin.Context, raw []byte, pkgName string) {
	base := requestBase(c)
	key := metaCacheKey(c, pkgName)
	if out, ok := e.getRewritten(key); ok {
		c.Data(http.StatusOK, "application/json", out)
		return
	}
	// 快速路径：落盘内容带哨兵地址，一次字节替换即可（不做 JSON 解析）
	out, ok := ApplyBase(raw, base)
	if !ok {
		// 历史缓存（上游原始地址）或异常 JSON：回退整份 JSON 改写
		rewritten, err := RewritePackument(raw, pkgName, base)
		if err != nil {
			c.Data(http.StatusOK, "application/json", raw)
			return
		}
		out = rewritten
	}
	e.putRewritten(key, out)
	c.Data(http.StatusOK, "application/json", out)
}

// ---------- packument 改写缓存（LRU） ----------

func (e *Engine) getRewritten(key string) ([]byte, bool) {
	e.rwMu.Lock()
	defer e.rwMu.Unlock()
	if el, ok := e.rwCache[key]; ok {
		e.rwList.MoveToFront(el)
		return el.Value.(*rwEntry).out, true
	}
	return nil, false
}

func (e *Engine) putRewritten(key string, out []byte) {
	if len(out) > rewriteCacheMaxItemBytes {
		return
	}
	e.rwMu.Lock()
	defer e.rwMu.Unlock()
	if el, ok := e.rwCache[key]; ok {
		ent := el.Value.(*rwEntry)
		e.rwBytes += len(out) - len(ent.out)
		ent.out = out
		e.rwList.MoveToFront(el)
		e.evictRewrittenLocked()
		return
	}
	el := e.rwList.PushFront(&rwEntry{key: key, out: out})
	e.rwCache[key] = el
	e.rwBytes += len(out)
	e.evictRewrittenLocked()
}

// evictRewrittenLocked 淘汰超限条目（调用方需持 rwMu）。
func (e *Engine) evictRewrittenLocked() {
	for e.rwBytes > rewriteCacheMaxBytes || e.rwList.Len() > rewriteCacheMaxEntries {
		back := e.rwList.Back()
		if back == nil {
			return
		}
		ent := back.Value.(*rwEntry)
		e.rwBytes -= len(ent.out)
		e.rwList.Remove(back)
		delete(e.rwCache, ent.key)
	}
}

// invalidateRewritten 元数据刷新后失效该包的全部改写缓存。
func (e *Engine) invalidateRewritten(pkgName string) {
	e.rwMu.Lock()
	defer e.rwMu.Unlock()
	prefix := pkgName + "|"
	for el := e.rwList.Front(); el != nil; {
		next := el.Next()
		ent := el.Value.(*rwEntry)
		if strings.HasPrefix(ent.key, prefix) {
			e.rwBytes -= len(ent.out)
			e.rwList.Remove(el)
			delete(e.rwCache, ent.key)
		}
		el = next
	}
}

// fetchJSON 依次尝试主/备用上游，返回首个 200 响应。
// etag 非空时仅对主上游发送 If-None-Match：内容未变化时返回 304（body 为空），
// 避免重新下载并重写整张版本表。备用上游内容可能不同，不带条件头。
func (e *Engine) fetchJSON(ctx context.Context, settings *models.Settings, urlPath, etag string) (body []byte, status int, newETag string, err error) {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "", fmt.Errorf("上游并发已满: %w", err)
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
			return nil, http.StatusNotModified, etag, nil
		}
		// 读 max+1 字节判断是否超限：恰好 max 时无法区分"正好达到上限"与"被截断"
		var rerr error
		body, rerr = io.ReadAll(io.LimitReader(resp.Body, maxPackumentSize+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			// 读取中断得到的是截断体：不能当成功返回（可能被解析入库）
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 读取上游响应失败 %s: %v", u, rerr)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		if int64(len(body)) > maxPackumentSize {
			// 超限响应按上游错误处理：截断的 packument 可能静默丢失尾部版本，绝不落盘
			log.Printf("[proxy] 上游响应超过大小上限 %d: %s", maxPackumentSize, u)
			e.Stats.RecordUpstreamError(errKindOversize)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		lastBody, lastStatus = body, resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			return body, resp.StatusCode, resp.Header.Get("ETag"), nil
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
	return lastBody, lastStatus, "", fmt.Errorf("上游不可用 (status=%d)", lastStatus)
}

func upstreamCandidates(settings *models.Settings, urlPath string) []string {
	candidates := []string{strings.TrimSuffix(settings.UpstreamURL, "/") + urlPath}
	if settings.FallbackURL != "" {
		candidates = append(candidates, strings.TrimSuffix(settings.FallbackURL, "/")+urlPath)
	}
	return candidates
}

// downloadVersion 从上游下载 tarball 到磁盘（临时文件 + 原子重命名），并更新 DB。
// ctx 控制整体超时（含候选上游切换），失败时由调用方 Forget 对应 flight。
func (e *Engine) downloadVersion(ctx context.Context, settings *models.Settings, pkgName string, v *models.Version, destPath string) error {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	conventional := ConventionalFilename(pkgName, v.Version)
	candidates := upstreamCandidates(settings, "/"+pkgName+"/-/"+conventional)
	if v.TarballURL != "" {
		candidates = append(candidates, v.TarballURL)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			log.Printf("[proxy] 构建下载请求失败 %s: %v", u, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 下载 tarball 失败 %s: %v", u, err)
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
			log.Printf("[proxy] tarball 下载返回 %d: %s", resp.StatusCode, u)
			continue
		}
		// 下载前磁盘余量预检（Content-Length + 256MB）：磁盘耗尽时尽早失败，
		// 不新增状态码，沿现有 502 错误形态返回
		expected := resp.ContentLength // 上游声明长度；被透明解压时为 -1
		if err := utils.DiskHasRoom(settings.StorageDir, expected); err != nil {
			resp.Body.Close()
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(destPath), ".download-*")
		if err != nil {
			resp.Body.Close()
			return err
		}
		// 已知 sha256/integrity 时边下边算，落盘前比对
		var w io.Writer = tmp
		var hasher hash.Hash
		var wantSum []byte
		if algo, want, ok := parseIntegrity(v.Integrity); ok {
			if h := newIntegrityHash(algo); h != nil {
				hasher, wantSum = h, want
				w = io.MultiWriter(tmp, h)
			}
		}
		size, copyErr := io.Copy(w, resp.Body)
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
			log.Printf("[proxy] tarball 长度不符（期望 %d，实际 %d）: %s", expected, size, u)
			continue
		}
		// 内容校验：tarball 必须是 gzip；有 integrity 时还要摘要匹配
		if !isGzipFile(tmp.Name()) {
			os.Remove(tmp.Name())
			e.Stats.RecordUpstreamError(errKindVerify)
			log.Printf("[proxy] tarball 内容不是 gzip，已丢弃: %s", u)
			continue
		}
		if hasher != nil && !hmac.Equal(hasher.Sum(nil), wantSum) {
			os.Remove(tmp.Name())
			e.Stats.RecordUpstreamError(errKindVerify)
			log.Printf("[proxy] tarball integrity 校验失败，已丢弃: %s", u)
			continue
		}
		if err := renameReplace(tmp.Name(), destPath); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		return e.DB.SetVersionFile(v.ID, size)
	}
	return errors.New("所有上游均无法提供该 tarball")
}

// serveFile 零拷贝传输大文件，并带上强缓存响应头。
// tarball 按「包名 + 版本」不可变（npm 不允许同版本重新发布），可以长缓存；
// ETag/Last-Modified 交给 http.ServeContent 处理条件请求，命中时返回 304。
func (e *Engine) serveFile(c *gin.Context, fullPath, filename string) {
	f, err := os.Open(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取缓存文件失败: " + err.Error()})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取缓存文件失败: " + err.Error()})
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	// 传入真实 modtime：ServeContent 据此生成 Last-Modified 并处理 If-Modified-Since
	http.ServeContent(c.Writer, c.Request, filename, fi.ModTime(), f)
}

// passThrough 无版本记录时的透明转发（不缓存）。
func (e *Engine) passThrough(c *gin.Context, settings *models.Settings, urlPath string) {
	start := time.Now()
	release, err := e.acquireUpstream(c.Request.Context())
	if err != nil {
		e.logReq(c, "pass", urlPath, false, start, "上游并发已满")
		c.JSON(http.StatusBadGateway, gin.H{"error": "上游并发已满"})
		return
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	for _, base := range upstreamCandidates(settings, urlPath) {
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, base, nil)
		if err != nil {
			log.Printf("[proxy] 构建透传请求失败 %s: %v", base, err)
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[proxy] 透传请求失败 %s: %v", base, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}
		defer resp.Body.Close()
		c.Header("Content-Type", "application/octet-stream")
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, resp.Body)
		e.logReq(c, "pass", urlPath, false, start, "")
		return
	}
	e.logReq(c, "pass", urlPath, false, start, "上游不可用")
	c.JSON(http.StatusBadGateway, gin.H{"error": "上游不可用"})
}
