// Package handlers 提供 SPA 外壳服务、JSON API 与 Maven 仓库协议请求分发。
// 页面路由由前端 vue-router 接管，本包只负责数据接口与鉴权。
package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/middleware"
	"permaporter/internal/core/webutil"
	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/preheat"
	"permaporter/internal/mvnadapter/proxy"
)

// App 聚合全部依赖，供各 handler 使用。
type App struct {
	Cfg      *config.Config
	DB       *database.DB
	Engine   *proxy.Engine
	Sessions *middleware.SessionStore
	Limiter  *middleware.LoginLimiter
	Cache    *viewCache // 页面数据 TTL 缓存（列表/仪表盘）
	Preheat  *preheat.Manager
	Started  time.Time // 服务启动时间（healthz 展示）
}

// ---------- JSON 响应辅助（与生态语义无关，已下沉 core/webutil） ----------

var (
	okMsg   = webutil.OKMsg
	failMsg = webutil.FailMsg
)

// ---------- 鉴权 ----------

// IsAuthed 判断当前请求是否已登录。
func (a *App) IsAuthed(c *gin.Context) bool {
	token, _ := c.Cookie(middleware.CookieName)
	return a.Sessions.Valid(token)
}

// RequireAuth 管理接口鉴权：SPA 场景返回 401 JSON，由前端路由跳转登录页。
func (a *App) RequireAuth(c *gin.Context) {
	token, _ := c.Cookie(middleware.CookieName)
	if !a.Sessions.Valid(token) {
		failMsg(c, http.StatusUnauthorized, "未登录或登录已过期")
		c.Abort()
		return
	}
	c.Next()
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
