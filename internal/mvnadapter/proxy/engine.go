// Package proxy 的引擎实现：maven-metadata.xml 元数据（TTL + ETag 条件刷新）与
// 构件文件（不可变，sha1 校验链，永久缓存）的按需懒加载。
package proxy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
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
	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/models"
)

// maxMetaSize maven-metadata.xml 响应体大小上限，防止异常上游拖垮内存。
// 真实 metadata 最大约百 KB 量级（多版本构件），4MB 留足余量。
const maxMetaSize = 4 << 20

// maxChecksumSize 上游 .sha1/.md5 校验文件的大小上限（正常 32~128 字节）。
const maxChecksumSize = 1024

// metaUpdateTimeout 元数据拉取的整体超时。
const metaUpdateTimeout = 30 * time.Second

// defaultMetaRefreshTTL 元数据刷新间隔默认值：新版本最迟在该时长后可见。
// 过期后先做条件请求（ETag），上游未变化时只需一次 304，不重写缓存。
const defaultMetaRefreshTTL = 2 * time.Minute

// metaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先），
// 后台刷新继续执行；下次请求即可拿到新数据。
const metaRefreshWait = 5 * time.Second

// metaRefreshCooldown 刷新失败后的冷却时间，避免上游故障期间每个请求都等待刷新超时。
const metaRefreshCooldown = time.Minute

// metaFailMapMax 失败冷却表条目上限：超过后先清理已过期条目，防止大量一次性失败路径长期占用内存。
const metaFailMapMax = 4096

// fileDownloadTimeout 单个构件文件下载的整体超时（大 jar/uber-jar 可达数百 MB）。
const fileDownloadTimeout = 30 * time.Minute

// upstreamMaxConcurrent 上游并发拉取上限：突发批量构建时限流对上游的请求，避免互相拖垮。
const upstreamMaxConcurrent = 16

// dbTimeFormat DB 时间串格式（UTC），与数据库层口径一致。
const dbTimeFormat = "2006-01-02 15:04:05"

var errUpstreamNotFound = errors.New("上游不存在该构件或文件")

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

	// 元数据刷新失败冷却：仓库相对路径 -> 最近一次失败时间
	failMu    sync.Mutex
	failUntil map[string]time.Time

	// bgWG 后台任务（后台元数据刷新、构件下载）等待组：
	// 优雅退出时先等它们结束，再关闭数据库，避免在途下载写库报错。
	bgWG sync.WaitGroup
}

func dbTimeFormatNow() string { return time.Now().UTC().Format(dbTimeFormat) }

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
	}
}

// Inflight 返回当前正在从上游拉取的资源数（含排队）。
func (e *Engine) Inflight() int { return len(e.gate) }

// Shutdown 等待后台任务（后台元数据刷新、在途构件下载）结束，
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
		log.Printf("[mvn-proxy] 退出时仍有后台任务未完成，超时放弃等待")
	}
}

// ---------- maven-metadata.xml ----------

// ServeMetadata 处理 GET /<path>/maven-metadata.xml（构件级与版本级共用）：
// 本地命中且未过期直接返回；已过期时先同步刷新再返回，刷新失败或超过
// MetaRefreshWait 时回退旧缓存，后台刷新继续执行。
// raw XML 原样透传（不含绝对地址，协议安全），命中判定走 mvn_metadata 表。
func (e *Engine) ServeMetadata(c *gin.Context, rawRepoPath string) {
	start := time.Now()
	repoPath, ok := ParseMetaRepoPath(rawRepoPath)
	if !ok {
		e.proxyError(c, http.StatusBadRequest, "非法的元数据路径")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "meta", repoPath, false, start, "未初始化")
		return
	}
	if entry, merr := e.DB.GetMetaEntry(repoPath); merr == nil {
		if metaStale(entry.CachedAt, settings.MetaTTL) && !e.refreshCoolingDown(repoPath) {
			// 缓存过期：先刷新再返回，避免客户端拿到不含新版本的旧清单。
			// 失败/超时则回退旧缓存，并在冷却期内跳过重复刷新。
			if rerr := e.refreshMetadata(repoPath, settings, e.MetaRefreshWait); rerr == nil {
				if fresh, ferr := e.DB.GetMetaEntry(repoPath); ferr == nil {
					e.serveMetaEntry(c, fresh, true, start)
					return
				}
			}
		}
		e.serveMetaEntry(c, entry, true, start)
		return
	}
	// 冷缓存：同步拉取。命中/未命中计数由 serveMetaEntry 统一记录。
	if err := e.refreshMetadata(repoPath, settings, 0); err != nil {
		e.logReq(c, "meta", repoPath, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, http.StatusNotFound, err.Error())
		} else {
			e.proxyError(c, http.StatusBadGateway, err.Error())
		}
		return
	}
	entry, err := e.DB.GetMetaEntry(repoPath)
	if err != nil {
		e.proxyError(c, http.StatusInternalServerError, "读取元数据失败")
		return
	}
	e.serveMetaEntry(c, entry, false, start)
}

