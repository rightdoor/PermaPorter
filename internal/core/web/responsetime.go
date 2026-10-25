package web

import (
	"time"

	"github.com/gin-gonic/gin"
)

// ---------- X-Response-Time 响应头 ----------

// timingWriter 在写出状态码前注入 X-Response-Time。
type timingWriter struct {
	gin.ResponseWriter
	start time.Time
}

func (w *timingWriter) WriteHeader(code int) {
	w.Header().Set("X-Response-Time", time.Since(w.start).Round(time.Millisecond).String())
	w.ResponseWriter.WriteHeader(code)
}

// ResponseTime 为引擎挂 X-Response-Time 中间件（五端口通用基础设施）。
func ResponseTime() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer = &timingWriter{ResponseWriter: c.Writer, start: time.Now()}
		c.Next()
	}
}
