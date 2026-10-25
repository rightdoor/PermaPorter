// Package web 提供 npm 生态适配器的装配与 npm Registry 协议端口（9091）路由注册。
//
// 控制台部分（SPA /static /api /admin）由 core 的控制台聚合层负责，
// 本包只保留：App 装配、协议路由（NoRoute 分发/healthz/metrics）
// 与后台任务（清理调度）。会话与登录限流由 core 持有唯一实例并注入。
package web

import (
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/middleware"
	"permaporter/internal/npmadapter/cleanup"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/preheat"
	"permaporter/internal/npmadapter/proxy"
	handlers "permaporter/internal/npmadapter/web/handlers"
)

// NewApp 装配 npm 生态适配器的 App：引擎、视图缓存、预热管理器。
// sessions/limiter 由 core 注入（单实例，仅 9090 控制台实际使用）。
func NewApp(cfg *config.Config, db *database.DB, sessions *middleware.SessionStore, limiter *middleware.LoginLimiter) *handlers.App {
	engine := proxy.NewEngine(db, proxy.NewStats(db))
	return &handlers.App{
		Cfg:         cfg,
		DB:          db,
		Engine:      engine,
		Sessions:    sessions,
		Limiter:     limiter,
		Cache:       handlers.NewViewCache(db.WriteGen, db.StatsGen, 0),
		DetailCache: handlers.NewViewCache(db.WriteGen, db.StatsGen, handlers.DetailCacheMaxEntries),
		Preheat:     preheat.NewManager(engine),
		Started:     time.Now(),
	}
}

// RegisterProtocolRoutes 在 npm Registry 端口引擎上注册路由（9091 清单）。
// 仅本口健康/指标 + NoRoute 协议分发（packument/tarball/audit 透传）；
// 不注册任何 UI 路由，不可判定包名的路径由 ProxyDispatch 返回 JSON 404。
func RegisterProtocolRoutes(r *gin.Engine, app *handlers.App) {
	// 健康检查与指标（无需登录，仅本生态 npm_permaporter_*；HEAD 供负载均衡探活）
	r.GET("/healthz", app.Healthz)
	r.HEAD("/healthz", app.Healthz)
	r.GET("/metrics", app.Metrics)
	r.HEAD("/metrics", app.Metrics)

	// 其余全部交给 NPM Registry 代理
	r.NoRoute(app.ProxyDispatch)

	// 后台自动清理任务：每 12 小时执行一次（启动后延迟 10 分钟，
	// 避免与启动初期的批量下载重叠、误删"仅元数据"包）
	go AutoCleanup(app.DB)
}

// AutoCleanup 周期性清理孤立文件与失效状态：启动后延迟 10 分钟先执行一次，此后每 12 小时执行。
// 只接收 npm 生态存储目录（来自 npm 库 settings.storage_dir，四生态存储互不交叉）。
func AutoCleanup(db *database.DB) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[cleanup] 自动清理异常: %v", r)
			}
		}()
		settings, err := db.GetSettings()
		if err != nil {
			return
		}
		res := cleanup.Run(db, settings.StorageDir)
		// 顺带刷新索引统计：清理删除大量行后，让查询计划重新评估
		db.Optimize()
		log.Printf("[npm-cleanup] 自动清理完成：孤立压缩包 %d、失效版本记录 %d、孤立元数据 %d、仅元数据包 %d、空目录 %d",
			res.RemovedTarballs, res.RemovedVersions, res.RemovedMetas, res.RemovedMetaOnly, res.RemovedEmptyDirs)
	}
	time.Sleep(10 * time.Minute) // 启动后延迟执行，避开启动初期的批量下载
	run()
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		run()
	}
}

// PprofEnabled PPROF=1 时开启调试端点（仅 9090 控制台注册）。
func PprofEnabled() bool { return os.Getenv("PPROF") == "1" }
