package database

import (
	"strings"

	"permaporter/internal/npmadapter/models"
)

// PkgRow 包列表行（npm_packages 联表统计，基于已缓存版本）。
type PkgRow struct {
	models.Package
	VersionCount int64 // 已缓存版本数
	TotalSize    int64 // 已缓存版本体积和
	LastCached   string
}

// ActivityRow 最近缓存活动行。
type ActivityRow struct {
	PkgName       string
	Version       string
	FileSizeBytes int64
	CachedAt      string
}

// pkgBase 包列表基础查询：全部字段（含版本数/体积/最近缓存时间）都是 npm_packages 上的
// 物化列，单表扫描即可，不再对 npm_versions 做 JOIN + GROUP BY 聚合，
// 因此能走 pkg_name 索引与排序。物化列由 SetVersionFile/DeleteVersion/清理任务维护。
const pkgBase = `SELECT p.id, p.pkg_name, COALESCE(p.description,''),
	COALESCE(p.latest_cached_version,''),
	COALESCE(p.meta_cached_at,''), COALESCE(p.last_accessed_at,''),
	p.cached_version_count, p.cached_total_size, COALESCE(p.last_cached_at,'')
	FROM npm_packages p`

// pkgWhere 搜索条件：同时匹配包名与描述（想不起包名、只记得用途时也能搜到）。
// 只返回已缓存过版本的包（与列表口径一致）。LIKE 通配符被转义为字面匹配。
func pkgWhere(search string) (string, []any) {
	cond := " WHERE p.cached_version_count > 0"
	if search == "" {
		return cond, nil
	}
	pattern := "%" + escapeLike(search) + "%"
	return cond + " AND (p.pkg_name LIKE ? ESCAPE '\\' OR p.description LIKE ? ESCAPE '\\')", []any{pattern, pattern}
}