// serveMetaEntry 输出缓存的 raw XML（application/xml）。
func (e *Engine) serveMetaEntry(c *gin.Context, entry *models.MetaEntry, hit bool, start time.Time) {
	e.Stats.RecordMetaHit(hit)
	if hit {
		if m, err := ParseMetadataXML([]byte(entry.XML)); err == nil && m.GroupID != "" && m.ArtifactID != "" {
			e.DB.TouchArtifact(m.GroupID, m.ArtifactID)
		}
	}
	c.Header("X-Proxy-Cache", map[bool]string{true: "HIT", false: "MISS"}[hit])
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/xml", []byte(entry.XML))
	e.logReq(c, "meta", entry.RepoPath, hit, start, "")
}

// ForceRefreshArtifactMeta 强制刷新构件级 metadata（忽略 TTL 与失败冷却）。
func (e *Engine) ForceRefreshArtifactMeta(groupID, artifactID string) error {
	if !ValidGroupArtifact(groupID, artifactID) {
		return errors.New("非法的构件坐标")
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		return err
	}
	repoPath := ArtifactMetaPath(groupID, artifactID)
	e.clearRefreshFailure(repoPath)
	return e.refreshMetadata(repoPath, settings, 0)
}

// ---------- 构件文件 ----------

// ServeArtifactFile 处理 GET /<g>/<a>/<v>/<file>：
// 本地命中零拷贝返回；未命中回源（上游 .sha1 可得时强制校验），永久缓存。
func (e *Engine) ServeArtifactFile(c *gin.Context, rawRepoPath string) {
	start := time.Now()
	pf, ok := ParseFileRepoPath(rawRepoPath)
	if !ok {
		e.proxyError(c, http.StatusBadRequest, "非法的构件路径")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "file", pf.RepoPath, false, start, "未初始化")
		return
	}
	if fullPath, rec, cached := e.lookupCachedFile(settings, pf); cached {
		e.Stats.RecordFileHit(true)
		e.DB.TouchArtifact(rec.GroupID, rec.ArtifactID)
		c.Header("X-Proxy-Cache", "HIT")
		e.logReq(c, "file", pf.RepoPath, true, start, "")
		e.serveFile(c, fullPath, pf.Filename)
		return
	}
	// 未命中先计数再下载（下载失败也计入未命中，与 py 侧口径一致）
	e.Stats.RecordFileHit(false)
	fullPath, err := e.downloadArtifactSync(settings, pf)
	if err != nil {
		e.logReq(c, "file", pf.RepoPath, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, http.StatusNotFound, err.Error())
		} else {
			e.proxyError(c, http.StatusBadGateway, "构件拉取失败: "+err.Error())
		}
		return
	}
	c.Header("X-Proxy-Cache", "MISS")
	e.logReq(c, "file", pf.RepoPath, false, start, "")
	e.serveFile(c, fullPath, pf.Filename)
}

// lookupCachedFile 本地命中查找：DB 记录已缓存且磁盘文件在，返回磁盘全路径与记录。
// 路径越界记录、零字节文件一律视为未命中（重新回源），与清理器复位语义闭环。
func (e *Engine) lookupCachedFile(settings *models.Settings, pf *ParsedFile) (string, *database.FileWithArtifact, bool) {
	rec, err := e.DB.GetFileByRepoPath(pf.RepoPath)
	if err != nil || rec.FilePath == "" || rec.FileSizeBytes <= 0 {
		return "", nil, false
	}
	fullPath, perr := StorageFilePath(settings.StorageDir, filepath.FromSlash(rec.FilePath))
	if perr != nil {
		return "", nil, false
	}
	if fi, serr := os.Stat(fullPath); serr == nil && !fi.IsDir() && fi.Size() > 0 {
		return fullPath, rec, true
	}
	return "", nil, false
}

