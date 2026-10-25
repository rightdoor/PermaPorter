// Package handlers 提供 SPA 外壳服务、JSON API 与代理请求分发。
// 页面路由由前端 vue-router 接管，本包只负责数据接口与鉴权。
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/middleware"
	"permaporter/internal/core/webutil"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/preheat"
	"permaporter/internal/npmadapter/proxy"
)

// App 聚合全部依赖，供各 handler 使用。
type App struct {
	Cfg      *config.Config
	DB       *database.DB
	Engine   *proxy.Engine
	Sessions *middleware.SessionStore
	Limiter  *middleware.LoginLimiter
	Cache    *viewCache // 页面数据 TTL 缓存（列表/搜索/仪表盘）
	// DetailCache 包详情缓存：单条含渲染后的 README，体积大，独立控制容量
	DetailCache *viewCache
	Preheat     *preheat.Manager
	Started     time.Time // 服务启动时间（healthz 展示）
}

// Dep 依赖项（包详情接口）。
type Dep struct {
	Name  string `json:"name"`
	Range string `json:"range"`
}

// ParseDeps 将依赖 JSON 解析为按名称排序的列表。
func ParseDeps(js string) []Dep {
	if strings.TrimSpace(js) == "" {
		return nil
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		return nil
	}
	deps := make([]Dep, 0, len(m))
	for k, v := range m {
		deps = append(deps, Dep{Name: k, Range: fmt.Sprintf("%v", v)})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	return deps
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
