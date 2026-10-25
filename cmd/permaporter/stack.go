// 后端栈装配：单库 → 四生态适配器 → 四个协议端口 → 控制台引擎。
// 正常启动与向导原地初始化共用同一装配路径；向导完成后仅替换 9090 的处理器
// （不重启进程：容器内 nginx 常驻，re-exec 不可靠）。
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	coredatabase "permaporter/internal/core/database"
	"permaporter/internal/core/middleware"
	"permaporter/internal/core/utils"
	coreweb "permaporter/internal/core/web"
	goadapter_database "permaporter/internal/goadapter/database"
	goadapter "permaporter/internal/goadapter/web"
	gohandlers "permaporter/internal/goadapter/web/handlers"
	mvnadapter_database "permaporter/internal/mvnadapter/database"
	mvnadapter "permaporter/internal/mvnadapter/web"
	mvnhandlers "permaporter/internal/mvnadapter/web/handlers"
	npmadapter_database "permaporter/internal/npmadapter/database"
	npmadapter "permaporter/internal/npmadapter/web"
	npmhandlers "permaporter/internal/npmadapter/web/handlers"
	pyadapter_database "permaporter/internal/pyadapter/database"
	pyadapter "permaporter/internal/pyadapter/web"
	pyhandlers "permaporter/internal/pyadapter/web/handlers"
)

// runtime 进程级运行时：监听错误通道 + 已注册的后端栈（正常启动与向导原地启动共用）。
type runtime struct {
	errCh chan error

	mu     sync.Mutex
	stacks []*backendStack
	uiSrv  *http.Server
}

func newRuntime() *runtime {
	return &runtime{errCh: make(chan error, 16)}
}

func (rt *runtime) addStack(s *backendStack) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.stacks = append(rt.stacks, s)
}

// serveUI 监听 9090（唯一在启动期即创建的监听器；未初始化模式下处理器可被向导替换）。
// 拆成 listenUI + serveUI 两步：先占住端口但不 Serve，完整后端装配成功后再开始服务——
// 装配任一步失败时调用方关闭 listener 退出，不存在任何端口短暂监听的窗口。
func (rt *runtime) listenUI(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func (rt *runtime) serveUI(l net.Listener, h http.Handler) *http.Server {
	srv := newServer(l.Addr().String(), h)
	rt.mu.Lock()
	rt.uiSrv = srv
	rt.mu.Unlock()
	go func() { rt.errCh <- srv.Serve(l) }()
	return srv
}

// shutdownAll 优雅退出：先停收 9090 请求，再逐栈收尾（协议端口 → 引擎后台任务 → 统计落盘 → 关库）。
func (rt *runtime) shutdownAll() {
	rt.mu.Lock()
	uiSrv := rt.uiSrv
	stacks := append([]*backendStack(nil), rt.stacks...)
	rt.mu.Unlock()

	// 每个监听器独立的 10s 收尾预算：串行共享同一 ctx 时，首个端口若耗尽全部预算，
	// 后续端口（含大文件下载中的协议口）拿到已过期的 ctx 会立即放弃等待在途请求。
	if uiSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = uiSrv.Shutdown(ctx)
		cancel()
	}
	for _, st := range stacks {
		st.shutdown()
	}
}

// backendStack 一次完整后端栈：单库 + 四生态 + 四个协议端口。
type backendStack struct {
	store  *coredatabase.Store
	goDB   *goadapter_database.DB
	npmDB  *npmadapter_database.DB
	pyDB   *pyadapter_database.DB
	mvnDB  *mvnadapter_database.DB
	goApp  *gohandlers.App
	npmApp *npmhandlers.App
	pyApp  *pyhandlers.App
	mvnApp *mvnhandlers.App

	protocolServers []*http.Server
}

// shutdown 栈级优雅收尾：协议端口停收 → 引擎等待后台任务 → 各生态统计落盘 → 关单库。
func (s *backendStack) shutdown() {
	for _, srv := range s.protocolServers {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	bgCtx, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer bgCancel()
	if s.goApp != nil {
		s.goApp.Engine.Shutdown(bgCtx)
	}
	if s.npmApp != nil {
		s.npmApp.Engine.Shutdown(bgCtx)
	}
	if s.pyApp != nil {
		s.pyApp.Engine.Shutdown(bgCtx)
	}
	if s.mvnApp != nil {
		s.mvnApp.Engine.Shutdown(bgCtx)
	}
	if s.goDB != nil {
		s.goDB.Close()
	}
	if s.npmDB != nil {
		s.npmDB.Close()
	}
	if s.pyDB != nil {
		s.pyDB.Close()
	}
	if s.mvnDB != nil {
		s.mvnDB.Close()
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			log.Printf("[core] 单库关闭失败: %v", err)
		}
	}
}

