package web

import (
	"log"
	"time"

	"permaporter/internal/core/middleware"
)

// Janitor 周期性回收内存中的会话与登录限流记录，避免长期运行后只增不减。
// 会话与限流器是 core 唯一实例（仅 9090 使用），由 main 启动本任务。
func Janitor(sessions *middleware.SessionStore, limiter *middleware.LoginLimiter) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if n := sessions.Cleanup(); n > 0 {
			log.Printf("[auth] 清理过期会话 %d 个", n)
		}
		if n := limiter.Cleanup(); n > 0 {
			log.Printf("[auth] 清理登录限流记录 %d 条", n)
		}
	}
}
