package database

import (
	"strings"

	"permaporter/internal/mvnadapter/models"
)

// ArtifactRow 构件列表行（mvn_artifacts 单表，物化统计列直接读取）。
type ArtifactRow struct {
	models.Artifact
}

// ActivityRow 最近缓存活动行。
type ActivityRow struct {
	GroupID       string
	ArtifactID    string
	Version       string
	Filename      string
	FileSizeBytes int64
	CachedAt      string
}

// artifactBase 构件列表基础查询：全部字段都是 mvn_artifacts 上的物化列，单表扫描即可。
const artifactBase = `SELECT a.id, a.group_id, a.artifact_id,
	COALESCE(a.latest_version,''), COALESCE(a.release_version,''), a.version_count,
	COALESCE(a.last_updated,''), COALESCE(a.meta_cached_at,''), COALESCE(a.last_accessed_at,''),
	a.file_count, a.cached_file_count, a.cached_total_size, COALESCE(a.last_cached_at,'')
	FROM mvn_artifacts a`

// artifactWhere 搜索条件：匹配 groupId / artifactId / group:artifact 组合名。
// 返回全部已收录构件（含仅元数据构件——只缓存过 maven-metadata.xml 的构件同样合法，
// 清理任务也不得删除）。LIKE 通配符被转义为字面匹配。
func artifactWhere(search string) (string, []any) {
	if search == "" {
		return "", nil
	}
	pattern := "%" + escapeLike(search) + "%"
	return " WHERE (a.group_id LIKE ? ESCAPE '\\' OR a.artifact_id LIKE ? ESCAPE '\\' OR (a.group_id || ':' || a.artifact_id) LIKE ? ESCAPE '\\')",
		[]any{pattern, pattern, pattern}
}

// escapeLike 转义 LIKE 模式中的通配符（配合 ESCAPE '\' 子句按字面匹配）。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// UpsertArtifact 写入/更新构件行，返回构件 ID。
// 有值字段覆盖、空字段保留旧值：固定版本下载只建最小行（版本列为空），
// 构件级 metadata 解析再补齐版本信息。
func (db *DB) UpsertArtifact(a *models.Artifact) (int64, error) {
	_, err := db.Exec(
		`INSERT INTO mvn_artifacts (group_id, artifact_id, latest_version, release_version, version_count, last_updated, meta_cached_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(group_id, artifact_id) DO UPDATE SET
			latest_version = CASE WHEN excluded.latest_version != '' THEN excluded.latest_version ELSE mvn_artifacts.latest_version END,
			release_version = CASE WHEN excluded.release_version != '' THEN excluded.release_version ELSE mvn_artifacts.release_version END,
			version_count = CASE WHEN excluded.version_count > 0 THEN excluded.version_count ELSE mvn_artifacts.version_count END,
			last_updated = CASE WHEN excluded.last_updated != '' THEN excluded.last_updated ELSE mvn_artifacts.last_updated END,
			meta_cached_at = CASE WHEN excluded.meta_cached_at != '' THEN excluded.meta_cached_at ELSE mvn_artifacts.meta_cached_at END`,
		a.GroupID, a.ArtifactID, a.LatestVersion, a.ReleaseVersion, a.VersionCount, a.LastUpdated, a.MetaCachedAt)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM mvn_artifacts WHERE group_id = ? AND artifact_id = ?`,
		a.GroupID, a.ArtifactID).Scan(&id)
	return id, err
}

// GetArtifact 按 groupId:artifactId 查询全量字段。
func (db *DB) GetArtifact(groupID, artifactID string) (*models.Artifact, error) {
	a := &models.Artifact{}
	err := db.QueryRow(
		`SELECT id, group_id, artifact_id, COALESCE(latest_version,''), COALESCE(release_version,''),
		        version_count, COALESCE(last_updated,''), COALESCE(meta_cached_at,''),
		        COALESCE(last_accessed_at,''), file_count, cached_file_count,
		        cached_total_size, COALESCE(last_cached_at,'')
		 FROM mvn_artifacts WHERE group_id = ? AND artifact_id = ?`, groupID, artifactID,
	).Scan(&a.ID, &a.GroupID, &a.ArtifactID, &a.LatestVersion, &a.ReleaseVersion,
		&a.VersionCount, &a.LastUpdated, &a.MetaCachedAt,
		&a.LastAccessedAt, &a.FileCount, &a.CachedFileCount,
		&a.CachedTotalSize, &a.LastCachedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// GetArtifactLite 代理热路径需要的字段（不含统计大列）。
// 最新版本口径：release 优先（Maven 解析 SNAPSHOT 之外的首选），回落 latest。
func (db *DB) GetArtifactLite(groupID, artifactID string) (*models.Artifact, error) {
	a := &models.Artifact{}
	err := db.QueryRow(
		`SELECT id, group_id, artifact_id, COALESCE(latest_version,''), COALESCE(release_version,''),
		        COALESCE(meta_cached_at,''), COALESCE(last_accessed_at,'')
		 FROM mvn_artifacts WHERE group_id = ? AND artifact_id = ?`, groupID, artifactID,
	).Scan(&a.ID, &a.GroupID, &a.ArtifactID, &a.LatestVersion, &a.ReleaseVersion,
		&a.MetaCachedAt, &a.LastAccessedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// TouchArtifact 更新构件的最后访问时间（内存累积，StartStatsFlush 定时批量落库）。
// key 为 "group:artifact"。
func (db *DB) TouchArtifact(groupID, artifactID string) {
	db.mem.touch(groupID + ":" + artifactID)
}

// TouchMetaCachedAt 上游返回 304 时只刷新构件级 metadata 缓存时间戳。
// 该写入不改变页面展示数据，走 execNoGen 避免推进写代次导致页面缓存全量失效。
func (db *DB) TouchMetaCachedAt(groupID, artifactID string) {
	_, _ = db.execNoGen(`UPDATE mvn_artifacts SET meta_cached_at = CURRENT_TIMESTAMP
		WHERE group_id = ? AND artifact_id = ?`, groupID, artifactID)
}

// ListArtifactsOrdered 带排序的构件列表查询。sortKey/dir 必须经过
// ArtifactSortClause 白名单校验，非法值回退默认排序（最近缓存倒序）。
func (db *DB) ListArtifactsOrdered(search, sortKey, dir string, offset, limit int) ([]ArtifactRow, error) {
	where, args := artifactWhere(search)
	args = append(args, limit, offset)
	rows, err := db.Query(artifactBase+where+` ORDER BY `+
		ArtifactSortClause(sortKey, dir)+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	items := []ArtifactRow{}
	for rows.Next() {
		var r ArtifactRow
		if err := rows.Scan(&r.ID, &r.GroupID, &r.ArtifactID, &r.LatestVersion, &r.ReleaseVersion,
			&r.VersionCount, &r.LastUpdated, &r.MetaCachedAt,
			&r.LastAccessedAt, &r.FileCount, &r.CachedFileCount,
			&r.CachedTotalSize, &r.LastCachedAt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return items, nil
}

// ArtifactSortClause 把排序键/方向渲染为 ORDER BY 片段（白名单防注入）。
// 合法键：name / versions / size / time；方向：asc / desc；默认最近缓存倒序。
func ArtifactSortClause(sortKey, dir string) string {
	keys := map[string]string{
		"name":     "(a.group_id || ':' || a.artifact_id)",
		"versions": "a.cached_file_count",
		"size":     "a.cached_total_size",
		"time":     "a.last_cached_at",
	}
	col, ok := keys[sortKey]
	if !ok {
		return "a.last_cached_at DESC, a.id DESC"
	}
	if dir != "asc" && dir != "desc" {
		dir = "desc"
	}
	return col + " " + strings.ToUpper(dir) + ", a.id DESC"
}

// CountArtifacts 统计构件总数（支持搜索）。
func (db *DB) CountArtifacts(search string) (int64, error) {
	where, args := artifactWhere(search)
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM mvn_artifacts a`+where, args...).Scan(&n)
	return n, err
}

