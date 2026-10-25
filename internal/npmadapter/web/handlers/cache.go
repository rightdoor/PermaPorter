package handlers

import (
	"fmt"
	"time"

	"permaporter/internal/core/webutil"
	"permaporter/internal/npmadapter/database"
)

// 页面缓存机制（TTL + 代次失效 + 超限淘汰）已下沉 core/webutil；
// 此处只保留本生态的缓存键组装与查询绑定（key 一律带 npm: 前缀）。

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

// DetailCacheMaxEntries 包详情缓存条目上限（装配 NewViewCache 时使用）。
const DetailCacheMaxEntries = webutil.DetailCacheMaxEntries

// ---------- 页面数据缓存辅助 ----------

// cachedStats 首页/仪表盘共用：包总数、版本总数、缓存总大小、命中率。
// 依赖两类数据：包/版本数据 + 命中率统计。
type totalsHit struct {
	PC, VC, Size int64
	HitRate      float64
}

func (a *App) cachedStats() (pc, vc, size int64, hitRate float64) {
	if v, ok := a.Cache.Get("npm:stats:totals+hit", cacheData|cacheStats); ok {
		t := v.(totalsHit)
		return t.PC, t.VC, t.Size, t.HitRate
	}
	var err error
	pc, vc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存，避免把 0 缓存 30s
	}
	hitRate = a.Engine.Stats.HitRatePercent()
	a.Cache.Set("npm:stats:totals+hit", totalsHit{pc, vc, size, hitRate}, cacheData|cacheStats, viewCacheTTL)
	return
}

func (a *App) cachedRecent() []database.ActivityRow {
	if v, ok := a.Cache.Get("npm:dash:recent", cacheData); ok {
		return v.([]database.ActivityRow)
	}
	recent, err := a.DB.RecentActivity(15)
	if err != nil {
		return nil // 查询失败不缓存空列表
	}
	a.Cache.Set("npm:dash:recent", recent, cacheData, viewCacheTTL)
	return recent
}

func (a *App) listPackagesCached(q string, offset, limit int) ([]database.PkgRow, error) {
	return a.listPackagesSortedCached(q, "", "", offset, limit)
}

// listPackagesSortedCached 带排序键的包列表缓存（sortKey/dir 已由调用方白名单校验）。
func (a *App) listPackagesSortedCached(q, sortKey, dir string, offset, limit int) ([]database.PkgRow, error) {
	key := fmt.Sprintf("npm:pkglist:%q:%s:%s:%d:%d", normalizeCacheKey(q), sortKey, dir, offset, limit)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.([]database.PkgRow), nil
	}
	items, err := a.DB.ListPackagesOrdered(q, sortKey, dir, offset, limit)
	if err == nil {
		a.Cache.Set(key, items, cacheData, viewCacheTTL)
	}
	return items, err
}

func (a *App) countPackagesCached(q string) (int64, error) {
	key := "npm:pkgcount:" + normalizeCacheKey(q)
	if v, ok := a.Cache.Get(key, cacheData); ok {
		return v.(int64), nil
	}
	n, err := a.DB.CountPackages(q)
	if err == nil {
		a.Cache.Set(key, n, cacheData, viewCacheTTL)
	}
	return n, err
}

// cachedTotals Metrics 专用聚合缓存：/metrics 抓取频率高，避免每次抓取都执行
// 聚合查询；只依赖统计代次（statsGen）+ 短 TTL 失效，指标名与文本格式不变。
func (a *App) cachedTotals() (pc, vc, size int64) {
	if v, ok := a.Cache.Get("npm:metrics:totals", cacheStats); ok {
		t := v.(totalsHit)
		return t.PC, t.VC, t.Size
	}
	var err error
	pc, vc, size, err = a.DB.StatsTotals()
	if err != nil {
		return // 查询失败返回零值但不缓存
	}
	a.Cache.Set("npm:metrics:totals", totalsHit{PC: pc, VC: vc, Size: size}, cacheStats, metricsTotalsTTL)
	return
}
