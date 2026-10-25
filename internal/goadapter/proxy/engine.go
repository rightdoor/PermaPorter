// Package proxy 实现 GOPROXY 协议代理引擎：
// 元数据（@v/list / @latest）按 TTL 条件刷新，不可变产物（.info/.mod/.zip/.ziphash）
// 永久缓存；不可变产物命中即返回，永不主动回源。
package proxy

import (
	"context"
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
	"permaporter/internal/goadapter/database"
	"permaporter/internal/goadapter/models"
)

// 元数据/产物大小上限，防止异常上游拖垮内存或磁盘。
const (
	maxListSize   = 8 << 20   // @v/list：超大模块（数千版本）约 100KB 量级，8MB 留足余量
	maxInfoSize   = 1 << 20   // .info：固定两个 JSON 字段
	maxModSize    = 16 << 20  // .mod：go.mod 上限
	maxZipSize    = 500 << 20 // 与 Go 官方 zip.MaxZipFile 对齐
	maxTileSize   = 4 << 20   // sumdb tile/lookup 响应上限
	maxReadmeSize = 512 << 10 // 从 zip 提取的 README 上限
)

// 元数据拉取的整体超时。
const metaUpdateTimeout = 30 * time.Second

// defaultMetaRefreshTTL 元数据刷新间隔默认值：新版本最迟在该时长后可见。
const defaultMetaRefreshTTL = 2 * time.Minute

// metaRefreshWait 缓存过期时等待上游刷新的最长时间：超时先返回旧缓存（可用性优先），
// 后台刷新继续执行；下次请求即可拿到新数据。
const metaRefreshWait = 5 * time.Second

// metaRefreshCooldown 刷新失败后的冷却时间，避免上游故障期间每个请求都等待刷新超时。
const metaRefreshCooldown = time.Minute

// metaFailMapMax 失败冷却表条目上限：超过后先清理已过期条目。
const metaFailMapMax = 4096

// zipDownloadTimeout 单个 zip 下载的整体超时（含候选上游切换）。
const zipDownloadTimeout = 30 * time.Minute

// upstreamMaxConcurrent 上游并发拉取上限：突发批量安装时限流对上游的请求。
const upstreamMaxConcurrent = 16

var errUpstreamNotFound = errors.New("上游不存在该模块或版本")

// errDiskFull 磁盘剩余空间不足（下载前预检失败，共享 core/utils 的哨兵错误）。
var errDiskFull = utils.ErrDiskFull

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

	// 元数据刷新失败冷却：key -> 最近一次失败时间
	failMu    sync.Mutex
	failUntil map[string]time.Time

	// bgWG 后台任务（后台元数据刷新、zip 下载）等待组：
	// 优雅退出时先等它们结束，再关闭数据库，避免在途下载写库报错。
	bgWG sync.WaitGroup

	// sumdb /supported 探测结果缓存：name -> 结果
	sumMu   sync.Mutex
	sumdbOK map[string]sumdbProbe
}

type sumdbProbe struct {
	supported bool
	until     time.Time
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
		sumdbOK:         make(map[string]sumdbProbe),
	}
}

// Inflight 返回当前正在从上游拉取的资源数（含排队）。
func (e *Engine) Inflight() int { return len(e.gate) }

// Shutdown 等待后台任务（后台元数据刷新、在途 zip 下载）结束，
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

// ServeRequest 分发已解析的 GOPROXY 协议请求。
// 只读代理：协议端点只接受 GET/HEAD，其余方法返回 405。
func (e *Engine) ServeRequest(c *gin.Context, req *Request) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
	default:
		e.proxyError(c, http.StatusMethodNotAllowed, "此镜像为只读代理，禁止发布操作")
		return
	}
	switch req.Kind {
	case KindList:
		e.serveList(c, req)
	case KindLatest:
		e.serveLatest(c, req)
	case KindInfo:
		e.serveInfo(c, req)
	case KindMod:
		e.serveMod(c, req)
	case KindZip:
		e.serveZip(c, req)
	case KindZiphash:
		e.serveZiphash(c, req)
	default:
		e.proxyError(c, http.StatusNotFound, "未知的 GOPROXY 资源类型")
	}
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

// ForceRefreshMeta 强制从上游刷新模块元数据（@v/list 与 @latest，忽略 TTL 与失败冷却）。
func (e *Engine) ForceRefreshMeta(module string) error {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return err
	}
	e.clearRefreshFailure("list:" + module)
	e.clearRefreshFailure("latest:" + module)
	if err := e.refreshList(module, settings, 0); err != nil {
		return err
	}
	// @latest 上游并非对所有模块提供（罕见）；列表刷新成功即视为刷新完成
	if err := e.refreshLatest(module, settings, 0); err != nil && !errors.Is(err, errUpstreamNotFound) {
		return err
	}
	return nil
}