// ensureArtifactFile 确保构件文件已缓存，返回磁盘全路径与命中标记。
// 不记录请求统计与访问时间：命中率只属于协议请求路径，预热复用本方法不得污染。
func (e *Engine) ensureArtifactFile(settings *models.Settings, pf *ParsedFile) (string, bool, error) {
	if fullPath, _, cached := e.lookupCachedFile(settings, pf); cached {
		return fullPath, true, nil
	}
	fullPath, err := e.downloadArtifactSync(settings, pf)
	if err != nil {
		return "", false, err
	}
	return fullPath, false, nil
}

// downloadArtifactSync 归并下载（singleflight：同路径并发请求只触发一次上游拉取），
// 返回磁盘全路径。
func (e *Engine) downloadArtifactSync(settings *models.Settings, pf *ParsedFile) (string, error) {
	fullPath, perr := StorageFilePath(settings.StorageDir, filepath.FromSlash(pf.RepoPath))
	if perr != nil {
		return "", errors.New("构件路径非法")
	}
	key := "file:" + pf.RepoPath
	_, err, _ := e.sf.Do(key, func() (any, error) {
		// 下载与请求生命周期解耦，整体超时预算，失败不再无限挂起
		ctx, cancel := context.WithTimeout(context.Background(), fileDownloadTimeout)
		defer cancel()
		return nil, e.downloadArtifact(ctx, settings, pf, fullPath)
	})
	if err != nil {
		return "", err
	}
	return fullPath, nil
}

// ---------- 校验和端点 ----------

// ServeChecksum 处理 GET <资源>.sha1/.md5/.sha256/.sha512：
// 确保目标资源已缓存后由本地内容即时计算摘要（与实际服务内容永远一致），
// 不落盘、不回源校验文件本身。rawPath 为带校验和后缀的原始路径。
func (e *Engine) ServeChecksum(c *gin.Context, rawPath string) {
	start := time.Now()
	target, algo, ok := ParseChecksumSuffix(rawPath)
	if !ok {
		e.proxyError(c, http.StatusBadRequest, "非法的校验和路径")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "file", target, false, start, "未初始化")
		return
	}
	var sum string
	var hit bool
	if repoPath, isMeta := ParseMetaRepoPath(target); isMeta {
		// 元数据校验和随元数据本体口径：缓存存在即命中，缺失时同步拉取（TTL 刷新由本体路径负责）
		entry, merr := e.DB.GetMetaEntry(repoPath)
		hit = merr == nil
		if !hit {
			if rerr := e.refreshMetadata(repoPath, settings, 0); rerr != nil {
				e.checksumError(c, target, start, rerr)
				return
			}
			if entry, merr = e.DB.GetMetaEntry(repoPath); merr != nil {
				e.checksumError(c, target, start, merr)
				return
			}
		}
		e.Stats.RecordMetaHit(hit)
		if sum, err = digestString(algo, []byte(entry.XML)); err != nil {
			e.checksumError(c, target, start, err)
			return
		}
	} else if pf, isFile := ParseFileRepoPath(target); isFile {
		fullPath, rec, cached := e.lookupCachedFile(settings, pf)
		hit = cached
		if cached {
			e.Stats.RecordFileHit(true)
			e.DB.TouchArtifact(rec.GroupID, rec.ArtifactID)
		} else {
			e.Stats.RecordFileHit(false)
			if fullPath, err = e.downloadArtifactSync(settings, pf); err != nil {
				e.checksumError(c, target, start, err)
				return
			}
		}
		if sum, err = digestFile(algo, fullPath); err != nil {
			e.checksumError(c, target, start, err)
			return
		}
	} else {
		e.proxyError(c, http.StatusBadRequest, "非法的校验和目标路径")
		return
	}
	c.Header("X-Proxy-Cache", map[bool]string{true: "HIT", false: "MISS"}[hit])
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(sum))
	e.logReq(c, "file", target+"."+string(algo), hit, start, "")
}

