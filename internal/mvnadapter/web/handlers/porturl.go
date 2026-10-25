package handlers

import (
	"fmt"
	"net"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/webutil"
	"permaporter/internal/mvnadapter/proxy"
)

// validCoords 校验构件坐标（groupId:artifactId，与 proxy 侧同一白名单）。
func validCoords(coords string) (groupID, artifactID string, ok bool) {
	for i := 0; i < len(coords); i++ {
		if coords[i] == ':' {
			groupID, artifactID = coords[:i], coords[i+1:]
			return groupID, artifactID, proxy.ValidGroupArtifact(groupID, artifactID)
		}
	}
	return "", "", false
}

// externalBase 构造 Maven 协议端口（9094）的外部基地址：沿用当前请求的
// scheme 与主机名，端口替换为本生态协议端口（下载链接由服务端注入端口，
// 前端不拼接；SPA 在 9090，文件 URL 必须落到 9094 才能被仓库引擎服务）。
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

// protocolBase Maven 协议端口基地址（详情页文件下载链接用）。
func (a *App) protocolBase(c *gin.Context) string {
	return externalBase(c, a.Cfg.MVNPort)
}
