package proxy

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// closeTrackingBody 记录 Close 是否被调用。
type closeTrackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *closeTrackingBody) Close() error { b.closed.Store(true); return nil }

// TestReadAndCloseClosesBody 回归测试：非 200 分支共用 readAndClose 丢弃响应体，
// 必须同时关闭连接，否则批量 404 模块会累积泄漏连接/文件描述符。
func TestReadAndCloseClosesBody(t *testing.T) {
	reader := strings.NewReader("error page")
	body := &closeTrackingBody{Reader: reader}
	resp := &http.Response{Body: body}
	readAndClose(resp)
	if !body.closed.Load() {
		t.Fatal("readAndClose 应关闭响应体")
	}
	if reader.Len() != 0 {
		t.Fatalf("响应体应被读完，剩余 %d 字节", reader.Len())
	}
}
