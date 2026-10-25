package logring

import (
	"sync"
	"time"
)

// LogCapacity 请求日志环形缓冲容量（内存中保留最近 N 条，重启清空）。
// 四生态各自实例化一枚 Ring，容量 500。
const LogCapacity = 500

// LogEntry 单条代理请求日志。
type LogEntry struct {
	Time time.Time
	IP   string
	Kind string // 生态自定义：go 侧 meta/zip/sumdb/pass；npm 侧 meta/tarball/pass/audit
	Pkg  string
	Hit  bool // 是否命中本地缓存
	Dur  time.Duration
	Err  string
}

// LogRing 固定容量环形缓冲，并发安全，只保留最近 N 条。
type LogRing struct {
	mu      sync.Mutex
	entries []LogEntry
	next    int
	full    bool
}

// NewLogRing 创建环形缓冲。容量下限 1：n <= 0 会使 Add 的取模运算除零。
func NewLogRing(n int) *LogRing {
	if n < 1 {
		n = 1
	}
	return &LogRing{entries: make([]LogEntry, n)}
}

// Add 追加一条日志（覆盖最老条目）。
func (r *LogRing) Add(e LogEntry) {
	r.mu.Lock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Recent 返回最近日志（新→旧），最多 n 条。
func (r *LogRing) Recent(n int) []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.next
	if r.full {
		size = len(r.entries)
	}
	if n > size {
		n = size
	}
	out := make([]LogEntry, 0, n)
	for i := 0; i < n; i++ {
		idx := (r.next - 1 - i + len(r.entries)*2) % len(r.entries)
		e := r.entries[idx]
		if e.Time.IsZero() {
			break
		}
		out = append(out, e)
	}
	return out
}
