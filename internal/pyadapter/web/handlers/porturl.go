package handlers

import (
	"fmt"
	"net"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/webutil"
	"permaporter/internal/pyadapter/proxy"
)

// validProjectName 校验规范化项目名（PEP 503 名字规则与 proxy 侧同一白名单）。
func validProjectName(name string) bool {
	return name != "" && len(name) <= 214 && proxy.ValidProjectName(name)
}

// externalBase 构造 PyPI Simple 协议端口（9093）的外部基地址：沿用当前请求的
// scheme 与主机名，端口替换为本生态协议端口（下载链接由服务端注入端口，
// 前端不拼接；SPA 在 9090，文件 URL 必须落到 9093 才能被 Simple 引擎服务）。
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

// protocolBase PyPI Simple 协议端口基地址（详情页文件下载链接用）。
func (a *App) protocolBase(c *gin.Context) string {
	return externalBase(c, a.Cfg.PyPort)
}
