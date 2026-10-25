package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/webui"
	"permaporter/internal/mvnadapter/proxy"
)

// wantsBrowserHTML 判断是否为浏览器页面请求：GET + Accept 声明 text/html。
// Maven/Gradle 客户端（application/xml、application/octet-stream）不会命中，
// 因此不会干扰仓库协议行为。
func wantsBrowserHTML(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet {
		return false
	}
	return strings.Contains(c.GetHeader("Accept"), "text/html")
}

// SPA 输出 SPA 外壳页面（路由由前端 vue-router 接管，含 404 页）。
// 初始化/登录状态直接注入 window.__STATE__，前端首屏无需再请求 /api/state。
func (a *App) SPA(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", webui.IndexHTML(webui.State{
		Initialized: a.DB.IsInitialized(),
		Authed:      a.IsAuthed(c),
		Version:     config.Version,
	}))
}

// MvnDispatch 处理所有未匹配路由的 Maven 仓库协议请求。
// 元数据: GET /<g>/<a>[/<v>]/maven-metadata.xml；构件文件: GET /<g>/<a>/<v>/<file>；
// 校验和: 上述任意资源 + .sha1/.md5/.sha256/.sha512。
// 9094 全部错误为 text/plain; charset=utf-8；发布类请求返回 405。
func (a *App) MvnDispatch(c *gin.Context) {
	p := c.Request.URL.Path
	browser := wantsBrowserHTML(c)
	if strings.HasPrefix(p, "/admin") {
		if browser {
			a.SPA(c)
			return
		}
		c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found"))
		return
	}
	if !a.DB.IsInitialized() {
		if browser {
			a.SPA(c)
			return
		}
		c.Data(http.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("服务未完成初始化配置"))
		return
	}
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
	default:
		// 只读代理：禁止 PUT / DELETE 等发布操作
		c.Data(http.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte("此镜像为只读代理，禁止发布操作"))
		return
	}
	// 校验和请求（<资源>.sha1 等）优先识别：资源本体可能是 metadata 或构件文件
	if _, _, ok := proxy.ParseChecksumSuffix(p); ok {
		a.Engine.ServeChecksum(c, p)
		return
	}
	if strings.HasSuffix(p, "/"+proxy.MetaFilename) {
		a.Engine.ServeMetadata(c, p)
		return
	}
	if _, ok := proxy.ParseFileRepoPath(p); ok {
		a.Engine.ServeArtifactFile(c, p)
		return
	}
	if browser {
		a.SPA(c)
		return
	}
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found"))
}
