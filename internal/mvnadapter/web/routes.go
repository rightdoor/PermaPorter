// Package web 提供 mvn 生态适配器的装配与 Maven 仓库协议端口（9094）路由注册。
//
// 控制台部分（SPA /static /api /admin）由 core 的控制台聚合层负责，
// 本包只保留：App 装配、协议路由（NoRoute/healthz/metrics）
// 与后台任务（清理调度）。会话与登录限流由 core 持有唯一实例并注入。
package web

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/core/middleware"
	"permaporter/internal/mvnadapter/cleanup"
	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/preheat"
	"permaporter/internal/mvnadapter/proxy"
	handlers "permaporter/internal/mvnadapter/web/handlers"
)

// NewApp 装配 mvn 生态适配器的 App：引擎、视图缓存、预热管理器。
// sessions/limiter 由 core 注入（单实例，仅 9090 控制台实际使用）。
func NewApp(cfg *config.Config, db *database.DB, sessions *middleware.SessionStore, limiter *middleware.LoginLimiter) *handlers.App {
	engine := proxy.NewEngine(db, proxy.NewStats(db))
	return &handlers.App{
		Cfg:      cfg,
		DB:       db,
		Engine:   engine,
		Sessions: sessions,
		Limiter:  limiter,
		Cache:    handlers.NewViewCache(db.WriteGen, db.StatsGen, 0),
		Preheat:  preheat.NewManager(engine),
		Started:  time.Now(),
	}
}

// RegisterProtocolRoutes 在 Maven 仓库协议端口引擎上注册路由（9094 清单）。
// 仅协议端点 + 本口健康/指标；NoRoute 落协议分发，非协议路径由 MvnDispatch
// 返回 text/plain 404。不注册任何 UI 路由。
func RegisterProtocolRoutes(r *gin.Engine, app *handlers.App) {
	// 健康检查与指标（无需登录，仅本生态 mvn_permaporter_*；HEAD 供负载均衡探活）
	r.GET("/healthz", app.Healthz)
	r.HEAD("/healthz", app.Healthz)
	r.GET("/metrics", app.Metrics)
	r.HEAD("/metrics", app.Metrics)

	// 其余全部交给 Maven 仓库协议代理（非协议路径 → text/plain 404）
	r.NoRoute(app.MvnDispatch)

	// 后台自动清理任务：每 12 小时执行一次（启动后延迟 10 分钟，
	// 避免与启动初期的批量下载重叠、误删进行中的下载产物）
	go AutoCleanup(app.DB)
}

// AutoCleanup 周期性清理孤立文件与失效状态：启动后延迟 10 分钟先执行一次，此后每 12 小时执行。
// 清理只做安全动作（复位缺失文件状态、删孤立文件、临时残留与空目录），
// 不删除构件记录、metadata 缓存或任何仅元数据构件的合法缓存。
// 只接收 mvn 生态存储目录（来自 ecosystem_settings.storage_dir，各生态存储互不交叉）。
func AutoCleanup(db *database.DB) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[mvn-cleanup] 自动清理异常: %v", r)
			}
		}()
		settings, err := db.GetSettings()
		if err != nil {
			return
		}
		res := cleanup.Run(db, settings.StorageDir)
		// 顺带刷新索引统计：清理删除大量行后，让查询计划重新评估
		db.Optimize()
		log.Printf("[mvn-cleanup] 自动清理完成：复位缺失文件 %d、孤立文件 %d、临时残留 %d、空目录 %d",
			res.ResetFileRecords, res.RemovedFiles, res.RemovedTmpFiles, res.PrunedDirs)
	}
	time.Sleep(10 * time.Minute) // 启动后延迟执行，避开启动初期的批量下载
	run()
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		run()
	}
}
