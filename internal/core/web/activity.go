package web

import (
	"strings"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/idle"
)

// Activity 为五个端口的引擎挂请求打点中间件：任何业务请求都唤醒空闲追踪器，
// 重置"空闲→回收"的计时。与 AccessLog 一致，跳过探针与管理性路径——
// 健康检查 / 指标抓取 / 静态资源在带监控的 deployment 里是连续的，若计入活动，
// 空闲回收永远不会触发，功能形同虚设。
func Activity(tracker *idle.Tracker) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isIdleIgnoredPath(c.Request.URL.Path) {
			tracker.Touch()
		}
		c.Next()
	}
}

// isIdleIgnoredPath 判断是否为不应计入"活动"的探针/管理路径。
// 规则与 AccessLog 的跳过项保持一致，避免两处口径漂移。
func isIdleIgnoredPath(path string) bool {
	return strings.HasPrefix(path, "/static/") || path == "/healthz" || path == "/metrics"
}