// PreheatModule 预热单个模块：version 为空时先解析最新版本；
// withZip 时再下载 .zip（否则只缓存 .info + .mod）。
// 返回 state："cached"（原本就已就绪）或 "done"（本次新拉取）。
func (e *Engine) PreheatModule(module, version string, withZip bool) (state, resolvedVersion string, size int64, err error) {
	settings, err := e.DB.GetSettings()
	if err != nil {
		return "", "", 0, err
	}
	if err := ValidateModuleInput(module); err != nil {
		return "", version, 0, err
	}
	if version == "" {
		version, err = e.resolveLatestVersion(settings, module)
		if err != nil {
			return "", "", 0, err
		}
	} else if err := validateVersionToken(version); err != nil {
		return "", version, 0, err
	}
	req := &Request{Module: module, EscapedModule: escapePath(module), Version: version, EscapedVersion: escapeVersion(version)}

	infoCached, err := e.ensureInfo(settings, req)
	if err != nil {
		return "", version, 0, err
	}
	modCached, err := e.ensureMod(settings, req)
	if err != nil {
		return "", version, 0, err
	}
	if !withZip {
		if infoCached && modCached {
			return "cached", version, 0, nil
		}
		return "done", version, 0, nil
	}
	if _, err := e.ensureZip(settings, req); err != nil {
		return "", version, 0, err
	}
	if v, gerr := e.DB.GetVersionLiteByPath(module, version); gerr == nil {
		size = v.ZipSizeBytes
		// zip 若原本已缓存（ensureZip 返回 cached），视为整体已就绪
		if infoCached && modCached && v.ZipSizeBytes > 0 {
			return "cached", version, size, nil
		}
	}
	return "done", version, size, nil
}

// resolveLatestVersion 预热用：确定模块应缓存的最新版本。
// 优先 @latest；不可用时回退 @v/list 的最高 release（再无则最高 prerelease）。
func (e *Engine) resolveLatestVersion(settings *models.Settings, module string) (string, error) {
	if m, err := e.DB.GetModuleLite(module); err == nil && m.LatestVersion != "" && !metaStale(m.LatestCachedAt, settings.MetaTTL) {
		return m.LatestVersion, nil
	}
	if err := e.refreshLatest(module, settings, 0); err == nil {
		if m, err := e.DB.GetModuleLite(module); err == nil && m.LatestVersion != "" {
			return m.LatestVersion, nil
		}
	} else if !errors.Is(err, errUpstreamNotFound) {
		return "", err
	}
	// @latest 不可用：回退 @v/list
	if err := e.refreshList(module, settings, 0); err != nil {
		return "", err
	}
	m, err := e.DB.GetModuleLite(module)
	if err != nil || m.LatestVersion == "" {
		return "", fmt.Errorf("上游未提供该模块的版本信息")
	}
	return m.LatestVersion, nil
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

// ---------- 响应辅助 ----------

// proxyError 输出代理协议错误：text/plain（GOPROXY 客户端不解析 JSON/HTML）。
func (e *Engine) proxyError(c *gin.Context, code int, msg string) {
	c.Data(code, "text/plain; charset=utf-8", []byte(msg))
}

// serveMetaText 返回可变元数据（@v/list）：text/plain + no-cache。
func (e *Engine) serveMetaText(c *gin.Context, data []byte) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", data)
}

// serveMetaJSON 返回可变元数据（@latest）：application/json + no-cache。
func (e *Engine) serveMetaJSON(c *gin.Context, data []byte) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/json", data)
}