// escapeLike 转义 LIKE 模式中的通配符（配合 ESCAPE '\' 子句按字面匹配）。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// UpsertPackage 写入/更新包元数据，返回包 ID。
// 首次创建时同时初始化 last_accessed_at，避免该字段为空。
func (db *DB) UpsertPackage(p *models.Package) (int64, error) {
	_, err := db.Exec(
		`INSERT INTO npm_packages (pkg_name, description, latest_version, repository_url, homepage_url, license, readme_content, meta_cached_at, meta_etag, last_accessed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(pkg_name) DO UPDATE SET
			description=excluded.description, latest_version=excluded.latest_version,
			repository_url=excluded.repository_url, homepage_url=excluded.homepage_url,
			license=excluded.license, readme_content=excluded.readme_content,
			meta_cached_at=excluded.meta_cached_at, meta_etag=excluded.meta_etag`,
		p.PkgName, p.Description, p.LatestVersion, p.RepositoryURL, p.HomepageURL, p.License, p.ReadmeContent, p.MetaCachedAt, p.MetaETag)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM npm_packages WHERE pkg_name = ?`, p.PkgName).Scan(&id)
	return id, err
}

// GetPackage 按包名查询。
func (db *DB) GetPackage(name string) (*models.Package, error) {
	p := &models.Package{}
	err := db.QueryRow(
		`SELECT id, pkg_name, COALESCE(description,''), COALESCE(latest_version,''),
		        COALESCE(repository_url,''), COALESCE(homepage_url,''), COALESCE(license,''),
		        COALESCE(readme_content,''), COALESCE(meta_cached_at,''), COALESCE(meta_etag,''),
		        COALESCE(last_accessed_at,'')
		 FROM npm_packages WHERE pkg_name = ?`, name,
	).Scan(&p.ID, &p.PkgName, &p.Description, &p.LatestVersion, &p.RepositoryURL,
		&p.HomepageURL, &p.License, &p.ReadmeContent, &p.MetaCachedAt, &p.MetaETag, &p.LastAccessedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetPackageLite 只读取代理热路径需要的字段（不加载 readme_content 大字段）。
func (db *DB) GetPackageLite(name string) (*models.Package, error) {
	p := &models.Package{}
	err := db.QueryRow(
		`SELECT id, pkg_name, COALESCE(latest_version,''),
		        COALESCE(meta_cached_at,''), COALESCE(meta_etag,''), COALESCE(last_accessed_at,'')
		 FROM npm_packages WHERE pkg_name = ?`, name,
	).Scan(&p.ID, &p.PkgName, &p.LatestVersion, &p.MetaCachedAt, &p.MetaETag, &p.LastAccessedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// TouchPackage 更新包的最后访问时间（内存累积，StartStatsFlush 定时批量落库）。
func (db *DB) TouchPackage(name string) {
	db.mem.touch(name)
}

// TouchMetaCachedAt 上游返回 304 时只刷新元数据时间戳，不重写版本表。
// 该写入不改变页面展示数据，走 execNoGen 避免推进写代次导致页面缓存全量失效。
func (db *DB) TouchMetaCachedAt(name string) {
	_, _ = db.execNoGen(`UPDATE npm_packages SET meta_cached_at = CURRENT_TIMESTAMP WHERE pkg_name = ?`, name)
}

// ListPackages 分页查询包列表：仅返回已缓存版本数 > 0 的包，
// 版本数/体积/最新版本均为已缓存口径，按最近缓存时间倒序。
func (db *DB) ListPackages(search string, offset, limit int) ([]PkgRow, error) {
	return db.ListPackagesOrdered(search, "", "", offset, limit)
}

// ListPackagesOrdered 带排序的包列表查询。sortKey/dir 必须经过
// PackageSortClause 白名单校验，非法值回退默认排序（最近缓存倒序）。
func (db *DB) ListPackagesOrdered(search, sortKey, dir string, offset, limit int) ([]PkgRow, error) {
	where, args := pkgWhere(search)
	args = append(args, limit, offset)
	rows, err := db.Query(pkgBase+where+` ORDER BY `+
		PackageSortClause(sortKey, dir)+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	items := []PkgRow{}
	for rows.Next() {
		var r PkgRow
		// LatestVersion 即物化的最新已缓存版本号
		if err := rows.Scan(&r.ID, &r.PkgName, &r.Description, &r.LatestVersion,
			&r.MetaCachedAt, &r.LastAccessedAt, &r.VersionCount, &r.TotalSize, &r.LastCached); err != nil {
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

// PackageSortClause 把排序键/方向渲染为 ORDER BY 片段（白名单防注入）。
// 合法键：name / npm_versions / size / time；方向：asc / desc；默认最近缓存倒序。
// 排序全部落在 npm_packages 的物化列上，可走索引。
func PackageSortClause(sortKey, dir string) string {
	keys := map[string]string{
		"name":     "p.pkg_name",
		"npm_versions": "p.cached_version_count",
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

// CountPackages 统计包总数（仅含已缓存包，支持搜索）。
// 计数直接读物化列，不再对 npm_versions 做 DISTINCT 聚合。
func (db *DB) CountPackages(search string) (int64, error) {
	where, args := pkgWhere(search)
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM npm_packages p`+where, args...).Scan(&n)
	return n, err
}

// DeletePackage 删除包记录（npm_versions 经外键级联删除）。
func (db *DB) DeletePackage(pkgName string) error {
	_, err := db.Exec(`DELETE FROM npm_packages WHERE pkg_name = ?`, pkgName)
	return err
}

// StatsTotals 仪表盘/首页统计：包总数、版本总数、缓存总大小（均为已缓存口径，
// 由部分索引 idx_version_cached* 支撑，不再全表扫描）。
func (db *DB) StatsTotals() (pkgCount, verCount, totalSize int64, err error) {
	err = db.QueryRow(
		`SELECT (SELECT COUNT(DISTINCT pkg_id) FROM npm_versions WHERE file_size_bytes > 0),
		        (SELECT COUNT(*) FROM npm_versions WHERE file_size_bytes > 0),
		        (SELECT COALESCE(SUM(file_size_bytes),0) FROM npm_versions WHERE file_size_bytes > 0)`).
		Scan(&pkgCount, &verCount, &totalSize)
	return
}

// RecentActivity 最近缓存的 tarball 下载记录（cached_at 部分索引支撑排序）。
// 条件写成 file_size_bytes > 0 以命中 idx_version_cached_at 部分索引。
func (db *DB) RecentActivity(limit int) ([]ActivityRow, error) {
	rows, err := db.Query(
		`SELECT p.pkg_name, v.version, COALESCE(v.file_size_bytes,0), COALESCE(v.cached_at,'')
		 FROM npm_versions v JOIN npm_packages p ON p.id = v.pkg_id
		 WHERE v.file_size_bytes > 0
		 ORDER BY v.cached_at DESC, v.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.PkgName, &r.Version, &r.FileSizeBytes, &r.CachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
