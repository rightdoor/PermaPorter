package database

import (
	"strings"

	"permaporter/internal/pyadapter/models"
)

// ProjectRow 项目列表行（py_projects 单表，物化统计列直接读取）。
type ProjectRow struct {
	models.Project
}

// ActivityRow 最近缓存活动行。
type ActivityRow struct {
	ProjectName   string
	Version       string
	Filename      string
	FileSizeBytes int64
	CachedAt      string
}

// projectBase 项目列表基础查询：全部字段都是 py_projects 上的物化列，单表扫描即可。
const projectBase = `SELECT p.id, p.project_name, COALESCE(p.display_name,''),
	COALESCE(p.latest_version,''), COALESCE(p.summary,''), COALESCE(p.meta_cached_at,''),
	COALESCE(p.last_accessed_at,''), p.file_count, p.cached_file_count, p.cached_total_size,
	COALESCE(p.last_cached_at,'')
	FROM py_projects p`

// projectWhere 搜索条件：匹配规范化名/显示名/摘要。返回全部已收录项目
// （含仅元数据项目——Simple 协议下锚点页缓存同样合法，清理任务也不得删除）。
// LIKE 通配符被转义为字面匹配。
func projectWhere(search string) (string, []any) {
	if search == "" {
		return "", nil
	}
	pattern := "%" + escapeLike(search) + "%"
	return " WHERE (p.project_name LIKE ? ESCAPE '\\' OR p.display_name LIKE ? ESCAPE '\\' OR p.summary LIKE ? ESCAPE '\\')", []any{pattern, pattern, pattern}
}

// escapeLike 转义 LIKE 模式中的通配符（配合 ESCAPE '\' 子句按字面匹配）。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// UpsertProject 写入/更新项目元数据，返回项目 ID。
func (db *DB) UpsertProject(p *models.Project) (int64, error) {
	_, err := db.Exec(
		`INSERT INTO py_projects (project_name, display_name, latest_version, summary, meta_cached_at, meta_etag)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_name) DO UPDATE SET
			display_name=excluded.display_name, latest_version=excluded.latest_version,
			summary=excluded.summary, meta_cached_at=excluded.meta_cached_at, meta_etag=excluded.meta_etag`,
		p.ProjectName, p.DisplayName, p.LatestVersion, p.Summary, p.MetaCachedAt, p.MetaETag)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM py_projects WHERE project_name = ?`, p.ProjectName).Scan(&id)
	return id, err
}

// GetProject 按规范化名查询全量字段。
func (db *DB) GetProject(name string) (*models.Project, error) {
	p := &models.Project{}
	err := db.QueryRow(
		`SELECT id, project_name, COALESCE(display_name,''), COALESCE(latest_version,''),
		        COALESCE(summary,''), COALESCE(meta_cached_at,''), COALESCE(meta_etag,''),
		        COALESCE(last_accessed_at,''), file_count, cached_file_count,
		        cached_total_size, COALESCE(last_cached_at,'')
		 FROM py_projects WHERE project_name = ?`, name,
	).Scan(&p.ID, &p.ProjectName, &p.DisplayName, &p.LatestVersion,
		&p.Summary, &p.MetaCachedAt, &p.MetaETag,
		&p.LastAccessedAt, &p.FileCount, &p.CachedFileCount,
		&p.CachedTotalSize, &p.LastCachedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetProjectLite 代理热路径需要的字段（不含统计大列）。
func (db *DB) GetProjectLite(name string) (*models.Project, error) {
	p := &models.Project{}
	err := db.QueryRow(
		`SELECT id, project_name, COALESCE(latest_version,''),
		        COALESCE(meta_cached_at,''), COALESCE(meta_etag,''), COALESCE(last_accessed_at,'')
		 FROM py_projects WHERE project_name = ?`, name,
	).Scan(&p.ID, &p.ProjectName, &p.LatestVersion, &p.MetaCachedAt, &p.MetaETag, &p.LastAccessedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// TouchProject 更新项目的最后访问时间（内存累积，StartStatsFlush 定时批量落库）。
func (db *DB) TouchProject(name string) {
	db.mem.touch(name)
}

// TouchMetaCachedAt 上游返回 304 时只刷新锚点页缓存时间戳，不重写文件表。
// 该写入不改变页面展示数据，走 execNoGen 避免推进写代次导致页面缓存全量失效。
func (db *DB) TouchMetaCachedAt(name string) {
	_, _ = db.execNoGen(`UPDATE py_projects SET meta_cached_at = CURRENT_TIMESTAMP WHERE project_name = ?`, name)
}

// ListProjects 分页查询项目列表（24/页，按最近缓存倒序）。
func (db *DB) ListProjects(search string, offset, limit int) ([]ProjectRow, error) {
	return db.ListProjectsOrdered(search, "", "", offset, limit)
}

// ListProjectsOrdered 带排序的项目列表查询。sortKey/dir 必须经过
// ProjectSortClause 白名单校验，非法值回退默认排序（最近缓存倒序）。
func (db *DB) ListProjectsOrdered(search, sortKey, dir string, offset, limit int) ([]ProjectRow, error) {
	where, args := projectWhere(search)
	args = append(args, limit, offset)
	rows, err := db.Query(projectBase+where+` ORDER BY `+
		ProjectSortClause(sortKey, dir)+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	items := []ProjectRow{}
	for rows.Next() {
		var r ProjectRow
		if err := rows.Scan(&r.ID, &r.ProjectName, &r.DisplayName, &r.LatestVersion,
			&r.Summary, &r.MetaCachedAt, &r.LastAccessedAt, &r.FileCount,
			&r.CachedFileCount, &r.CachedTotalSize, &r.LastCachedAt); err != nil {
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

// ProjectSortClause 把排序键/方向渲染为 ORDER BY 片段（白名单防注入）。
// 合法键：name / versions / size / time；方向：asc / desc；默认最近缓存倒序。
func ProjectSortClause(sortKey, dir string) string {
	keys := map[string]string{
		"name":     "p.project_name",
		"versions": "p.cached_file_count",
		"size":     "p.cached_total_size",
		"time":     "p.last_cached_at",
	}
	col, ok := keys[sortKey]
	if !ok {
		return "p.last_cached_at DESC, p.id DESC"
	}
	if dir != "asc" && dir != "desc" {
		dir = "desc"
	}
	return col + " " + strings.ToUpper(dir) + ", p.id DESC"
}

// CountProjects 统计项目总数（支持搜索）。
func (db *DB) CountProjects(search string) (int64, error) {
	where, args := projectWhere(search)
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM py_projects p`+where, args...).Scan(&n)
	return n, err
}

