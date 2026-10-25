package upstream

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ClientCache 根据出站代理地址缓存 http.Client，实现代理配置热加载。
// 支持 http://、https://、socks5:// 协议（含 user:pass@ 认证），空字符串表示直连。
// 带淘汰：长期未使用的条目（如已被弃用的旧代理地址）会被关闭空闲连接并移除，
// 防止每个用过的代理地址永久保留一个 Transport 与空闲连接池。
type ClientCache struct {
	mu      sync.Mutex
	clients map[string]*cacheEntry
	// warned 已告警过的非法代理地址：告警按地址去重，避免每请求刷日志
	warned map[string]bool
}

// cacheIdleTTL 条目闲置淘汰时长：超过该时长未被 Get 命中的 client 被淘汰。
const cacheIdleTTL = 10 * time.Minute

type cacheEntry struct {
	client  *http.Client
	lastUse time.Time
}

// NewClientCache 创建客户端缓存。
func NewClientCache() *ClientCache {
	return &ClientCache{
		clients: make(map[string]*cacheEntry),
		warned:  make(map[string]bool),
	}
}

// Get 获取（或创建）指定出站代理的 http.Client，并顺带淘汰闲置条目。
// 只在 Transport 层设阶段性超时（拨号/握手/响应头），整体超时由调用方的 ctx 控制。
func (cc *ClientCache) Get(proxyURL string) *http.Client {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	now := time.Now()
	if e, ok := cc.clients[proxyURL]; ok {
		e.lastUse = now
		cc.evictLocked(now)
		return e.client
	}
	// 非法代理地址（如漏写 scheme 的 localhost:8080）不静默降级直连：
	// 出站流量绕过管理员配置的代理是安全问题，必须留下告警痕迹（按地址去重）。
	if proxyURL != "" && !validProxyURL(proxyURL) && !cc.warned[proxyURL] {
		cc.warned[proxyURL] = true
		log.Printf("[upstream] 出站代理地址非法（已忽略并直连）: %q", proxyURL)
	}
	client := newClient(proxyURL)
	cc.clients[proxyURL] = &cacheEntry{client: client, lastUse: now}
	cc.evictLocked(now)
	return client
}

// Len 返回当前缓存条目数（测试与观测用）。
func (cc *ClientCache) Len() int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return len(cc.clients)
}

// evictLocked 关闭并移除超过闲置时长的条目（调用方需持锁）。
// 淘汰只关闭空闲连接：在途请求由使用方 ctx 控制，不受影响。
func (cc *ClientCache) evictLocked(now time.Time) {
	for key, e := range cc.clients {
		if now.Sub(e.lastUse) < cacheIdleTTL {
			continue
		}
		if tr, ok := e.client.Transport.(interface{ CloseIdleConnections() }); ok {
			tr.CloseIdleConnections()
		}
		delete(cc.clients, key)
	}
}

// validProxyURL 判断出站代理地址可解析为带 scheme 与 host 的 URL。
func validProxyURL(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

// newClient 构建指定出站代理的 http.Client。
func newClient(proxyURL string) *http.Client {
	transport := &http.Transport{
		// 显式拨号超时：零值 Dialer 会卡到 OS 级 TCP 超时（上游地址不可达时）
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true, // 自定义 DialContext 后需显式开启，保持 HTTP/2 连接复用
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil && validProxyURL(proxyURL) {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: &retryTransport{base: transport}}
}

// retryTransport 幂等请求（GET/HEAD）在传输层失败时立即重试一次。
// 上游偶发连接抖动（如复用到已死亡空闲连接时的 EOF/连接重置）会让冷缓存
// 首请求直接 502，立即重试即可恢复；仅覆盖未收到任何响应的传输错误
// （RoundTrip 返回 err 即响应未开始），不改变 HTTP 状态码语义与各适配器
// 的回退链判定。四生态上游请求均为无主体 GET/HEAD，按 Client.Do 契约
// 可用原请求安全重发；ctx 已取消或到期时不再重试。
type retryTransport struct {
	base http.RoundTripper
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil || req.Context().Err() != nil {
		return resp, err
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// CloseIdleConnections 委托底层 Transport：条目淘汰时的空闲连接回收不受包装影响。
func (t *retryTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
