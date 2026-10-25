// PermaPorter 服务入口：单进程、一个二进制、五个监听器。
//
//	9090 控制台（SPA + /api 聚合 + /admin + 聚合 healthz/metrics）
//	9091 npm Registry（packument/tarball/audit 透传）
//	9092 GOPROXY（@v/*/info/mod/zip/ziphash/sumdb）
//	9093 PyPI Simple（/simple/*、/packages/*）
//	9094 Maven 仓库（maven-metadata.xml + 构件文件，Gradle 同协议）
//
// 启动判定顺序（config.Bootstrap）：
//
//	配置文件解析失败 → 拒绝启动；UNATTENDED=1 → 无头正常启动；
//	数据库已配置 → 正常启动（校验失败拒绝启动）；数据库未配置 → 未初始化模式
//	（仅监听 9090 的 SPA + 初始化向导，向导完成后进程内原地启动完整后端，不重启进程）。
//
// 优雅退出：停收请求 → 各引擎 Shutdown → 统计落盘 → 关闭数据库。
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/middleware"
	coreweb "permaporter/internal/core/web"
)

func main() {
	showVersion := flag.Bool("version", false, "打印版本号后退出（用于升级后核对）")
	flag.Parse()

	if *showVersion {
		fmt.Println(config.Version)
		return
	}

	coreweb.InitLogging()
	cfg := config.Load()

	// ---------- 启动判定：配置文件层 → 模式决策（解析失败/校验失败即拒绝启动） ----------
	outcome, err := config.Bootstrap(cfg)
	if err != nil {
		log.Fatalf("[core] 启动判定失败，拒绝启动（不监听任何端口）: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("[core] 启动校验失败，拒绝启动（不监听任何端口）: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	sessions := middleware.NewSessionStore()
	limiter := middleware.NewLoginLimiter()
	// 会话与登录限流的后台回收：过期会话只在再次访问时删除、限流条目只在同 IP
	// 再次尝试时清理，没有本任务 map 会只增不减
	go coreweb.Janitor(sessions, limiter)

	rt := newRuntime()

	if outcome.Mode == config.BootModeSetup {
		runSetupMode(cfg, sessions, limiter, rt)
		waitAndShutdown(rt)
		return
	}

	// ---------- 正常启动：先占住 9090（不服务），完整后端装配成功后再开始服务 ----------
	// 任一步失败即关闭 listener 退出——不存在端口短暂监听的窗口。
	uiLn, err := rt.listenUI(fmt.Sprintf("0.0.0.0:%d", cfg.UIPort))
	if err != nil {
		log.Fatalf("[core] 监听失败: %v", err)
	}
	stack, err := bootFullStack(cfg, sessions, limiter, rt, upstreamsFromConfig(cfg), "")
	if err != nil {
		_ = uiLn.Close()
		log.Fatalf("[core] 启动失败，拒绝启动（不监听任何端口）: %v", err)
	}
	uiEngine := newEngine(coreweb.AccessLog(""))
	withConsoleRoutes(uiEngine, cfg, stack, sessions, limiter)
	rt.serveUI(uiLn, uiEngine)
	logStarted(cfg, stack.store)
	waitAndShutdown(rt)
}

// upstreamsFromConfig 从进程配置取四生态初始上游（正常启动路径）。
func upstreamsFromConfig(cfg *config.Config) coreweb.SetupUpstreams {
	return coreweb.SetupUpstreams{
		Go:         cfg.GoUpstream,
		GoFallback: cfg.GoFallback,
		NPM:        cfg.NPMUpstream,
		Py:         cfg.PyUpstream,
		MVN:        cfg.MVNUpstream,
	}
}

// waitAndShutdown 等待退出信号或任一监听器错误，随后优雅退出全部已启动栈。
func waitAndShutdown(rt *runtime) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-rt.errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		// 任一端口监听失败（如端口被占用）即整体失败退出，避免半可用状态
		log.Fatalf("[core] 监听失败: %v", err)
	case sig := <-quit:
		log.Printf("[core] 收到信号 %v，正在优雅退出...", sig)
	}

	// 关闭顺序：停收请求 → 各引擎 Shutdown（等待后台元数据刷新/下载收尾）→ 统计落盘 → 关库。
	rt.shutdownAll()
	log.Printf("[core] 已退出（统计已落盘）")
}

// logStarted 全端口就绪日志。
func logStarted(cfg *config.Config, store *coredatabase.Store) {
	log.Printf("[core] 服务已启动: 控制台 http://0.0.0.0:%d / npm http://0.0.0.0:%d / GOPROXY http://0.0.0.0:%d / PyPI http://0.0.0.0:%d / Maven http://0.0.0.0:%d",
		cfg.UIPort, cfg.NPMPort, cfg.GoPort, cfg.PyPort, cfg.MVNPort)
	log.Printf("[core] 数据库: %s · go 存储: %s · npm 存储: %s · py 存储: %s · mvn 存储: %s · 配置文件: %s",
		store.Dialect(), cfg.GoStorageDir, cfg.NPMStorageDir, cfg.PyStorageDir, cfg.MVNStorageDir, cfg.ConfigPath)
}

// newEngine 构建统一配置的 gin 引擎：可信代理仅本机反代、
// 访问日志（按生态产物后缀跳过成功下载）、panic 恢复、X-Response-Time。
func newEngine(accessLog gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		log.Printf("[core] 设置可信代理失败（沿用默认值）: %v", err)
	}
	r.Use(accessLog, gin.Recovery(), coreweb.ResponseTime())
	return r
}

// newServer 三监听器共用同一超时策略（WriteTimeout 不设，避免切断大文件下载）。
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