// StatsTotals 仪表盘/首页统计：项目总数、已缓存文件数、缓存总大小。
// 口径：fileCount 只统计已缓存本体（file_size_bytes > 0）。
func (db *DB) StatsTotals() (projectCount, fileCount, totalSize int64, err error) {
	err = db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM py_projects),
		        (SELECT COUNT(*) FROM py_files WHERE file_size_bytes > 0),
		        (SELECT COALESCE(SUM(file_size_bytes),0) FROM py_files WHERE file_size_bytes > 0)`).
		Scan(&projectCount, &fileCount, &totalSize)
	return
}

// RecentActivity 最近落盘的文件缓存记录（cached_at 支撑排序）。
func (db *DB) RecentActivity(limit int) ([]ActivityRow, error) {
	rows, err := db.Query(
		`SELECT p.project_name, COALESCE(f.version,''), f.filename,
		        COALESCE(f.file_size_bytes,0), COALESCE(f.cached_at,'')
		 FROM py_files f JOIN py_projects p ON p.id = f.project_id
		 WHERE f.file_size_bytes > 0
		 ORDER BY f.cached_at DESC, f.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.ProjectName, &r.Version, &r.Filename, &r.FileSizeBytes, &r.CachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// AllProjectNames 返回全部规范化项目名（/simple/ 根索引用，字典序）。
func (db *DB) AllProjectNames() ([]string, error) {
	rows, err := db.Query(`SELECT project_name FROM py_projects ORDER BY project_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// RecalcProjectStats 重算并物化项目的文件统计列（删除/清理后修正）。
func (db *DB) RecalcProjectStats(projectID int64) {
	_, _ = db.Exec(
		`UPDATE py_projects SET
			file_count = (SELECT COUNT(*) FROM py_files f WHERE f.project_id = py_projects.id),
			cached_file_count = (SELECT COUNT(*) FROM py_files f WHERE f.project_id = py_projects.id AND f.file_size_bytes > 0),
			cached_total_size = (SELECT COALESCE(SUM(f.file_size_bytes),0) FROM py_files f WHERE f.project_id = py_projects.id AND f.file_size_bytes > 0),
			last_cached_at = (SELECT COALESCE(MAX(f.cached_at),'') FROM py_files f WHERE f.project_id = py_projects.id AND f.file_size_bytes > 0)
		 WHERE id = ?`, projectID)
}
