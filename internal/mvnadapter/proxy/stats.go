package proxy

import (
	"sort"
	"sync"

	"permaporter/internal/mvnadapter/database"
)

// 上游错误类别（低基数白名单，用于 mvn_permaporter_upstream_errors_total{{kind}} 指标）。
const (
	errKindNetwork  = "network"   // 上游网络错误/超时
	errKindNotFound = "not_found" // 上游 404/410
	errKindStatus   = "status"    // 上游返回异常状态码（5xx/403 等）
	errKindOversize = "oversize"  // 响应超过大小上限
	errKindVerify   = "verify"    // 产物校验失败（构件 sha1）
)

// Stats 命中率统计，由 status 数据表持久化（重启不丢失）；
// 另持进程内存的上游错误计数（低基数、重启清零，只经 /metrics 暴露，不落库）。
type Stats struct {
	db     *database.DB
	errMu  sync.Mutex
	errCnt map[string]int64
}

// NewStats 创建统计器。
func NewStats(db *database.DB) *Stats {
	return &Stats{db: db, errCnt: make(map[string]int64)}
}

// RecordUpstreamError 按类别累计一次上游错误。
func (s *Stats) RecordUpstreamError(kind string) {
	s.errMu.Lock()
	s.errCnt[kind]++
	s.errMu.Unlock()
}

// UpstreamErrors 返回各类上游错误的累计次数副本。
func (s *Stats) UpstreamErrors() map[string]int64 {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	out := make(map[string]int64, len(s.errCnt))
	for k, v := range s.errCnt {
		out[k] = v
	}
	return out
}

// SortedErrorKinds 返回排序后的错误类别（指标输出顺序稳定）。
func SortedErrorKinds(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RecordMetaHit 记录一次 maven-metadata.xml 请求。
func (s *Stats) RecordMetaHit(hit bool) {
	s.db.RecordMetaHit(hit)
}

// RecordFileHit 记录一次构件文件请求。
func (s *Stats) RecordFileHit(hit bool) {
	s.db.RecordFileHit(hit)
}

// HitRatePercent 返回总命中率百分比（0-100），从 mvn_status 表读取。
func (s *Stats) HitRatePercent() float64 {
	st, err := s.db.GetStatusStats()
	if err != nil {
		return 0
	}
	total := st.MetaTotal + st.FileTotal
	if total == 0 {
		return 0
	}
	hits := st.MetaHits + st.FileHits
	return float64(hits) / float64(total) * 100
}
