package handlers

import (
	"fmt"
	"time"

	"permaporter/internal/core/webutil"
	"permaporter/internal/mvnadapter/database"
)

// 页面缓存机制（TTL + 代次失效 + 超限淘汰）已下沉 core/webutil；
// 此处只保留本生态的缓存键组装与查询绑定（key 一律带 mvn: 前缀）。

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

// cachedStats 首页/仪表盘共用：构件总数、已缓存文件数、缓存总大小、命中率。
type totalsHit struct {
	AC, FC, Size int64
	HitRate      float64
}

func (a *App) cachedStats() (ac, fc, size int64, hitRate float64) {
	if v, ok := a.Cache.Get("mvn:stats:totals+hit", cacheData|cacheStats); ok {
		t := v.(totalsHit)
		return t.AC, t.FC, t.Size, t.HitRate
	}
	var err error
	ac, fc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存，避免把 0 缓存 30s
	}
	hitRate = a.Engine.Stats.HitRatePercent()
	a.Cache.Set("mvn:stats:totals+hit", totalsHit{ac, fc, size, hitRate}, cacheData|cacheStats, viewCacheTTL)
	return
}

func (a *App) cachedRecent() []database.ActivityRow {
	if v, ok := a.Cache.Get("mvn:dash:recent", cacheData); ok {
		return v.([]database.ActivityRow)
	}
	recent, err := a.DB.RecentActivity(15)
	if err != nil {
		return nil // 查询失败不缓存空列表
	}
	a.Cache.Set("mvn:dash:recent", recent, cacheData, viewCacheTTL)
	return recent
}

// listArtifactsSortedCached 带排序键的构件列表缓存（sortKey/dir 已由调用方白名单校验）。
func (a *App) listArtifactsSortedCached(q, sortKey, dir string, offset, limit int) ([]database.ArtifactRow, error) {
	key := fmt.Sprintf("mvn:artlist:%q:%s:%s:%d:%d", normalizeCacheKey(q), sortKey, dir, offset, limit)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.([]database.ArtifactRow), nil
	}
	items, err := a.DB.ListArtifactsOrdered(q, sortKey, dir, offset, limit)
	if err == nil {
		a.Cache.Set(key, items, cacheData, viewCacheTTL)
	}
	return items, err
}

func (a *App) countArtifactsCached(q string) (int64, error) {
	key := "mvn:artcount:" + normalizeCacheKey(q)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.(int64), nil
	}
	n, err := a.DB.CountArtifacts(q)
	if err == nil {
		a.Cache.Set(key, n, cacheData, viewCacheTTL)
	}
	return n, err
}

// cachedTotals Metrics 专用聚合缓存：/metrics 抓取频率高，避免每次抓取都执行
// 聚合查询；只依赖统计代次（statsGen）+ 短 TTL 失效，指标名与文本格式不变。
func (a *App) cachedTotals() (ac, fc, size int64) {
	if v, ok := a.Cache.Get("mvn:metrics:totals", cacheStats); ok {
		t := v.(totalsHit)
		return t.AC, t.FC, t.Size
	}
	var err error
	ac, fc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存
	}
	a.Cache.Set("mvn:metrics:totals", totalsHit{AC: ac, FC: fc, Size: size}, cacheStats, metricsTotalsTTL)
	return
}
