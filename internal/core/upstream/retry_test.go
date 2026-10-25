package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

// stubTransport 按脚本逐次返回（响应，错误）对，并记录调用次数与
// CloseIdleConnections 是否被委托。
type stubTransport struct {
	steps  []stubStep
	calls  int
	closed bool
}

type stubStep struct {
	resp *http.Response
	err  error
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	i := s.calls
	s.calls++
	if i >= len(s.steps) {
		return nil, errors.New("stub 脚本耗尽")
	}
	return s.steps[i].resp, s.steps[i].err
}

func (s *stubTransport) CloseIdleConnections() { s.closed = true }

func okResp() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(nil)}
}

func newGet(ctx context.Context) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream.test/x", nil)
	return req
}

// TestClientUsesRetryTransport 经 ClientCache 创建的 client 应使用重试包装。
func TestClientUsesRetryTransport(t *testing.T) {
	cc := NewClientCache()
	if _, ok := cc.Get("").Transport.(*retryTransport); !ok {
		t.Fatal("client 的 Transport 应为重试包装")
	}
}

// TestRetryGetOnTransportError GET 首次传输错误后应立即重试一次并成功。
func TestRetryGetOnTransportError(t *testing.T) {
	stub := &stubTransport{steps: []stubStep{
		{err: io.EOF},
		{resp: okResp()},
	}}
	rt := &retryTransport{base: stub}
	resp, err := rt.RoundTrip(newGet(context.Background()))
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatal("应返回第二次尝试的响应")
	}
	if stub.calls != 2 {
		t.Fatalf("应恰好重试一次（2 次调用），实际 %d", stub.calls)
	}
}

// TestRetryExhaustedReturnsLastError 两次均失败时返回第二次错误，不再继续。
func TestRetryExhaustedReturnsLastError(t *testing.T) {
	second := errors.New("第二次连接重置")
	stub := &stubTransport{steps: []stubStep{
		{err: io.EOF},
		{err: second},
	}}
	rt := &retryTransport{base: stub}
	_, err := rt.RoundTrip(newGet(context.Background()))
	if !errors.Is(err, second) {
		t.Fatalf("应返回第二次的错误，实际 %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("应恰好 2 次调用，实际 %d", stub.calls)
	}
}

// TestNoRetryForNonIdempotent 非幂等方法（POST 等）传输错误不重试。
func TestNoRetryForNonIdempotent(t *testing.T) {
	stub := &stubTransport{steps: []stubStep{{err: io.EOF}}}
	rt := &retryTransport{base: stub}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://upstream.test/x", nil)
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("应返回首次错误")
	}
	if stub.calls != 1 {
		t.Fatalf("POST 不应重试（1 次调用），实际 %d", stub.calls)
	}
}

// TestRetryHead HEAD 幂等，与 GET 同样重试。
func TestRetryHead(t *testing.T) {
	stub := &stubTransport{steps: []stubStep{
		{err: io.EOF},
		{resp: okResp()},
	}}
	rt := &retryTransport{base: stub}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodHead,
		"http://upstream.test/x", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("HEAD 重试后应成功: %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("应恰好重试一次，实际 %d", stub.calls)
	}
}

// TestNoRetryAfterContextCanceled ctx 已取消/到期时不再重试。
func TestNoRetryAfterContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &cancelAfterFirst{cancel: cancel}
	rt := &retryTransport{base: base}
	_, err := rt.RoundTrip(newGet(ctx))
	if err == nil {
		t.Fatal("应返回首次错误")
	}
	if base.calls != 1 {
		t.Fatalf("ctx 取消后不应重试（1 次调用），实际 %d", base.calls)
	}
}

// cancelAfterFirst 首次调用时取消请求 ctx 并返回传输错误。
type cancelAfterFirst struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancelAfterFirst) RoundTrip(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.calls == 1 {
		c.cancel()
	}
	return nil, io.EOF
}

// TestCloseIdleConnectionsDelegated 条目淘汰依赖的空闲连接回收应穿透包装。
func TestCloseIdleConnectionsDelegated(t *testing.T) {
	stub := &stubTransport{}
	rt := &retryTransport{base: stub}
	rt.CloseIdleConnections()
	if !stub.closed {
		t.Fatal("CloseIdleConnections 应委托底层 Transport")
	}
}
