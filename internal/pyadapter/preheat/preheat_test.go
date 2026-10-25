package preheat

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coredatabase "permaporter/internal/core/database"
	pydatabase "permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/proxy"
)

// newTestManager 搭建带假上游的预热管理器。
func newTestManager(t *testing.T, upstreamURL string) *Manager {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "test.db")) + "?_busy_timeout=10000&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db := pydatabase.Attach(store)
	t.Cleanup(func() { db.Close(); _ = store.Close() })
	if err := db.InitSettings(upstreamURL, filepath.Join(dir, "storage"), time.Minute); err != nil {
		t.Fatalf("init settings: %v", err)
	}
	engine := proxy.NewEngine(db, proxy.NewStats(db))
	return NewManager(engine)
}

// waitDone 等待任务完成（或超时）。
func waitDone(t *testing.T, m *Manager) JobView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := m.LatestView(); v != nil && v.Finished {
			return *v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("预热任务超时未完成")
	return JobView{}
}

// TestPreheatRunAndStates 预热任务执行：成功项 done/cached、失败项 fail、视图字段完整。
func TestPreheatRunAndStates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/demo-pkg/" {
			w.Write([]byte(`<!DOCTYPE html><html><body><a href="/packages/demo_pkg-0.1.0-py3-none-any.whl#sha256=x">demo_pkg-0.1.0-py3-none-any.whl</a></body></html>`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	m := newTestManager(t, upstream.URL)
	m.Start([]string{"demo-pkg", "missing-pkg"}, false)

	v := waitDone(t, m)
	if v.Total != 2 || !v.Finished || v.Failed != 1 {
		t.Fatalf("任务应完成全部条目: %+v", v)
	}
	byName := map[string]struct {
		State, Version string
	}{}
	for _, it := range v.Items {
		byName[it.Name] = struct {
			State, Version string
		}{it.State, it.Version}
	}
	if s := byName["demo-pkg"]; s.State != "done" || s.Version != "0.1.0" {
		t.Fatalf("demo-pkg 应 done 且解析出 0.1.0: %+v", byName["demo-pkg"])
	}
	if s := byName["missing-pkg"]; s.State != "fail" {
		t.Fatalf("missing-pkg 应 fail: %+v", byName["missing-pkg"])
	}

	// 重复预热同一项目：锚点页已缓存 → cached
	m2 := m
	m2.Start([]string{"demo-pkg"}, false)
	v = waitDone(t, m2)
	if len(v.Items) != 1 || v.Items[0].State != "cached" {
		t.Fatalf("重复预热应 cached: %+v", v.Items)
	}
}
