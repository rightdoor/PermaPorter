// Package webutil 提供四生态管理端共用的 Web 层辅助：JSON 响应、页面数据缓存、
// 排序白名单、磁盘水位视图与手动清理任务状态机。
// 只收纳与生态语义无关的部分；各生态的 dashboard 字段、清理规则、表查询差异留在 adapter。
package webutil

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/utils"
)

// ---------- JSON 响应辅助 ----------

// OKMsg 统一成功响应（可携带附加字段）。
func OKMsg(c *gin.Context, extra gin.H) {
	body := gin.H{"ok": true}
	for k, v := range extra {
		body[k] = v
	}
	c.JSON(200, body)
}

// FailMsg 统一错误响应。
func FailMsg(c *gin.Context, code int, msg string) {
	c.JSON(code, gin.H{"error": msg})
}

// ---------- 页面数据缓存（viewCache） ----------

const (
	// ViewCacheTTL 页面数据缓存时长：代理下载风暴期间页面查询完全走缓存不碰 DB；
	// 对应类别的写操作会推进代次使缓存立即失效，常规更新延迟不超过 TTL。
	ViewCacheTTL = 30 * time.Second
	// viewCacheMaxEntries 缓存条目上限。搜索/列表的 key 含用户输入，
	// 公开接口可被随机查询撑爆内存，必须有硬上限。
	viewCacheMaxEntries = 1024
	// DetailCacheMaxEntries 详情缓存条目上限：单条含渲染后的 README，体积远大于列表，
	// 因此用更小的条目上限单独控制。
	DetailCacheMaxEntries = 128
	// viewCacheMaxQueryLen 参与缓存键的查询串最大长度，超出部分截断。
	viewCacheMaxQueryLen = 100
)

// CacheClass 页面缓存的数据依赖类别：决定哪些写操作会让条目失效。
type CacheClass uint8

const (
	// CacheData 依赖包/版本/配置等展示数据（DB.WriteGen）。
	CacheData CacheClass = 1 << iota
	// CacheStats 依赖命中率/每日趋势（DB.StatsGen）。
	CacheStats
)

// ViewCache 页面数据 TTL 缓存，按数据类别用对应代次做失效判断。
// 分成两类代次是为了让「周期性的统计落库」不再打掉列表/搜索缓存。
// key 由调用方自带生态前缀，四生态可共用同一实现。
type ViewCache struct {
	mu         sync.Mutex
	dataGen    func() uint64
	statsGen   func() uint64
	maxEntries int
	entries    map[string]viewEntry
	// seq 单调递增的插入序号：淘汰按插入序（近似 FIFO）而非全 map 反复扫描。
	seq uint64
}

type viewEntry struct {
	val      any
	exp      time.Time
	dataGen  uint64
	statsGen uint64
	seq      uint64
}

// NewViewCache 创建页面缓存；两个参数分别提供数据代次与统计代次，
// maxEntries 为条目上限（<=0 时使用 viewCacheMaxEntries）。
func NewViewCache(dataGen, statsGen func() uint64, maxEntries int) *ViewCache {
	if maxEntries <= 0 {
		maxEntries = viewCacheMaxEntries
	}
	return &ViewCache{
		dataGen:    dataGen,
		statsGen:   statsGen,
		maxEntries: maxEntries,
		entries:    make(map[string]viewEntry),
	}
}

// Get 读取缓存条目；过期或对应代次已推进时视为未命中。
func (vc *ViewCache) Get(key string, class CacheClass) (any, bool) {
	if vc == nil {
		return nil, false
	}
	vc.mu.Lock()
	e, ok := vc.entries[key]
	vc.mu.Unlock()
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	if class&CacheData != 0 && e.dataGen != vc.dataGen() {
		return nil, false
	}
	if class&CacheStats != 0 && e.statsGen != vc.statsGen() {
		return nil, false
	}
	return e.val, true
}

// Set 写入缓存条目并执行超限淘汰。
func (vc *ViewCache) Set(key string, val any, class CacheClass, ttl time.Duration) {
	if vc == nil {
		return
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.seq++
	vc.entries[key] = viewEntry{
		val:      val,
		exp:      time.Now().Add(ttl),
		dataGen:  vc.dataGen(),
		statsGen: vc.statsGen(),
		seq:      vc.seq,
	}
	vc.evictLocked()
}

// Len 返回当前缓存条目数（测试与观测用）。
func (vc *ViewCache) Len() int {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return len(vc.entries)
}

// evictLocked 超限时先清过期条目，仍超限则按插入序淘汰到 3/4 水位（调用方需持锁）。
// 页面缓存的 TTL 均一（ViewCacheTTL），插入序近似等价于最早到期序；
// 单次收集+排序后批量删除，替代"每删一条全 map 扫一遍找最早到期"的 O(n²)——
// 公开搜索接口的缓存键含用户输入，超限淘汰必须保持廉价，否则随机查询可把
// 缓存钉在上限使每次 Set 都持锁扫描数毫秒。
func (vc *ViewCache) evictLocked() {
	if len(vc.entries) <= vc.maxEntries {
		return
	}
	now := time.Now()
	for k, e := range vc.entries {
		if now.After(e.exp) {
			delete(vc.entries, k)
		}
	}
	target := vc.maxEntries * 3 / 4
	if len(vc.entries) <= target {
		return
	}
	type keyed struct {
		key string
		seq uint64
	}
	cands := make([]keyed, 0, len(vc.entries))
	for k, e := range vc.entries {
		cands = append(cands, keyed{k, e.seq})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].seq < cands[j].seq })
	for i := 0; i < len(cands) && len(vc.entries) > target; i++ {
		delete(vc.entries, cands[i].key)
	}
}

