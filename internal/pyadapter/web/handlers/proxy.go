package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/webui"
)

// wantsBrowserHTML 判断是否为浏览器页面请求：GET + Accept 声明 text/html。
// pip 客户端（锚点页 */*、文件请求 application/octet-stream）不会命中，
// 因此不会干扰 Simple 协议行为；文件下载路径浏览器点击时 Accept 也是 text/html，
// 必须照常返回文件流（/packages/* 在协议分发内处理，不落入此处）。
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

// PyDispatch 处理所有未匹配路由的 PyPI Simple 协议请求。
// 锚点页: GET /simple/、/simple/{project}/；发行文件: GET /packages/{filename}。
// 9093 全部错误为 text/plain; charset=utf-8；发布类请求返回 405。
func (a *App) PyDispatch(c *gin.Context) {
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
	switch {
	case p == "/simple" || p == "/simple/":
		a.Engine.ServeSimpleIndex(c)
		return
	case strings.HasPrefix(p, "/simple/"):
		name := strings.Trim(strings.TrimPrefix(p, "/simple/"), "/")
		if name == "" || strings.Contains(name, "/") {
			break
		}
		if !strings.HasSuffix(p, "/") {
			// PEP 503 惯例：锚点页以 / 结尾，缺斜杠时 301 补齐
			c.Redirect(http.StatusMovedPermanently, p+"/")
			return
		}
		a.Engine.ServeProjectPage(c, name)
		return
	case strings.HasPrefix(p, "/packages/"):
		filename := strings.TrimPrefix(p, "/packages/")
		if filename == "" || strings.Contains(filename, "/") {
			break
		}
		a.Engine.ServePackageFile(c, filename)
		return
	}
	if browser {
		a.SPA(c)
		return
	}
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found"))
}