// bootFullStack 装配完整后端并启动四个协议端口（不含 9090，由调用方处理）。
// cfg 的端口/上游/数据库字段须已是最终生效值（向导路径由调用方克隆并覆盖）。
// adminHash 非空时无条件写入管理员密码（向导语义：重新进入向导会重置密码）。
// 任一步失败：回滚已启动部分并返回错误（不留下半可用状态）。
func bootFullStack(cfg *config.Config, sessions *middleware.SessionStore, limiter *middleware.LoginLimiter, rt *runtime, ups coreweb.SetupUpstreams, adminHash string) (*backendStack, error) {
	// 存储目录：四棵树两两互异且互不为父子（启动校验已保证）；
	// go 侧额外创建集中临时目录 tmp/（zip 下载先写临时文件、校验通过后原子改名用）
	for _, dir := range []string{cfg.GoStorageDir, cfg.GoStorageDir + "/tmp", cfg.NPMStorageDir, cfg.PyStorageDir, cfg.MVNStorageDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建存储目录失败 %s: %w", dir, err)
		}
	}

	// ---------- core 单例：单库（sqlite / postgresql，方言差异收敛在 core/database） ----------
	var store *coredatabase.Store
	var err error
	switch cfg.EffectiveDBDriver() {
	case config.DriverPostgreSQL:
		store, err = coredatabase.OpenPostgreSQL(cfg.DBDSN)
	default:
		store, err = coredatabase.Open(cfg.DBDSN)
	}
	if err != nil {
		return nil, fmt.Errorf("打开单库失败: %w", err)
	}
	s := &backendStack{store: store}

	// ---------- 平台凭据（任何端口开始服务之前写入） ----------
	// 向导路径：adminHash 无条件写入（重新进入向导 = 重置密码，向导完成页有提示）。
	// 正常/无头路径：INIT_PASSWORD 语义不变——首次启动且库未初始化时写入。
	if adminHash != "" {
		if err := store.SetPasswordHash(adminHash); err != nil {
			s.shutdown()
			return nil, fmt.Errorf("写入管理员密码失败: %w", err)
		}
	} else if cfg.InitPassword != "" && !store.IsInitialized() {
		hash, err := utils.HashPassword(cfg.InitPassword)
		if err != nil {
			s.shutdown()
			return nil, fmt.Errorf("INIT_PASSWORD 无效: %w", err)
		}
		if err := store.SetPasswordHash(hash); err != nil {
			s.shutdown()
			return nil, fmt.Errorf("单库初始化失败: %w", err)
		}
		log.Printf("[core] 已通过 INIT_PASSWORD 环境变量完成初始化")
	}

	// ---------- 各生态适配器（挂载同一单库，表名前缀隔离） ----------
	s.goDB = goadapter_database.Attach(store)
	s.npmDB = npmadapter_database.Attach(store)
	s.pyDB = pyadapter_database.Attach(store)
	s.mvnDB = mvnadapter_database.Attach(store)
	s.goApp = goadapter.NewApp(cfg, s.goDB, sessions, limiter)
	s.npmApp = npmadapter.NewApp(cfg, s.npmDB, sessions, limiter)
	s.pyApp = pyadapter.NewApp(cfg, s.pyDB, sessions, limiter)
	s.mvnApp = mvnadapter.NewApp(cfg, s.mvnDB, sessions, limiter)
	// 生态配置行缺失则按默认配置补齐（向导路径的 cfg 已携带向导所选上游）
	for _, e := range []struct {
		name string
		fn   func() error
	}{
		{"go", s.goApp.EnsureInitSettings}, {"npm", s.npmApp.EnsureInitSettings},
		{"py", s.pyApp.EnsureInitSettings}, {"mvn", s.mvnApp.EnsureInitSettings},
	} {
		if err := e.fn(); err != nil {
			s.shutdown()
			return nil, fmt.Errorf("%s 生态配置行初始化失败: %w", e.name, err)
		}
	}

	// 高频统计（命中率/访问时间）落库错峰：单库下各生态共用一把写锁，
	// go 在每分钟整点、npm 第 20 秒、py 第 40 秒、mvn 第 50 秒批量落库，避免同时抢锁
	s.goDB.StartStatsFlush(time.Minute, 0)
	s.npmDB.StartStatsFlush(time.Minute, 20*time.Second)
	s.pyDB.StartStatsFlush(time.Minute, 40*time.Second)
	s.mvnDB.StartStatsFlush(time.Minute, 50*time.Second)

	// ---------- 四个协议端口：同步绑定（失败即报错），成功后异步 Serve ----------
	rNPM := newEngine(coreweb.AccessLog(".tgz"))
	npmadapter.RegisterProtocolRoutes(rNPM, s.npmApp)
	rGO := newEngine(coreweb.AccessLog(".zip"))
	goadapter.RegisterProtocolRoutes(rGO, s.goApp)
	rPY := newEngine(coreweb.AccessLog(".whl"))
	pyadapter.RegisterProtocolRoutes(rPY, s.pyApp)
	rMVN := newEngine(coreweb.AccessLog(".jar"))
	mvnadapter.RegisterProtocolRoutes(rMVN, s.mvnApp)
	protos := []struct {
		name   string
		port   int
		engine *gin.Engine
	}{
		{"npm", cfg.NPMPort, rNPM},
		{"GOPROXY", cfg.GoPort, rGO},
		{"PyPI", cfg.PyPort, rPY},
		{"Maven", cfg.MVNPort, rMVN},
	}
	for _, p := range protos {
		addr := fmt.Sprintf("0.0.0.0:%d", p.port)
		l, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			s.shutdown()
			return nil, fmt.Errorf("%s 端口 %d 监听失败: %w", p.name, p.port, lerr)
		}
		srv := newServer(addr, p.engine)
		s.protocolServers = append(s.protocolServers, srv)
		go func(sv *http.Server, ln net.Listener) { rt.errCh <- sv.Serve(ln) }(srv, l)
	}

	rt.addStack(s)
	return s, nil
}

