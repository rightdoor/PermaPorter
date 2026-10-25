package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/proxy"
)

// Metrics Prometheus 文本格式指标端点（无需登录，只暴露聚合数值）。
// 覆盖命中率、回源并发、缓存规模与运行时长，配合 /debug/pprof 足够排障。
func (a *App) Metrics(c *gin.Context) {
	st, _ := a.DB.StatusStatsNoFlush()
	pc, vc, size := a.cachedTotals()
	uptime := time.Since(a.Started).Seconds()

	var b []byte
	// family 输出一个指标的 HELP/TYPE 与全部样本
	family := func(name, help, typ string, samples ...string) {
		b = append(b, fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)...)
		for _, s := range samples {
			b = append(b, s...)
			b = append(b, '\n')
		}
	}
	gauge := func(name, help string, v float64) {
		family(name, help, "gauge", fmt.Sprintf("%s %g", name, v))
	}
	counter := func(name, help string, v int64) {
		family(name, help, "counter", fmt.Sprintf("%s %d", name, v))
	}

	counter("npm_permaporter_meta_requests_total", "元数据请求总数", st.MetaTotal)
	counter("npm_permaporter_meta_hits_total", "元数据缓存命中数", st.MetaHits)
	counter("npm_permaporter_tarball_requests_total", "tarball 请求总数", st.TarballTotal)
	counter("npm_permaporter_tarball_hits_total", "tarball 缓存命中数", st.TarballHits)
	gauge("npm_permaporter_cache_hit_ratio", "综合缓存命中率（0-1）", hitRatio(st))
	gauge("npm_permaporter_upstream_inflight", "当前占用上游并发额度的请求数", float64(a.Engine.Inflight()))
	gauge("npm_permaporter_cached_packages", "已缓存过版本的包数量", float64(pc))
	gauge("npm_permaporter_cached_versions", "已缓存版本数量", float64(vc))
	gauge("npm_permaporter_cache_bytes", "已缓存 tarball 总字节数", float64(size))
	gauge("npm_permaporter_uptime_seconds", "进程运行时长（秒）", uptime)
	family("npm_permaporter_build_info", "构建信息（value 恒为 1）", "gauge",
		fmt.Sprintf(`npm_permaporter_build_info{version=%q} 1`, config.Version))

	// 低基数上游错误计数（进程内存累计，重启清零）
	if errs := a.Engine.Stats.UpstreamErrors(); len(errs) > 0 {
		samples := make([]string, 0, len(errs))
		for _, k := range proxy.SortedErrorKinds(errs) {
			samples = append(samples, fmt.Sprintf(`npm_permaporter_upstream_errors_total{kind=%q} %d`, k, errs[k]))
		}
		family("npm_permaporter_upstream_errors_total", "上游错误累计次数（按错误类别）", "counter", samples...)
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8", b)
}

// hitRatio 综合命中率（0-1），无请求时返回 0。
func hitRatio(st database.StatusStats) float64 {
	total := st.MetaTotal + st.TarballTotal
	if total == 0 {
		return 0
	}
	return float64(st.MetaHits+st.TarballHits) / float64(total)
}
