package database

import (
	"log"
	"time"
)

// DailyRow 单日代理请求统计（按本地日聚合）。
type DailyRow struct {
	Day        string // 2006-01-02
	IndexTotal int64
	IndexHits  int64
	FileTotal  int64
	FileHits   int64
}

// dailyLayout 本地日期字符串格式，与 BumpDaily/BackfillDaily 保持一致。
const dailyLayout = "2006-01-02"

// BumpDaily 将一段增量累加到当天的统计行（FlushStats 批量落库时调用）。
// 同一时间只有 FlushStats 串行调用（写池单连接），无并发冲突。
func (db *DB) BumpDaily(indexTotal, indexHits, fileTotal, fileHits int64) {
	if indexTotal == 0 && fileTotal == 0 {
		return
	}
	day := time.Now().Format(dailyLayout)
	// 统计写入只推进统计代次：仪表盘趋势会刷新，项目列表/搜索缓存不受影响
	_, err := db.execStats(
		`INSERT INTO py_daily_stats (day, index_total, index_hits, file_total, file_hits)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(day) DO UPDATE SET
			index_total = py_daily_stats.index_total + excluded.index_total,
			index_hits = py_daily_stats.index_hits + excluded.index_hits,
			file_total = py_daily_stats.file_total + excluded.file_total,
			file_hits = py_daily_stats.file_hits + excluded.file_hits`,
		day, indexTotal, indexHits, fileTotal, fileHits)
	if err != nil {
		// 统计非关键路径，失败只记日志
		log.Printf("[database] 每日统计落库失败: %v", err)
	}
}

// BackfillDailyFromCache 用 py_files.cached_at 回填每日"回源缓存次数"历史
// （升级前无按天请求计数，以当日新增缓存的文件数近似下载趋势）。
// 仅在 py_daily_stats 为空时执行一次。DATE(x,'localtime') 是 SQLite 方言函数，
// 此处拉原始 UTC 时间串在 Go 侧按本地日分组（与 BumpDaily 的本地日口径一致，
// 且 SQLite / PostgreSQL 双方言通用）。
func (db *DB) BackfillDailyFromCache() error {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM py_daily_stats`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	rows, err := db.Query(
		`SELECT f.cached_at FROM py_files f
		 WHERE f.file_size_bytes > 0 AND f.cached_at IS NOT NULL AND f.cached_at != ''`)
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return err
		}
		if t, perr := time.ParseInLocation(time.DateTime, ts, time.UTC); perr == nil {
			counts[t.Local().Format(dailyLayout)]++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for day, cnt := range counts {
		if _, err := db.Exec(
			`INSERT INTO py_daily_stats (day, file_total) VALUES (?, ?)
			 ON CONFLICT(day) DO NOTHING`, day, cnt); err != nil {
			return err
		}
	}
	return nil
}

// DailyTrend 最近 days 天（含当天）的每日统计，缺失的日期补零。
func (db *DB) DailyTrend(days int) ([]DailyRow, error) {
	if days <= 0 {
		days = 7
	}
	today := time.Now()
	start := today.AddDate(0, 0, -(days - 1)).Format(dailyLayout)
	rows, err := db.Query(
		`SELECT day, index_total, index_hits, file_total, file_hits
		 FROM py_daily_stats WHERE day >= ? ORDER BY day`, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]DailyRow{}
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(&r.Day, &r.IndexTotal, &r.IndexHits, &r.FileTotal, &r.FileHits); err != nil {
			return nil, err
		}
		byDay[r.Day] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DailyRow, 0, days)
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -(days - 1 - i)).Format(dailyLayout)
		if r, ok := byDay[day]; ok {
			out = append(out, r)
		} else {
			out = append(out, DailyRow{Day: day})
		}
	}
	return out, nil
}