// withConsoleRoutes 在引擎上注册完整控制台路由（9090）。
func withConsoleRoutes(r *gin.Engine, cfg *config.Config, st *backendStack, sessions *middleware.SessionStore, limiter *middleware.LoginLimiter) *gin.Engine {
	coreweb.RegisterConsoleRoutes(r, &coreweb.Console{
		Cfg:      cfg,
		Store:    st.store,
		Sessions: sessions,
		Limiter:  limiter,
		Go:       st.goApp,
		Npm:      st.npmApp,
		Py:       st.pyApp,
		Mvn:      st.mvnApp,
		Started:  time.Now(),
	})
	return r
}

// ---------- 未初始化模式（仅 9090：SPA + 初始化向导） ----------

// swapHandler 9090 的可替换处理器：向导 initialize 成功后从向导控制台原地切换为完整控制台。
type swapHandler struct {
	mu sync.RWMutex
	h  http.Handler
}

func (s *swapHandler) Swap(h http.Handler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.h
	s.mu.RUnlock()
	h.ServeHTTP(w, r)
}

// runSetupMode 未初始化模式：仅监听 9090（SPA + 向导 API），不连接数据库、不监听协议端口。
// 向导 initialize：全量校验与配置文件写回在 core/web 完成；Boot 在进程内原地装配完整后端
// 并替换 9090 处理器、启动四个协议端口。
func runSetupMode(cfg *config.Config, sessions *middleware.SessionStore, limiter *middleware.LoginLimiter, rt *runtime) {
	setupEngine := newEngine(coreweb.AccessLog(""))
	swap := &swapHandler{h: setupEngine}
	sc := &coreweb.SetupConsole{
		Cfg:       cfg,
		InitToken: cfg.InitToken,
		Boot: func(req coreweb.SetupRequest) error {
			cfg2 := *cfg // 浅拷贝足够：Config 全部为值字段
			cfg2.UIPort, cfg2.NPMPort, cfg2.GoPort, cfg2.PyPort, cfg2.MVNPort =
				req.Ports.UI, req.Ports.NPM, req.Ports.Go, req.Ports.Py, req.Ports.MVN
			cfg2.GoUpstream, cfg2.GoFallback = req.Upstreams.Go, req.Upstreams.GoFallback
			cfg2.NPMUpstream, cfg2.PyUpstream, cfg2.MVNUpstream = req.Upstreams.NPM, req.Upstreams.Py, req.Upstreams.MVN
			cfg2.Database, cfg2.DatabaseConfigured = req.PG, true
			switch req.Driver {
			case config.DriverPostgreSQL:
				cfg2.DBDSN = req.PG.PostgresDSN()
			default:
				cfg2.DBDSN = req.DSN
			}
			if err := cfg2.Validate(); err != nil {
				return err
			}
			st, err := bootFullStack(&cfg2, sessions, limiter, rt, req.Upstreams, req.AdminHash)
			if err != nil {
				return err
			}
			full := newEngine(coreweb.AccessLog(""))
			withConsoleRoutes(full, &cfg2, st, sessions, limiter)
			swap.Swap(full)
			logStarted(&cfg2, st.store)
			return nil
		},
	}
	coreweb.RegisterSetupRoutes(setupEngine, sc)
	uiLn, err := rt.listenUI(fmt.Sprintf("0.0.0.0:%d", cfg.UIPort))
	if err != nil {
		log.Fatalf("[core] 监听失败: %v", err)
	}
	rt.serveUI(uiLn, swap)
	log.Printf("[core] 未初始化模式：仅监听 %d，向导完成后将原地启动四个协议端口（无需重启）", cfg.UIPort)
}