// NormalizeCacheKey 归一化参与缓存键的用户输入：去空白、转小写、限长。
// 避免大小写/超长随机串制造大量等价但不同的 key。
func NormalizeCacheKey(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if len(q) > viewCacheMaxQueryLen {
		q = q[:viewCacheMaxQueryLen]
	}
	return q
}

// ---------- 反向代理头信任判定 ----------

// ExternalScheme 判断客户端实际访问的外部 scheme。
// 仅当直连 peer 是本机回环（与 gin SetTrustedProxies 的 127.0.0.1/::1 配置一致，
// 即请求确实经过本机反向代理转发）时才采信 X-Forwarded-Proto；任意客户端伪造
// 该头不得影响会话 Cookie 的 Secure 标记与注入前端的生态下载地址。
func ExternalScheme(c *gin.Context) string {
	if c.Request.TLS != nil {
		return "https"
	}
	if isLoopbackPeer(c) && c.GetHeader("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}

// ForwardedHost 返回客户端实际访问的主机名（不含端口）。
// 仅当直连 peer 是本机回环时才采信 X-Forwarded-Host（取第一个值），
// 否则使用请求自身的 Host 头——任意客户端伪造该头不得注入下载地址。
func ForwardedHost(c *gin.Context) string {
	host := c.Request.Host
	if isLoopbackPeer(c) {
		if fh := c.GetHeader("X-Forwarded-Host"); fh != "" {
			if i := strings.IndexByte(fh, ','); i >= 0 {
				fh = fh[:i]
			}
			return strings.TrimSpace(fh)
		}
	}
	return host
}

// isLoopbackPeer 判断 TCP 直连对端是否为本机回环地址（可信反向代理的范围）。
func isLoopbackPeer(c *gin.Context) bool {
	ip := net.ParseIP(c.RemoteIP())
	return ip != nil && ip.IsLoopback()
}

// ---------- 排序白名单 ----------

// NormalizeSort 校验排序参数，归一到白名单内的键与方向；
// 非法值返回空串（即默认"最近缓存倒序"），同时避免非法参数打爆缓存键。
// 四生态后台列表的排序键一致（name/versions/size/time）。
func NormalizeSort(sortKey, dir string) (key, order string) {
	switch sortKey {
	case "name", "versions", "size", "time":
		if dir == "asc" {
			return sortKey, "asc"
		}
		return sortKey, "desc"
	}
	return "", ""
}

// ---------- 磁盘水位视图 ----------

// DiskView 磁盘水位 + 回源计数（仪表盘展示）。
type DiskView struct {
	Free     uint64 `json:"free"`
	Total    uint64 `json:"total"`
	Warn     bool   `json:"warn"` // 剩余低于阈值
	Err      string `json:"err,omitempty"`
	Inflight int    `json:"inflight"` // 正在从上游拉取的资源数
}

// ReadDiskView 读取存储目录所在磁盘水位与回源计数；
// warnBytes 为剩余空间告警阈值，inflight 为当前回源数。
func ReadDiskView(storageDir string, inflight int, warnBytes uint64) DiskView {
	v := DiskView{Inflight: inflight}
	free, total, err := utils.DiskFree(storageDir)
	if err != nil {
		v.Err = "读取磁盘信息失败"
		return v
	}
	v.Free, v.Total = free, total
	v.Warn = free < warnBytes
	return v
}

// DiskViewError 构造带错误信息的磁盘视图（如读取配置失败）。
func DiskViewError(errMsg string, inflight int) DiskView {
	return DiskView{Err: errMsg, Inflight: inflight}
}

// ---------- 手动清理任务状态机 ----------

// CleanupJob 手动清理任务的状态机：清理会遍历整个存储目录（可能几十秒），
// 放后台执行、前端轮询进度，避免请求超时或占满连接。
// 四生态共用状态机；清理规则与完成消息的拼装留在各 adapter。
type CleanupJob struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	message  string
	errMsg   string
}

// Running 当前是否有清理任务在执行。
func (j *CleanupJob) Running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running
}

// Submit 提交一次后台清理任务，已在运行时返回 false（调用方回 409）。
// fn 在后台 goroutine 执行并返回完成消息；panic 被捕获并记入 errMsg。
func (j *CleanupJob) Submit(fn func() string) bool {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return false
	}
	j.running = true
	j.started = time.Now()
	j.finished = time.Time{}
	j.message = ""
	j.errMsg = ""
	j.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				j.mu.Lock()
				j.errMsg = "清理异常: " + toStr(r)
				j.running = false
				j.finished = time.Now()
				j.mu.Unlock()
			}
		}()
		msg := fn()
		j.mu.Lock()
		j.running = false
		j.finished = time.Now()
		j.message = msg
		j.mu.Unlock()
	}()
	return true
}

// Payload 当前状态的 JSON 形态（前端轮询）。
func (j *CleanupJob) Payload() gin.H {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := gin.H{
		"running":  j.running,
		"finished": !j.finished.IsZero(),
		"message":  j.message,
		"err":      j.errMsg,
	}
	if !j.started.IsZero() {
		out["startedAt"] = j.started.Format(time.RFC3339)
	}
	if !j.finished.IsZero() {
		out["finishedAt"] = j.finished.Format(time.RFC3339)
		out["durationMs"] = j.finished.Sub(j.started).Milliseconds()
	}
	return out
}

func toStr(v any) string {
	return fmt.Sprint(v)
}
