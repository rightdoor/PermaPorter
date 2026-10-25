package upstream

import (
	"net/http"
	"testing"
	"time"
)

// baseTransport 取出 client 底层的 http.Transport（重试包装之下）。
func baseTransport(c *http.Client) *http.Transport {
	if rt, ok := c.Transport.(*retryTransport); ok {
		return rt.base.(*http.Transport)
	}
	return c.Transport.(*http.Transport)
}

// TestClientCacheCachesByProxy 同一出站代理地址复用同一 http.Client，
// 不同地址各自创建（代理配置热加载的基础）。
func TestClientCacheCachesByProxy(t *testing.T) {
	cc := NewClientCache()
	direct := cc.Get("")
	direct2 := cc.Get("")
	if direct != direct2 {
		t.Fatal("同地址应复用同一 client")
	}
	proxied := cc.Get("http://127.0.0.1:7890")
	if proxied == direct {
		t.Fatal("不同地址应创建不同 client")
	}
	if cc.Get("http://127.0.0.1:7890") != proxied {
		t.Fatal("代理地址也应复用")
	}
}

// TestClientTransportTimeouts client 只在 Transport 层设阶段性超时，
// 整体超时由调用方 ctx 控制（Client.Timeout 应为零值）。
func TestClientTransportTimeouts(t *testing.T) {
	cc := NewClientCache()
	c := cc.Get("")
	if c.Timeout != 0 {
		t.Fatalf("Client.Timeout 应为 0（整体超时由 ctx 控制），实际 %v", c.Timeout)
	}
	tr := baseTransport(c)
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("响应头超时应为 30s，实际 %v", tr.ResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != 15*time.Second {
		t.Fatalf("TLS 握手超时应为 15s，实际 %v", tr.TLSHandshakeTimeout)
	}
}

// TestClientProxyApplied 代理地址应写入 Transport.Proxy，直连时不设。
func TestClientProxyApplied(t *testing.T) {
	cc := NewClientCache()
	direct := cc.Get("")
	if tr := baseTransport(direct); tr.Proxy != nil {
		t.Fatal("直连 client 不应设置 Proxy")
	}
	proxied := cc.Get("socks5://127.0.0.1:1080")
	if tr := baseTransport(proxied); tr.Proxy == nil {
		t.Fatal("代理 client 应设置 Proxy")
	}
}

// TestClientCacheEvictsIdleEntries 闲置条目被淘汰并关闭空闲连接；
// 再次 Get 会创建新 client。
func TestClientCacheEvictsIdleEntries(t *testing.T) {
	cc := NewClientCache()
	cc.Get("")
	proxied := cc.Get("http://127.0.0.1:7890")
	if cc.Len() != 2 {
		t.Fatalf("应有 2 个条目，实际 %d", cc.Len())
	}
	// 人为把代理条目置为超过闲置时长，触发淘汰
	cc.mu.Lock()
	cc.clients["http://127.0.0.1:7890"].lastUse = time.Now().Add(-2 * cacheIdleTTL)
	cc.mu.Unlock()
	cc.Get("")
	if cc.Len() != 1 {
		t.Fatalf("闲置条目应被淘汰，剩余 %d", cc.Len())
	}
	if cc.Get("http://127.0.0.1:7890") == proxied {
		t.Fatal("淘汰后再次 Get 应创建新 client")
	}
}