// StatsTotals 仪表盘/首页统计：构件总数、已缓存文件数、缓存总大小。
// 口径：fileCount 只统计已缓存本体（file_size_bytes > 0）。
func (db *DB) StatsTotals() (artifactCount, fileCount, totalSize int64, err error) {
	err = db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM mvn_artifacts),
		        (SELECT COUNT(*) FROM mvn_files WHERE file_size_bytes > 0),
		        (SELECT COALESCE(SUM(file_size_bytes),0) FROM mvn_files WHERE file_size_bytes > 0)`).
		Scan(&artifactCount, &fileCount, &totalSize)
	return
}

// RecentActivity 最近落盘的文件缓存记录（cached_at 支撑排序）。
func (db *DB) RecentActivity(limit int) ([]ActivityRow, error) {
	rows, err := db.Query(
		`SELECT a.group_id, a.artifact_id, COALESCE(f.version,''), f.filename,
		        COALESCE(f.file_size_bytes,0), COALESCE(f.cached_at,'')
		 FROM mvn_files f JOIN mvn_artifacts a ON a.id = f.artifact_id
		 WHERE f.file_size_bytes > 0
		 ORDER BY f.cached_at DESC, f.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.GroupID, &r.ArtifactID, &r.Version, &r.Filename, &r.FileSizeBytes, &r.CachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// RecalcArtifactStats 重算并物化构件的文件统计列（删除/清理后修正）。
func (db *DB) RecalcArtifactStats(artifactID int64) {
	_, _ = db.Exec(
		`UPDATE mvn_artifacts SET
			file_count = (SELECT COUNT(*) FROM mvn_files f WHERE f.artifact_id = mvn_artifacts.id),
			cached_file_count = (SELECT COUNT(*) FROM mvn_files f WHERE f.artifact_id = mvn_artifacts.id AND f.file_size_bytes > 0),
			cached_total_size = (SELECT COALESCE(SUM(f.file_size_bytes),0) FROM mvn_files f WHERE f.artifact_id = mvn_artifacts.id AND f.file_size_bytes > 0),
			last_cached_at = (SELECT COALESCE(MAX(f.cached_at),'') FROM mvn_files f WHERE f.artifact_id = mvn_artifacts.id AND f.file_size_bytes > 0)
		 WHERE id = ?`, artifactID)
}

// ArtifactKey 构件的展示名（group:artifact）。
func ArtifactKey(groupID, artifactID string) string { return groupID + ":" + artifactID }
