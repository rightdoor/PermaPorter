package handlers

import (
	"fmt"
	"time"

	"permaporter/internal/core/webutil"
	"permaporter/internal/pyadapter/database"
)

// 页面缓存机制（TTL + 代次失效 + 超限淘汰）已下沉 core/webutil；
// 此处只保留本生态的缓存键组装与查询绑定（key 一律带 py: 前缀）。

type viewCache = webutil.ViewCache
type cacheClass = webutil.CacheClass

// metricsTotalsTTL Metrics 聚合查询的短缓存时长（10-15s 区间）。
const metricsTotalsTTL = 12 * time.Second

const (
	viewCacheTTL = webutil.ViewCacheTTL
	cacheData    = webutil.CacheData
	cacheStats   = webutil.CacheStats
)

var (
	NewViewCache      = webutil.NewViewCache
	normalizeCacheKey = webutil.NormalizeCacheKey
)

// ---------- 页面数据缓存辅助 ----------

// cachedStats 首页/仪表盘共用：项目总数、已缓存文件数、缓存总大小、命中率。
type totalsHit struct {
	PC, FC, Size int64
	HitRate      float64
}

func (a *App) cachedStats() (pc, fc, size int64, hitRate float64) {
	if v, ok := a.Cache.Get("py:stats:totals+hit", cacheData|cacheStats); ok {
		t := v.(totalsHit)
		return t.PC, t.FC, t.Size, t.HitRate
	}
	var err error
	pc, fc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存，避免把 0 缓存 30s
	}
	hitRate = a.Engine.Stats.HitRatePercent()
	a.Cache.Set("py:stats:totals+hit", totalsHit{pc, fc, size, hitRate}, cacheData|cacheStats, viewCacheTTL)
	return
}

func (a *App) cachedRecent() []database.ActivityRow {
	if v, ok := a.Cache.Get("py:dash:recent", cacheData); ok {
		return v.([]database.ActivityRow)
	}
	recent, err := a.DB.RecentActivity(15)
	if err != nil {
		return nil // 查询失败不缓存空列表
	}
	a.Cache.Set("py:dash:recent", recent, cacheData, viewCacheTTL)
	return recent
}

func (a *App) listProjectsCached(q string, offset, limit int) ([]database.ProjectRow, error) {
	return a.listProjectsSortedCached(q, "", "", offset, limit)
}

// listProjectsSortedCached 带排序键的项目列表缓存（sortKey/dir 已由调用方白名单校验）。
func (a *App) listProjectsSortedCached(q, sortKey, dir string, offset, limit int) ([]database.ProjectRow, error) {
	key := fmt.Sprintf("py:projlist:%q:%s:%s:%d:%d", normalizeCacheKey(q), sortKey, dir, offset, limit)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.([]database.ProjectRow), nil
	}
	items, err := a.DB.ListProjectsOrdered(q, sortKey, dir, offset, limit)
	if err == nil {
		a.Cache.Set(key, items, cacheData, viewCacheTTL)
	}
	return items, err
}

func (a *App) countProjectsCached(q string) (int64, error) {
	key := "py:projcount:" + normalizeCacheKey(q)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.(int64), nil
	}
	n, err := a.DB.CountProjects(q)
	if err == nil {
		a.Cache.Set(key, n, cacheData, viewCacheTTL)
	}
	return n, err
}

// cachedTotals Metrics 专用聚合缓存：/metrics 抓取频率高，避免每次抓取都执行
// 聚合查询；只依赖统计代次（statsGen）+ 短 TTL 失效，指标名与文本格式不变。
func (a *App) cachedTotals() (pc, fc, size int64) {
	if v, ok := a.Cache.Get("py:metrics:totals", cacheStats); ok {
		t := v.(totalsHit)
		return t.PC, t.FC, t.Size
	}
	var err error
	pc, fc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存
	}
	a.Cache.Set("py:metrics:totals", totalsHit{PC: pc, FC: fc, Size: size}, cacheStats, metricsTotalsTTL)
	return
}
