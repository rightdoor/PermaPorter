package database

import "database/sql"

// StatusStats 系统运行统计（go_status 表，单行 id=1）。
// 写入经 statsMem 内存缓冲批量落库，读取前先刷盘保证完整。
type StatusStats struct {
	MetaTotal int64
	MetaHits  int64
	ZipTotal  int64
	ZipHits   int64
}

// GetStatusStats 读取统计，首次读取时自动初始化一行。
func (db *DB) GetStatusStats() (StatusStats, error) {
	db.FlushStats()
	var s StatusStats
	err := db.QueryRow(
		`SELECT COALESCE(meta_total,0), COALESCE(meta_hits,0), COALESCE(zip_total,0), COALESCE(zip_hits,0)
		 FROM go_status WHERE id = 1`).
		Scan(&s.MetaTotal, &s.MetaHits, &s.ZipTotal, &s.ZipHits)
	if err == sql.ErrNoRows {
		_, _ = db.Exec(`INSERT INTO go_status (id) VALUES (1) ON CONFLICT(id) DO NOTHING`)
		return StatusStats{}, nil
	}
	return s, err
}

// StatusStatsNoFlush 读取已落库的统计，但不触发内存缓冲刷盘。
// 供 /metrics 这类高频只读场景使用：避免每次抓取都产生一次写库。
func (db *DB) StatusStatsNoFlush() (StatusStats, error) {
	var s StatusStats
	err := db.QueryRow(
		`SELECT COALESCE(meta_total,0), COALESCE(meta_hits,0), COALESCE(zip_total,0), COALESCE(zip_hits,0)
		 FROM go_status WHERE id = 1`).
		Scan(&s.MetaTotal, &s.MetaHits, &s.ZipTotal, &s.ZipHits)
	if err == sql.ErrNoRows {
		return StatusStats{}, nil
	}
	return s, err
}

// RecordMetaHit 记录一次元数据请求结果（内存累积，StartStatsFlush 定时落库）。
func (db *DB) RecordMetaHit(hit bool) {
	db.mem.recordMeta(hit)
}

// RecordZipHit 记录一次 zip 请求结果（内存累积，StartStatsFlush 定时落库）。
func (db *DB) RecordZipHit(hit bool) {
	db.mem.recordZip(hit)
}
