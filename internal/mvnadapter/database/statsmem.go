package database

import (
	"sync"
	"sync/atomic"
	"time"
)

// statsMem 高频统计的内存缓冲：命中率计数与构件访问时间先累积在内存，
// 由 StartStatsFlush 周期性批量落库。消除每个代理请求 2~3 次同步写 SQLite 的热点
// （批量下载期间这些写曾独占唯一的数据库连接，是请求超时的核心瓶颈之一）。
type statsMem struct {
	metaTotal int64
	metaHits  int64
	fileTotal int64
	fileHits  int64

	mu      sync.Mutex
	touched map[string]struct{} // key 为 "group:artifact"

	// stop 刷盘 goroutine 的停止信号（Close 时关闭，配合 stopOnce 保证只关一次）
	stop     chan struct{}
	stopOnce sync.Once
	// startOnce 启动幂等：重复调用 StartStatsFlush 不再另起 goroutine
	startOnce sync.Once
}

// stopFlusher 停止刷盘 goroutine（幂等）。
func (m *statsMem) stopFlusher() {
	if m.stop != nil {
		m.stopOnce.Do(func() { close(m.stop) })
	}
}

func (m *statsMem) recordMeta(hit bool) {
	atomic.AddInt64(&m.metaTotal, 1)
	if hit {
		atomic.AddInt64(&m.metaHits, 1)
	}
}

func (m *statsMem) recordFile(hit bool) {
	atomic.AddInt64(&m.fileTotal, 1)
	if hit {
		atomic.AddInt64(&m.fileHits, 1)
	}
}

func (m *statsMem) touch(key string) {
	m.mu.Lock()
	if m.touched == nil {
		m.touched = make(map[string]struct{})
	}
	m.touched[key] = struct{}{}
	m.mu.Unlock()
}

// statsSnapshot 一次待落盘的增量。
type statsSnapshot struct {
	metaTotal, metaHits int64
	fileTotal, fileHits int64
	touched             []string
}

func (m *statsMem) swap() statsSnapshot {
	s := statsSnapshot{
		metaTotal: atomic.SwapInt64(&m.metaTotal, 0),
		metaHits:  atomic.SwapInt64(&m.metaHits, 0),
		fileTotal: atomic.SwapInt64(&m.fileTotal, 0),
		fileHits:  atomic.SwapInt64(&m.fileHits, 0),
	}
	m.mu.Lock()
	if len(m.touched) > 0 {
		s.touched = make([]string, 0, len(m.touched))
		for key := range m.touched {
			s.touched = append(s.touched, key)
		}
		m.touched = nil
	}
	m.mu.Unlock()
	return s
}

// StartStatsFlush 启动周期性刷盘 goroutine：触发时刻对齐墙钟，
// 每个 interval 周期在第 offset 秒触发。单库下各生态共用一把写锁，
// 错峰落库（go 整分、npm 第 20 秒、py 第 40 秒、mvn 第 50 秒），避免同时抢锁。
// 服务退出时调用 FlushStats 补一次。
func (db *DB) StartStatsFlush(interval, offset time.Duration) {
	// 幂等：重复调用时直接返回。否则第二次调用会覆盖 stop channel，
	// 第一个刷盘 goroutine 等待的旧 channel 永远不会被关闭（goroutine 泄漏）。
	db.mem.startOnce.Do(func() {
		db.mem.stop = make(chan struct{})
		go func() {
			for {
				now := time.Now()
				next := now.Truncate(interval).Add(offset)
				if !next.After(now) {
					next = next.Add(interval)
				}
				select {
				case <-time.After(time.Until(next)):
					db.FlushStats()
				case <-db.mem.stop:
					return
				}
			}
		}()
	})
}

// FlushStats 将内存累积的命中率计数与构件访问时间批量写入数据库。
// 无增量时不产生任何写操作（避免无谓推进代次、打失效页面缓存）。
// 统计写入走 execStats（只推进统计代次），访问时间走 execNoGen（不推进任何代次），
// 因此周期性的落库不再让构件列表/搜索的页面缓存全量失效。
func (db *DB) FlushStats() {
	s := db.mem.swap()
	if s.metaTotal == 0 && s.fileTotal == 0 && len(s.touched) == 0 {
		return
	}
	if s.metaTotal > 0 || s.fileTotal > 0 {
		_, _ = db.execStats(`INSERT INTO mvn_status (id) VALUES (1) ON CONFLICT(id) DO NOTHING`)
		_, _ = db.execStats(
			`UPDATE mvn_status SET meta_total = meta_total + ?, meta_hits = meta_hits + ?,
				file_total = file_total + ?, file_hits = file_hits + ?,
				updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
			s.metaTotal, s.metaHits, s.fileTotal, s.fileHits)
		// 每日趋势计数（按本地日聚合）
		db.BumpDaily(s.metaTotal, s.metaHits, s.fileTotal, s.fileHits)
	}
	// TouchArtifact 批量落库：分块避免 SQL 变量数超限
	const chunk = 150 // key 含冒号，拆成 group/artifact 两个参数后每行占 2 个变量
	for i := 0; i < len(s.touched); i += chunk {
		end := i + chunk
		if end > len(s.touched) {
			end = len(s.touched)
		}
		keys := s.touched[i:end]
		placeholders := make([]byte, 0, len(keys)*5)
		args := make([]any, 0, len(keys)*2)
		for j, key := range keys {
			group, artifact := splitArtifactKey(key)
			if j > 0 {
				placeholders = append(placeholders, ',')
			}
			placeholders = append(placeholders, "(?,?)"...)
			args = append(args, group, artifact)
		}
		_, _ = db.execNoGen(
			`UPDATE mvn_artifacts SET last_accessed_at = CURRENT_TIMESTAMP
			 WHERE (group_id, artifact_id) IN (`+string(placeholders)+`)`,
			args...)
	}
}

// splitArtifactKey 把 "group:artifact" 拆回两段；groupId 可含点但不含冒号，
// 以第一个冒号切分即可，异常形态（无冒号）原样落 group、artifact 置空（UPDATE 不会命中）。
func splitArtifactKey(key string) (group, artifact string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
