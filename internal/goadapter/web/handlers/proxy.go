package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/webui"
	"permaporter/internal/goadapter/proxy"
)

// wantsBrowserHTML 判断是否为浏览器页面请求：GET + Accept 声明 text/html。
// go 客户端不会发送 Accept: text/html，因此不会干扰 GOPROXY 代理行为；
// zip 下载路径除外——浏览器点击下载链接时 Accept 也是 text/html，照常返回文件流。
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

// SumdbDispatch 校验和数据库转发（/sumdb/<name>/...，只读）。
func (a *App) SumdbDispatch(c *gin.Context) {
	rest := strings.TrimPrefix(c.Param("rest"), "/")
	name, sub, _ := strings.Cut(rest, "/")
	if !proxy.ValidSumdbName(name) {
		c.Data(http.StatusBadRequest, "text/plain; charset=utf-8", []byte("非法的 sumdb 名称"))
		return
	}
	a.Engine.ServeSumdb(c, name, sub)
}

// ProxyDispatch 处理所有未匹配路由的请求。
// GOPROXY 协议：GET /<module>/@v/list、/<module>/@latest、/<module>/@v/<ver>.info|.mod|.zip|.ziphash；
// 浏览器访问已缓存模块路径时 302 到公开详情页；其余未知路径输出 SPA 外壳（404 引导页）。
func (a *App) ProxyDispatch(c *gin.Context) {
	p := c.Request.URL.Path
	browser := wantsBrowserHTML(c)

	req, err := proxy.Parse(p)
	if err != nil {
		if errors.Is(err, proxy.ErrNotProtocol) {
			// 非协议路径：浏览器访问已缓存模块路径时 302 到公开详情页，否则回落 SPA 外壳
			if browser {
				if mod := modulePathOf(p); mod != "" {
					if _, gerr := a.DB.GetModuleLite(mod); gerr == nil {
						c.Redirect(http.StatusFound, "/module/"+mod)
						return
					}
				}
				a.SPA(c)
				return
			}
			// 9092 上非协议路径返回 text/plain 404（保留 GOPROXY 链式回退语义）
			a.plainNotFound(c)
			return
		}
		// 协议路径但参数非法：浏览器给引导页，协议客户端保持 400 text/plain
		if browser {
			a.SPA(c)
			return
		}
		c.Data(http.StatusBadRequest, "text/plain; charset=utf-8", []byte(err.Error()))
		return
	}

	// 只读代理：协议路径只接受 GET/HEAD
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
	default:
		c.Data(http.StatusMethodNotAllowed, "text/plain; charset=utf-8",
			[]byte("此镜像为只读代理，禁止发布操作"))
		return
	}
	if !a.DB.IsInitialized() {
		if browser {
			c.Redirect(http.StatusFound, "/admin/setup")
			return
		}
		c.Data(http.StatusServiceUnavailable, "text/plain; charset=utf-8",
			[]byte("服务未完成初始化配置"))
		return
	}
	a.Engine.ServeRequest(c, req)
}

// plainNotFound 非 GOPROXY 客户端的 404（text/plain，保留 GOPROXY 链式回退语义）。
func (a *App) plainNotFound(c *gin.Context) {
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found"))
}

// modulePathOf 从非协议路径推断候选模块路径（/github.com/x/y → github.com/x/y），
// 供浏览器 302 跳转判断使用；非法路径返回空串。
func modulePathOf(p string) string {
	mod := strings.Trim(p, "/")
	if mod == "" || strings.ContainsAny(mod, "?#") {
		return ""
	}
	if proxy.ValidateModuleInput(mod) != nil {
		return ""
	}
	return mod
}
