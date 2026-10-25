// Package handlers 提供 SPA 外壳服务、JSON API 与 GOPROXY 协议请求分发。
// 页面路由由前端 vue-router 接管，本包只负责数据接口与鉴权。
package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/middleware"
	"permaporter/internal/core/webutil"
	"permaporter/internal/goadapter/database"
	"permaporter/internal/goadapter/preheat"
	"permaporter/internal/goadapter/proxy"
)

// App 聚合全部依赖，供各 handler 使用。
type App struct {
	Cfg      *config.Config
	DB       *database.DB
	Engine   *proxy.Engine
	Sessions *middleware.SessionStore
	Limiter  *middleware.LoginLimiter
	Cache    *viewCache // 页面数据 TTL 缓存（列表/搜索/仪表盘）
	// DetailCache 模块详情缓存：单条含渲染后的 README，体积大，独立控制容量
	DetailCache *viewCache
	Preheat     *preheat.Manager
	Started     time.Time // 服务启动时间（healthz 展示）
}

// Requirement 依赖项（模块详情接口）。
type Requirement struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}

// ParseRequirements 将 versions.requirements_json 解析为按路径排序的列表。
func ParseRequirements(js string) []Requirement {
	if strings.TrimSpace(js) == "" {
		return nil
	}
	var reqs []Requirement
	if err := json.Unmarshal([]byte(js), &reqs); err != nil {
		return nil
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].Path < reqs[j].Path })
	return reqs
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