// serveImmutable 零拷贝返回不可变产物，并带上强缓存响应头。
// 产物按「模块 + 版本」不可变（写入前已校验），可以长缓存；
// ETag/Last-Modified 交给 http.ServeContent 处理条件请求与 Range。
func (e *Engine) serveImmutable(c *gin.Context, fullPath, contentType string) {
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
	c.Header("Content-Type", contentType)
	c.Header("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	// 传入真实 modtime：ServeContent 据此生成 Last-Modified 并处理 If-Modified-Since
	http.ServeContent(c.Writer, c.Request, filepath.Base(fullPath), fi.ModTime(), f)
}

// ---------- 磁盘路径 ----------

// storageFilePath 在存储目录下安全拼接路径：兜底确认最终路径仍在存储目录内，
// 防止任何经过校验的输入意外组合出越界路径。
func storageFilePath(root string, parts ...string) (string, error) {
	p := filepath.Join(append([]string{root}, parts...)...)
	cleanRoot := filepath.Clean(root)
	if p != cleanRoot && !strings.HasPrefix(p, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界: %s", p)
	}
	return p, nil
}

// moduleDir 模块的磁盘 @v 目录（GOPROXY 编码形式）。
func (e *Engine) moduleDir(settings *models.Settings, req *Request) (string, error) {
	return storageFilePath(settings.StorageDir, "modules", req.EscapedModule, "@v")
}

// artifactPath 版本产物文件的磁盘路径（ext 形如 ".zip"）。
func (e *Engine) artifactPath(settings *models.Settings, req *Request, ext string) (string, error) {
	return storageFilePath(settings.StorageDir, "modules", req.EscapedModule, "@v", req.EscapedVersion+ext)
}

// listPath @v/list 文件路径。
func (e *Engine) listPath(settings *models.Settings, req *Request) (string, error) {
	return storageFilePath(settings.StorageDir, "modules", req.EscapedModule, "@v", "list")
}

// latestPath @latest 缓存文件路径。
func (e *Engine) latestPath(settings *models.Settings, req *Request) (string, error) {
	return storageFilePath(settings.StorageDir, "modules", req.EscapedModule, "@v", "latest.json")
}

// ---------- 上游访问 ----------

// acquireUpstream 获取上游并发额度，ctx 取消时放弃排队。
func (e *Engine) acquireUpstream(ctx context.Context) (func(), error) {
	select {
	case e.gate <- struct{}{}:
		return func() { <-e.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetchBytes 依次尝试主/备用上游，返回首个 200 响应体。
// 上游 404/410 时不切换备用（避免未知模块每次都双倍请求），直接返回 errUpstreamNotFound；
// 网络错误、超时、5xx、403 继续尝试备用上游。etag 非空时仅对主上游发送 If-None-Match。
func (e *Engine) fetchBytes(ctx context.Context, settings *models.Settings, urlPath, etag string, maxSize int64) (body []byte, status int, newETag string, err error) {
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
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			e.Stats.RecordUpstreamError(errKindNotFound)
			return nil, http.StatusNotFound, "", errUpstreamNotFound
		}
		if resp.StatusCode == http.StatusNotModified {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, http.StatusNotModified, etag, nil
		}
		var rerr error
		body, rerr = io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			// 读取中断得到的是截断体：绝不能当成功返回（会被永久缓存且无自愈）
			e.Stats.RecordUpstreamError(errKindNetwork)
			log.Printf("[proxy] 读取上游响应失败 %s: %v", u, rerr)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		if int64(len(body)) > maxSize {
			log.Printf("[proxy] 上游响应超过大小上限 %d: %s", maxSize, u)
			e.Stats.RecordUpstreamError(errKindOversize)
			lastBody, lastStatus = nil, http.StatusBadGateway
			continue
		}
		lastBody, lastStatus = body, resp.StatusCode
		if resp.StatusCode == http.StatusOK {
			return body, resp.StatusCode, resp.Header.Get("ETag"), nil
		}
		e.Stats.RecordUpstreamError(errKindStatus)
		log.Printf("[proxy] 上游返回 %d: %s", resp.StatusCode, u)
	}
	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	if lastStatus == http.StatusNotFound {
		return nil, lastStatus, "", errUpstreamNotFound
	}
	return lastBody, lastStatus, "", fmt.Errorf("上游不可用 (status=%d)", lastStatus)
}

// fetchZiphash 探测上游是否提供 .ziphash（部分上游代理不提供）。
// 404 等视为不提供（返回空串，无错误）；仅当主上游声明有效内容时返回。
func (e *Engine) fetchZiphash(ctx context.Context, settings *models.Settings, req *Request) (string, error) {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	urlPath := "/" + req.EscapedModule + "/@v/" + req.EscapedVersion + ".ziphash"
	for _, u := range upstreamCandidates(settings, urlPath) {
		reqUp, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(reqUp)
		if err != nil {
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			// 残缺 ziphash 与本地 h1 比对必然不一致，会把完好的 zip 误判为校验失败：跳过比对
			log.Printf("[proxy] 读取上游 ziphash 失败 %s: %v", u, rerr)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return strings.TrimSpace(string(body)), nil
		}
		break // 404 等视为不提供，不再尝试备用（zip 本体已取得，以本地计算值为准）
	}
	return "", nil
}

// upstreamCandidates 生成主/备用上游的候选 URL 列表。
func upstreamCandidates(settings *models.Settings, urlPath string) []string {
	candidates := []string{strings.TrimSuffix(settings.UpstreamURL, "/") + urlPath}
	if settings.FallbackURL != "" {
		candidates = append(candidates, strings.TrimSuffix(settings.FallbackURL, "/")+urlPath)
	}
	return candidates
}

// ---------- 元数据刷新（TTL / 冷却） ----------

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

// refreshCoolingDown 判断该 key 是否处于刷新失败冷却期。
func (e *Engine) refreshCoolingDown(key string) bool {
	e.failMu.Lock()
	defer e.failMu.Unlock()
	until, ok := e.failUntil[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(e.failUntil, key)
		return false
	}
	return true
}

func (e *Engine) markRefreshFailed(key string) {
	now := time.Now()
	e.failMu.Lock()
	defer e.failMu.Unlock()
	// 一次性失败大量模块时避免 map 无限增长：达到上限先清掉已过期条目
	if len(e.failUntil) >= metaFailMapMax {
		for k, until := range e.failUntil {
			if now.After(until) {
				delete(e.failUntil, k)
			}
		}
	}
	e.failUntil[key] = now.Add(metaRefreshCooldown)
}

func (e *Engine) clearRefreshFailure(key string) {
	e.failMu.Lock()
	delete(e.failUntil, key)
	e.failMu.Unlock()
}

// ---------- 通用写入 ----------

// writeFileAtomic 先写临时文件再重命名，保证读方永远看到完整内容。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
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

// ensureDir 创建目录（含上级）。
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
