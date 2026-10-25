package web

import (
	"log"
	"net/http/pprof"

	"github.com/gin-gonic/gin"
)

// registerPprof 注册 pprof 调试端点（/debug/pprof/*，需管理员登录）。
// 仅 9090 控制台注册，鉴权复用控制台会话。
func registerPprof(r *gin.Engine, con *Console) {
	g := r.Group("/debug/pprof", con.RequireAuth)
	g.GET("/", gin.WrapF(pprof.Index))
	g.GET("/cmdline", gin.WrapF(pprof.Cmdline))
	g.GET("/profile", gin.WrapF(pprof.Profile))
	g.POST("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/trace", gin.WrapF(pprof.Trace))
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		g.GET("/"+name, gin.WrapH(pprof.Handler(name)))
	}
	log.Printf("[core] pprof 调试端点已开启: /debug/pprof (需管理员登录)")
}