func (e *Engine) checksumError(c *gin.Context, target string, start time.Time, err error) {
	e.logReq(c, "file", target, false, start, err.Error())
	if errors.Is(err, errUpstreamNotFound) {
		e.proxyError(c, http.StatusNotFound, err.Error())
		return
	}
	e.proxyError(c, http.StatusBadGateway, "校验目标拉取失败: "+err.Error())
}

// ---------- 预热 ----------

// PreheatArtifact 预热单个构件：确保构件级 metadata 已缓存；wantFiles 时再下载
// version（空串取 release/latest）的 .pom 与主 .jar（指定 classifier 时另加该文件）。
// 返回 state："cached"（原本就已就绪）或 "done"（本次新拉取），以及版本号与文件总大小。
func (e *Engine) PreheatArtifact(groupID, artifactID, version, classifier string, wantFiles bool) (state, resolvedVersion string, size int64, err error) {
	if !ValidGroupArtifact(groupID, artifactID) {
		return "", "", 0, errors.New("非法的构件坐标")
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		return "", "", 0, err
	}
	repoPath := ArtifactMetaPath(groupID, artifactID)
	metaReady := true
	if _, merr := e.DB.GetMetaEntry(repoPath); merr != nil {
		metaReady = false
		if rerr := e.refreshMetadata(repoPath, settings, 0); rerr != nil {
			return "", "", 0, rerr
		}
	}
	art, err := e.DB.GetArtifact(groupID, artifactID)
	if err != nil {
		// 元数据 XML 坐标与请求坐标不一致（如版本级 metadata、镜像内容异常）时
		// 构件行可能缺失：补一条最小行再读，预热不应因此整体失败
		if _, uerr := e.DB.UpsertArtifact(&models.Artifact{GroupID: groupID, ArtifactID: artifactID}); uerr != nil {
			return "", "", 0, uerr
		}
		if art, err = e.DB.GetArtifact(groupID, artifactID); err != nil {
			return "", "", 0, err
		}
	}
	if version == "" {
		version = firstNonEmpty(art.ReleaseVersion, art.LatestVersion)
	}
	if version == "" {
		return "", "", 0, errors.New("上游元数据无可用版本")
	}
	resolvedVersion = version
	if !wantFiles {
		if metaReady {
			return "cached", version, 0, nil
		}
		return "done", version, 0, nil
	}
	groupPath := strings.ReplaceAll(groupID, ".", "/")
	baseName := artifactID + "-" + version
	repoPaths := []string{
		fmt.Sprintf("%s/%s/%s/%s.pom", groupPath, artifactID, version, baseName),
	}
	if classifier != "" {
		repoPaths = append(repoPaths, fmt.Sprintf("%s/%s/%s/%s-%s.jar", groupPath, artifactID, version, baseName, classifier))
	} else {
		repoPaths = append(repoPaths, fmt.Sprintf("%s/%s/%s/%s.jar", groupPath, artifactID, version, baseName))
	}
	for _, rp := range repoPaths {
		pf, ok := ParseFileRepoPath(rp)
		if !ok {
			return "", version, size, fmt.Errorf("构件路径非法: %s", rp)
		}
		// 预热走 ensureArtifactFile：享受本地缓存但不计入请求统计（命中率只属于协议请求路径）
		fullPath, _, err := e.ensureArtifactFile(settings, pf)
		if err != nil {
			return "", version, size, fmt.Errorf("下载 %s 失败: %w", pf.Filename, err)
		}
		if st, serr := os.Stat(fullPath); serr == nil && !st.IsDir() {
			size += st.Size()
		}
	}
	if metaReady {
		return "cached", version, size, nil
	}
	return "done", version, size, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------- 出站连通性测试 ----------

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

// ---------- 日志与错误 ----------

// logReq 记录一条代理请求日志（环形缓冲，重启清空）。
func (e *Engine) logReq(c *gin.Context, kind, name string, hit bool, start time.Time, errMsg string) {
	ip := ""
	if c != nil {
		ip = c.ClientIP()
	}
	e.Log.Add(logring.LogEntry{
		Time: time.Now(),
		IP:   ip,
		Kind: kind,
		Pkg:  name,
		Hit:  hit,
		Dur:  time.Since(start),
		Err:  errMsg,
	})
}

// proxyError 统一协议错误输出：9094 一律 text/plain; charset=utf-8。
func (e *Engine) proxyError(c *gin.Context, code int, msg string) {
	c.Data(code, "text/plain; charset=utf-8", []byte(msg))
}

// ---------- 内部实现 ----------

// metaStale 判断元数据缓存时间是否超过刷新 TTL。
func metaStale(cachedAt string, ttl time.Duration) bool {
	if cachedAt == "" {
		return true
	}
	if ttl <= 0 {
		ttl = defaultMetaRefreshTTL
	}
	for _, f := range []string{dbTimeFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, cachedAt); err == nil {
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

// upstreamURL 拼接上游仓库根与仓库相对路径。
func upstreamURL(base, repoPath string) string {
	return strings.TrimSuffix(base, "/") + "/" + repoPath
}

// upstreamCandidates 主/备用上游候选（404 不切换，由调用方按状态码决定）。
func upstreamCandidates(settings *models.Settings, repoPath string) []string {
	candidates := []string{upstreamURL(settings.UpstreamURL, repoPath)}
	if settings.FallbackURL != "" {
		candidates = append(candidates, upstreamURL(settings.FallbackURL, repoPath))
	}
	return candidates
}

// refreshMetadata 同步刷新一条 metadata 缓存，并用 singleflight 归并同路径并发请求。
// wait > 0 时最多等待 wait 时长：超时返回 errMetaRefreshTimeout，但后台刷新继续执行，
// 调用方可先回退旧缓存；wait <= 0 表示一直等到刷新完成（受 metaUpdateTimeout 约束）。
func (e *Engine) refreshMetadata(repoPath string, settings *models.Settings, wait time.Duration) error {
	type result struct {
		err error
	}
	done := make(chan result, 1)
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		key := "meta:" + repoPath
		_, err, _ := e.sf.Do(key, func() (any, error) {
			// 与请求生命周期解耦：客户端断开不会连坐同路径全部等待者
			ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
			defer cancel()
			return nil, e.fetchAndCacheMetadata(ctx, settings, repoPath)
		})
		if err != nil {
			e.markRefreshFailed(repoPath)
			done <- result{err: err}
			return
		}
		e.clearRefreshFailure(repoPath)
		done <- result{}
	}()

	if wait <= 0 {
		r := <-done
		return r.err
	}
	select {
	case r := <-done:
		return r.err
	case <-time.After(wait):
		// 超时瞬间后台可能刚好完成：再取一次，避免把成功的刷新误标为失败（否则会白等一分钟冷却）。
		select {
		case r := <-done:
			return r.err
		default:
		}
		// 本次先返回旧缓存；冷却一小段时间，避免后续请求继续排队等待同一个慢刷新
		//（后台刷新成功后会清除冷却标记）。
		e.markRefreshFailed(repoPath)
		log.Printf("[mvn-proxy] 元数据刷新超过 %s，先返回旧缓存: %s", wait, repoPath)
		return errMetaRefreshTimeout
	}
}

// fetchAndCacheMetadata 拉取并缓存一条 metadata（条件请求 + 解析记账）。
func (e *Engine) fetchAndCacheMetadata(ctx context.Context, settings *models.Settings, repoPath string) error {
	// 缓存中记录了上游 ETag 时做条件请求：未变化（304）只更新时间戳，避免重写缓存。
	etag := ""
	if entry, err := e.DB.GetMetaEntry(repoPath); err == nil {
		etag = entry.ETag
	}
	body, status, newETag, err := e.fetchUpstream(ctx, settings, repoPath, etag, maxMetaSize)
	if status == http.StatusNotModified {
		e.DB.TouchMetaEntry(repoPath)
		// 构件级 metadata 304 时同步推进构件行的元数据时间戳（解析缓存 XML 判定归属）
		if entry, gerr := e.DB.GetMetaEntry(repoPath); gerr == nil {
			if m, perr := ParseMetadataXML([]byte(entry.XML)); perr == nil && m.IsArtifactLevel() {
				e.DB.TouchMetaCachedAt(m.GroupID, m.ArtifactID)
			}
		}
		return nil
	}
	if err != nil {
		if status == http.StatusNotFound {
			return errUpstreamNotFound
		}
		return fmt.Errorf("上游请求失败: %w", err)
	}
	// 解析记账：groupId/artifactId 以 XML 内容为准；构件级（带版本清单）补齐版本信息
	m, perr := ParseMetadataXML(body)
	if perr != nil {
		return perr
	}
	if m.GroupID != "" && m.ArtifactID != "" {
		art := &models.Artifact{GroupID: m.GroupID, ArtifactID: m.ArtifactID}
		if m.IsArtifactLevel() {
			art.LatestVersion = m.Versioning.Latest
			art.ReleaseVersion = m.Versioning.Release
			art.VersionCount = int64(len(m.Versioning.Versions.Version))
			art.LastUpdated = m.Versioning.LastUpdated
			art.MetaCachedAt = dbTimeFormatNow()
		}
		if _, uerr := e.DB.UpsertArtifact(art); uerr != nil {
			return uerr
		}
	}
	return e.DB.SaveMetaEntry(repoPath, string(body), newETag)
}

// fetchUpstream 依次尝试主/备用上游，返回首个 200 响应体。
// etag 非空时仅对主上游发送 If-None-Match：内容未变化时返回 304（body 为空）。
// 备用上游内容可能不同，不带条件头。
func (e *Engine) fetchUpstream(ctx context.Context, settings *models.Settings, repoPath, etag string, limit int64) (body []byte, status int, newETag string, err error) {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "", fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	var lastBody []byte
	lastStatus := 0
	for i, u := range upstreamCandidates(settings, repoPath) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rerr != nil {
			log.Printf("[mvn-proxy] 构建请求失败 %s: %v", u, rerr)
			continue
		}
		if i == 0 && etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, derr := client.Do(req)
		if derr != nil {
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[mvn-proxy] 请求上游失败 %s: %v", u, derr)
			continue
		}
		if resp.StatusCode == http.StatusNotModified {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, http.StatusNotModified, etag, nil
		}
		// 读 limit+1 字节判断是否超限：恰好 max 时无法区分"正好达到上限"与"被截断"
		body, rerr = io.ReadAll(io.LimitReader(resp.Body, limit+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			// 读取中断得到的是截断体：不能当成功返回（可能被解析入库）
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[mvn-proxy] 读取上游响应失败 %s: %v", u, rerr)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		if int64(len(body)) > limit {
			log.Printf("[mvn-proxy] 上游响应超过大小上限 %d: %s", limit, u)
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
		log.Printf("[mvn-proxy] 上游返回 %d: %s", resp.StatusCode, u)
	}
	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	return lastBody, lastStatus, "", fmt.Errorf("上游不可用 (status=%d)", lastStatus)
}

// refreshCoolingDown 判断该路径是否处于刷新失败冷却期。
func (e *Engine) refreshCoolingDown(repoPath string) bool {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	until, ok := e.failUntil[repoPath]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(e.failUntil, repoPath)
		return false
	}
	return true
}

func (e *Engine) markRefreshFailed(repoPath string) {
	now := time.Now()
	e.failMu.Lock()
	defer e.failMu.Unlock()
	// 一次性失败大量路径时避免 map 无限增长：达到上限先清掉已过期条目
	if len(e.failUntil) >= metaFailMapMax {
		for p, until := range e.failUntil {
			if now.After(until) {
				delete(e.failUntil, p)
			}
		}
	}
	e.failUntil[repoPath] = now.Add(metaRefreshCooldown)
}

func (e *Engine) clearRefreshFailure(repoPath string) {
	e.failMu.Lock()
	delete(e.failUntil, repoPath)
	e.failMu.Unlock()
}

// fetchUpstreamSHA1 取上游校验文件内容（供下载校验链比对）。
// 返回空串表示上游未提供（部分镜像无 .sha1）；此时放行下载但不省略后续长度核对。
func (e *Engine) fetchUpstreamSHA1(ctx context.Context, settings *models.Settings, repoPath string) string {
	body, status, _, err := e.fetchUpstream(ctx, settings, repoPath+".sha1", "", maxChecksumSize)
	if err != nil || status != http.StatusOK {
		return ""
	}
	// 形态 "<hex>" 或 "<hex> <filename>"：取首个 40 位十六进制段
	fields := strings.Fields(string(body))
	for _, f := range fields {
		if len(f) == 40 && isHex(f) {
			return strings.ToLower(f)
		}
	}
	return ""
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// downloadArtifact 从上游下载构件文件到磁盘（临时文件 + 原子重命名）并登记 DB。
// 先取上游 .sha1 作为期望值（可得时强制比对，校验链不可省略）；下载中边下边算。
func (e *Engine) downloadArtifact(ctx context.Context, settings *models.Settings, pf *ParsedFile, destPath string) error {
	// .sha1 预取必须在大闸之外：downloadArtifact 自身已持有上游并发闸，
	// 嵌套获取同一闸门会在并发打满时互相等待闸位而死锁（外层持闸者全部阻塞在内层 acquire）。
	wantSHA1 := e.fetchUpstreamSHA1(ctx, settings, pf.RepoPath)
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	var lastErr error
	for _, u := range upstreamCandidates(settings, pf.RepoPath) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rerr != nil {
			log.Printf("[mvn-proxy] 构建下载请求失败 %s: %v", u, rerr)
			continue
		}
		resp, derr := client.Do(req)
		if derr != nil {
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[mvn-proxy] 下载构件失败 %s: %v", u, derr)
			lastErr = derr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				e.Stats.RecordUpstreamError(errKindNotFound)
				log.Printf("[mvn-proxy] 构件下载返回 %d: %s", resp.StatusCode, u)
				// 镜像覆盖面可能不同：404 也继续尝试备用上游；全部 404 才判定不存在
				lastErr = errUpstreamNotFound
				continue
			}
			e.Stats.RecordUpstreamError(errKindStatus)
			log.Printf("[mvn-proxy] 构件下载返回 %d: %s", resp.StatusCode, u)
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
		tmp, terr := os.CreateTemp(filepath.Dir(destPath), ".mvndl-*")
		if terr != nil {
			resp.Body.Close()
			return terr
		}
		// 边下边算 sha1，落盘前与上游校验文件比对（可得时）
		digest := sha1.New()
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
			log.Printf("[mvn-proxy] 构件长度不符（期望 %d，实际 %d）: %s", expected, size, u)
			lastErr = errors.New("响应长度不符")
			continue
		}
		gotSHA1 := hex.EncodeToString(digest.Sum(nil))
		if wantSHA1 != "" && !strings.EqualFold(gotSHA1, wantSHA1) {
			os.Remove(tmp.Name())
			e.Stats.RecordUpstreamError(errKindVerify)
			log.Printf("[mvn-proxy] sha1 校验失败（期望 %s，实际 %s），已丢弃: %s", wantSHA1, gotSHA1, u)
			lastErr = errors.New("sha1 校验失败")
			continue
		}
		if err := renameReplace(tmp.Name(), destPath); err != nil {
			os.Remove(tmp.Name())
			return err
		}
		return e.recordArtifactFile(pf, size, gotSHA1)
	}
	if lastErr == nil {
		lastErr = errors.New("所有候选地址均无法提供该构件")
	}
	return lastErr
}

// recordArtifactFile 登记构件行（最小行，幂等）与文件记录（size/sha1/落盘路径）。
func (e *Engine) recordArtifactFile(pf *ParsedFile, size int64, sha1Hex string) error {
	artifactID, err := e.DB.UpsertArtifact(&models.Artifact{GroupID: pf.GroupID, ArtifactID: pf.ArtifactID})
	if err != nil {
		return err
	}
	return e.DB.EnsureFileRecord(artifactID, &models.File{
		ArtifactID:    artifactID,
		RepoPath:      pf.RepoPath,
		Version:       pf.Version,
		Filename:      pf.Filename,
		FileSizeBytes: size,
		SHA1:          sha1Hex,
		FilePath:      pf.RepoPath, // 存储树镜像仓库布局，落盘相对路径即仓库相对路径
	})
}

// serveFile 零拷贝传输构件文件，并带上强缓存响应头。
// 构件文件按仓库路径不可变（sha1 已在落盘前校验），可以长缓存；
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
