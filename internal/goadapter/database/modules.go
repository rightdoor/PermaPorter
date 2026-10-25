package database

import (
	"strings"

	"permaporter/internal/goadapter/models"
)

// ModuleRow 模块列表行（go_modules 单表，物化统计列直接读取）。
type ModuleRow struct {
	models.Module
}

// ActivityRow 最近缓存活动行。
type ActivityRow struct {
	ModulePath string
	Version    string
	ZipSize    int64
	CachedAt   string
}

// moduleBase 模块列表基础查询：全部字段都是 go_modules 上的物化列，单表扫描即可。
const moduleBase = `SELECT p.id, p.module_path, COALESCE(p.latest_version,''), COALESCE(p.latest_time,''),
	COALESCE(p.summary,''), COALESCE(p.meta_cached_at,''),
	p.version_count, p.cached_version_count, p.cached_total_size, p.last_cached_at
	FROM go_modules p`

// UpsertModulePath 确保模块记录存在，返回模块 ID。
func (db *DB) UpsertModulePath(modulePath string) (int64, error) {
	_, err := db.Exec(`INSERT INTO go_modules (module_path) VALUES (?) ON CONFLICT(module_path) DO NOTHING`, modulePath)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM go_modules WHERE module_path = ?`, modulePath).Scan(&id)
	return id, err
}

// GetModule 按模块路径查询全量字段（含 README 大字段）。
func (db *DB) GetModule(modulePath string) (*models.Module, error) {
	m := &models.Module{}
	err := db.QueryRow(
		`SELECT id, module_path, COALESCE(latest_version,''), COALESCE(latest_time,''),
		        COALESCE(meta_cached_at,''), COALESCE(latest_cached_at,''), COALESCE(list_etag,''),
		        COALESCE(readme_content,''), COALESCE(readme_version,''), COALESCE(summary,''),
		        COALESCE(last_accessed_at,''), version_count, cached_version_count,
		        cached_total_size, last_cached_at
		 FROM go_modules WHERE module_path = ?`, modulePath,
	).Scan(&m.ID, &m.ModulePath, &m.LatestVersion, &m.LatestTime,
		&m.MetaCachedAt, &m.LatestCachedAt, &m.ListETag,
		&m.ReadmeContent, &m.ReadmeVersion, &m.Summary,
		&m.LastAccessedAt, &m.VersionCount, &m.CachedVersionCount,
		&m.CachedTotalSize, &m.LastCachedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetModuleLite 只读取代理热路径需要的字段（不加载 README 大字段）。
func (db *DB) GetModuleLite(modulePath string) (*models.Module, error) {
	m := &models.Module{}
	err := db.QueryRow(
		`SELECT id, module_path, COALESCE(latest_version,''),
		        COALESCE(meta_cached_at,''), COALESCE(latest_cached_at,''), COALESCE(list_etag,'')
		 FROM go_modules WHERE module_path = ?`, modulePath,
	).Scan(&m.ID, &m.ModulePath, &m.LatestVersion, &m.MetaCachedAt, &m.LatestCachedAt, &m.ListETag)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// TouchModule 更新模块的最后访问时间（内存累积，StartStatsFlush 定时批量落库）。
func (db *DB) TouchModule(modulePath string) {
	db.mem.touch(modulePath)
}

// TouchMetaCachedAt 上游返回 304 时只刷新 @v/list 缓存时间戳，不重写版本表。
// 该写入不改变页面展示数据，走 execNoGen 避免推进写代次导致页面缓存全量失效。
func (db *DB) TouchMetaCachedAt(modulePath string) {
	_, _ = db.execNoGen(`UPDATE go_modules SET meta_cached_at = CURRENT_TIMESTAMP WHERE module_path = ?`, modulePath)
}

// UpdateListCached 记录 @v/list 刷新完成：时间戳与上游 ETag。
func (db *DB) UpdateListCached(modulePath, etag string) error {
	_, err := db.execNoGen(
		`UPDATE go_modules SET meta_cached_at = CURRENT_TIMESTAMP, list_etag = ? WHERE module_path = ?`,
		etag, modulePath)
	return err
}

// UpdateLatestFromList @latest 尚未拉取时，按 @v/list 的最高 release（无则最高 prerelease）
// 推导最新版本供 UI 展示；@latest 已缓存时不覆盖。
func (db *DB) UpdateLatestFromList(moduleID int64, version, latestTime string) error {
	_, err := db.Exec(
		`UPDATE go_modules SET latest_version = ?, latest_time = ?
		 WHERE id = ? AND COALESCE(latest_cached_at,'') = ''`,
		version, latestTime, moduleID)
	return err
}

// UpdateLatestCached 记录 @latest 刷新结果：最新版本号、发布时间与时间戳。
func (db *DB) UpdateLatestCached(modulePath, version, latestTime string) error {
	_, err := db.execNoGen(
		`UPDATE go_modules SET latest_cached_at = CURRENT_TIMESTAMP,
			latest_version = ?, latest_time = ? WHERE module_path = ?`,
		version, latestTime, modulePath)
	return err
}

// SaveReadme 保存从 zip 提取的 README 与纯文本摘要（仅当版本更新时覆盖）。
func (db *DB) SaveReadme(moduleID int64, version, content, summary string) error {
	_, err := db.Exec(
		`UPDATE go_modules SET readme_content = ?, readme_version = ?, summary = ? WHERE id = ?`,
		content, version, summary, moduleID)
	return err
}

// moduleWhere 搜索条件：匹配模块路径与摘要。返回全部已收录模块（含仅元数据模块，
// GOPROXY 语义下仅元数据缓存同样合法，清理任务也不得删除）。
// LIKE 通配符被转义：用户搜索 "100%" 时按字面匹配，不会变成任意串。
func moduleWhere(search string) (string, []any) {
	if search == "" {
		return "", nil
	}
	pattern := "%" + escapeLike(search) + "%"
	return " WHERE (p.module_path LIKE ? ESCAPE '\\' OR p.summary LIKE ? ESCAPE '\\')", []any{pattern, pattern}
}

// escapeLike 转义 LIKE 模式中的通配符（配合 ESCAPE '\' 子句按字面匹配）。
func escapeLike(s string) string {
	repl := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return repl.Replace(s)
}

// ListModules 分页查询模块列表（24/页，按最近缓存倒序）。
func (db *DB) ListModules(search string, offset, limit int) ([]ModuleRow, error) {
	return db.ListModulesOrdered(search, "", "", offset, limit)
}

// ListModulesOrdered 带排序的模块列表查询。sortKey/dir 必须经过
// ModuleSortClause 白名单校验，非法值回退默认排序（最近缓存倒序）。
func (db *DB) ListModulesOrdered(search, sortKey, dir string, offset, limit int) ([]ModuleRow, error) {
	where, args := moduleWhere(search)
	args = append(args, limit, offset)
	rows, err := db.Query(moduleBase+where+` ORDER BY `+
		ModuleSortClause(sortKey, dir)+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModuleRow{}
	for rows.Next() {
		var r ModuleRow
		if err := rows.Scan(&r.ID, &r.ModulePath, &r.LatestVersion, &r.LatestTime,
			&r.Summary, &r.MetaCachedAt, &r.VersionCount, &r.CachedVersionCount,
			&r.CachedTotalSize, &r.LastCachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// ModuleSortClause 把排序键/方向渲染为 ORDER BY 片段（白名单防注入）。
// 合法键与四生态统一白名单一致：name / versions / size / time；方向：asc / desc；
// 默认最近缓存倒序。
func ModuleSortClause(sortKey, dir string) string {
	keys := map[string]string{
		"name":     "p.module_path",
		"versions": "p.version_count",
		"size":     "p.cached_total_size",
		"time":     "p.last_cached_at",
	}
	col, ok := keys[sortKey]
	if !ok {
		return "p.last_cached_at DESC, p.last_accessed_at DESC, p.id DESC"
	}
	if dir != "asc" && dir != "desc" {
		dir = "desc"
	}
	return col + " " + strings.ToUpper(dir) + ", p.id DESC"
}

// CountModules 统计模块总数（支持搜索）。
func (db *DB) CountModules(search string) (int64, error) {
	where, args := moduleWhere(search)
	var n int64
	err := db.QueryRow(`SELECT COUNT(*) FROM go_modules p`+where, args...).Scan(&n)
	return n, err
}

// DeleteModule 删除模块记录（go_versions 经外键级联删除）。
func (db *DB) DeleteModule(modulePath string) error {
	_, err := db.Exec(`DELETE FROM go_modules WHERE module_path = ?`, modulePath)
	return err
}

// StatsTotals 仪表盘/首页统计：模块总数、版本总数、缓存总大小。
// 口径：version_count = 已知版本总数；缓存大小只统计 zip。
func (db *DB) StatsTotals() (moduleCount, verCount, totalSize int64, err error) {
	err = db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM go_modules),
		        (SELECT COALESCE(SUM(version_count),0) FROM go_modules),
		        (SELECT COALESCE(SUM(cached_total_size),0) FROM go_modules)`).
		Scan(&moduleCount, &verCount, &totalSize)
	return
}

// RecentActivity 最近落盘的 zip 缓存记录（zip_cached_at 部分索引支撑排序）。
func (db *DB) RecentActivity(limit int) ([]ActivityRow, error) {
	rows, err := db.Query(
		`SELECT p.module_path, v.version, COALESCE(v.zip_size_bytes,0), COALESCE(v.zip_cached_at,'')
		 FROM go_versions v JOIN go_modules p ON p.id = v.module_id
		 WHERE v.zip_size_bytes > 0
		 ORDER BY v.zip_cached_at DESC, v.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ActivityRow{}
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.ModulePath, &r.Version, &r.ZipSize, &r.CachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
