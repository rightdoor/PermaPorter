package web

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// InitLogging 统一日志出口：结构化 slog + 把标准库 log 的既有输出也接到 slog，
// 避免两种格式混在一起。单进程只在启动时调用一次。
func InitLogging() {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	// 既有代码里的 log.Printf 统一走 slog（级别 INFO），保持单一格式
	log.SetOutput(slog.NewLogLogger(handler, slog.LevelInfo).Writer())
	log.SetFlags(0)
}

// AccessLog 结构化访问日志：每个请求带 X-Request-ID，
// 跳过静态资源、健康检查与成功的产物请求（安装风暴时这些占绝大多数）。
// artifactSuffix 为生态产物扩展名：go 侧 ".zip"、npm 侧 ".tgz"（参数化注入）。
func AccessLog(artifactSuffix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		c.Header("X-Request-ID", rid)
		c.Next()

		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/static/") || path == "/healthz" || path == "/metrics" {
			return
		}
		status := c.Writer.Status()
		// 成功的产物下载不记（体量大且无排障价值）；失败仍要记
		if artifactSuffix != "" && status < 400 && strings.HasSuffix(path, artifactSuffix) {
			return
		}
		attrs := []any{
			"rid", rid,
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "err", c.Errors.String())
		}
		if status >= 500 {
			slog.Error("http", attrs...)
			return
		}
		slog.Info("http", attrs...)
	}
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
