package database

import "permaporter/internal/mvnadapter/models"

// GetMetaEntry 按仓库相对路径读取 maven-metadata.xml 缓存（两级共用一张表）。
// 热路径字段：XML 原文、ETag 与缓存时间（TTL 判定用）。
func (db *DB) GetMetaEntry(repoPath string) (*models.MetaEntry, error) {
	m := &models.MetaEntry{}
	err := db.QueryRow(
		`SELECT id, repo_path, xml, COALESCE(etag,''), COALESCE(cached_at,'')
		 FROM mvn_metadata WHERE repo_path = ?`, repoPath,
	).Scan(&m.ID, &m.RepoPath, &m.XML, &m.ETag, &m.CachedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// SaveMetaEntry 写入/更新一条 metadata 缓存（raw XML + ETag + 当前时间）。
func (db *DB) SaveMetaEntry(repoPath, xml, etag string) error {
	_, err := db.Exec(
		`INSERT INTO mvn_metadata (repo_path, xml, etag, cached_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(repo_path) DO UPDATE SET
			xml = excluded.xml, etag = excluded.etag, cached_at = excluded.cached_at`,
		repoPath, xml, etag)
	return err
}

// TouchMetaEntry 上游返回 304 时只刷新该路径的缓存时间戳（不重写 XML）。
// 走 execNoGen：时间戳写入不应推进写代次打失效页面缓存。
func (db *DB) TouchMetaEntry(repoPath string) {
	_, _ = db.execNoGen(`UPDATE mvn_metadata SET cached_at = CURRENT_TIMESTAMP WHERE repo_path = ?`, repoPath)
}

// DeleteMetaEntry 删除单条 metadata 缓存（管理端刷新失败兜底，当前未暴露）。
func (db *DB) DeleteMetaEntry(repoPath string) error {
	_, err := db.Exec(`DELETE FROM mvn_metadata WHERE repo_path = ?`, repoPath)
	return err
}
