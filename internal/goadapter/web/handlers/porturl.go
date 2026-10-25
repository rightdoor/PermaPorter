package handlers

import (
	"fmt"
	"net"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/webutil"
)

// externalBase 构造 GOPROXY 协议端口（9092）的外部基地址：沿用当前请求的
// scheme 与主机名，端口替换为本生态协议端口（下载链接由服务端注入端口，
// 前端不拼接；SPA 在 9090，产物 URL 必须落到 9092 才能被 GOPROXY 引擎服务）。
// 反代头仅在直连 peer 为本机可信代理时采信（见 webutil.ExternalScheme）。
func externalBase(c *gin.Context, port int) string {
	scheme := webutil.ExternalScheme(c)
	host := webutil.ForwardedHost(c)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		return scheme + "://" + host
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// protocolBase GOPROXY 协议端口基地址（.zip/.mod/.info 下载链接用）。
func (a *App) protocolBase(c *gin.Context) string {
	return externalBase(c, a.Cfg.GoPort)
}
