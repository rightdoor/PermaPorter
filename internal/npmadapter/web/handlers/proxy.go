package handlers

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/webui"
)

// pkgNameRe 合法包名：普通包或 @scope/name 形式的 scoped 包。
var pkgNameRe = regexp.MustCompile(`^(@[a-zA-Z0-9._~-]+/)?[a-zA-Z0-9._~-]+$`)

// ValidPkgName 校验包名可安全参与磁盘路径拼接：正则字符集放行了 "." 与 ".."，
// 但它们作为路径段会拼出存储目录之外的路径（pkg=".." 时 packages/<pkg> 即存储根），
// 必须显式拒绝。所有用包名拼磁盘路径的入口统一经过本函数。
func ValidPkgName(pkg string) bool {
	if pkg == "" || len(pkg) > 214 || !pkgNameRe.MatchString(pkg) {
		return false
	}
	for _, seg := range strings.Split(pkg, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// wantsBrowserHTML 判断是否为浏览器页面请求：GET + Accept 声明 text/html。
// npm 客户端（packument 请求 application/json、tarball 请求 */*）不会命中，
// 因此不会干扰 Registry API 行为；tarball 路径除外——浏览器点击下载链接时
// Accept 也是 text/html，必须照常返回文件流。
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

// ProxyDispatch 处理所有未匹配路由的 NPM Registry 请求。
// 元数据: GET /{pkg}；Tarball: GET /{pkg}/-/{filename}.tgz；发布类请求返回 405。
// 浏览器直接访问时不再返回裸 JSON：元数据路径 302 到公开详情页，
// 其余未知路径输出 SPA 外壳（由前端展示 404 引导页）。
func (a *App) ProxyDispatch(c *gin.Context) {
	p := c.Request.URL.Path
	browser := wantsBrowserHTML(c)
	if strings.HasPrefix(p, "/admin") {
		if browser {
			a.SPA(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !a.DB.IsInitialized() {
		if browser {
			c.Redirect(http.StatusFound, "/admin/setup")
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "服务未完成初始化配置"})
		return
	}
	// npm audit 安全查询（POST /-/npm/v1/security/advisories*）：只读语义，白名单透传上游
	if c.Request.Method == http.MethodPost && strings.HasPrefix(p, "/-/npm/v1/security/advisories") {
		a.Engine.PassThroughRequest(c, p)
		return
	}
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
	default:
		// 只读代理：禁止 PUT / DELETE 等发布操作
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "此镜像为只读代理，禁止发布操作"})
		return
	}
	pkg, filename, ok := splitPkgPath(p)
	if ok {
		if filename == "" {
			// 浏览器直接访问包名路径：跳转公开详情页（未缓存包由详情页给 404 引导）
			if browser {
				c.Redirect(http.StatusFound, "/package/"+pkg)
				return
			}
			a.Engine.ServePackument(c, pkg)
			return
		}
		// tarball 路径照常返回文件流（浏览器下载链接同样是该形态）
		a.Engine.ServeTarball(c, pkg, filename)
		return
	}
	if browser {
		a.SPA(c)
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
}

// splitPkgPath 拆解请求路径中的包名与 tarball 文件名。
func splitPkgPath(p string) (pkg, filename string, ok bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", "", false
	}
	if i := strings.Index(p, "/-/"); i > 0 {
		pkg, filename = p[:i], p[i+3:]
		if filename == "" || strings.Contains(filename, "/") {
			return "", "", false
		}
	} else {
		// 非 tarball 路径只允许普通包名或 @scope/name
		if strings.HasPrefix(p, "@") {
			if strings.Count(p, "/") != 1 {
				return "", "", false
			}
		} else if strings.Contains(p, "/") {
			return "", "", false
		}
		pkg = p
	}
	if !ValidPkgName(pkg) {
		return "", "", false
	}
	return pkg, filename, true
}
