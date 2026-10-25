package database

// CachedVersionRow 已缓存版本记录（清理对账用）。
type CachedVersionRow struct {
	ID          int64
	PkgID       int64
	PkgName     string
	TarballPath string // 相对存储目录的路径（/ 分隔）
}

// PkgVersionCount 包及其实例数量（清理对账用）。
// 附带访问/缓存时间，供清理跳过活跃包与保留期内的元数据。
type PkgVersionCount struct {
	PkgName        string
	VersionCount   int64
	CachedCount    int64
	LastAccessedAt string
	MetaCachedAt   string
}

// AllCachedVersions 返回所有已缓存（file_size>0）的版本及其包名。
func (db *DB) AllCachedVersions() ([]CachedVersionRow, error) {
	rows, err := db.Query(
		`SELECT v.id, v.pkg_id, p.pkg_name, v.tarball_path
		 FROM npm_versions v JOIN npm_packages p ON p.id = v.pkg_id
		 WHERE v.file_size_bytes > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CachedVersionRow{}
	for rows.Next() {
		var r CachedVersionRow
		if err := rows.Scan(&r.ID, &r.PkgID, &r.PkgName, &r.TarballPath); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// AllPackagesWithCounts 返回全部包及其版本/已缓存版本数量。
func (db *DB) AllPackagesWithCounts() ([]PkgVersionCount, error) {
	rows, err := db.Query(
		`SELECT p.pkg_name, COUNT(v.id), COALESCE(SUM(CASE WHEN v.file_size_bytes > 0 THEN 1 ELSE 0 END),0),
		        COALESCE(p.last_accessed_at,''), COALESCE(p.meta_cached_at,'')
		 FROM npm_packages p LEFT JOIN npm_versions v ON v.pkg_id = p.id
		 GROUP BY p.pkg_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PkgVersionCount{}
	for rows.Next() {
		var r PkgVersionCount
		if err := rows.Scan(&r.PkgName, &r.VersionCount, &r.CachedCount,
			&r.LastAccessedAt, &r.MetaCachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// DeleteVersionByID 按 ID 删除版本记录（调用方负责重算 latest_cached_version）。
func (db *DB) DeleteVersionByID(id int64) error {
	_, err := db.Exec(`DELETE FROM npm_versions WHERE id = ?`, id)
	return err
}

// DeleteOrphanVersions 删除无对应包记录的孤立版本（外键关闭期间可能残留）。
func (db *DB) DeleteOrphanVersions() (int64, error) {
	res, err := db.Exec(
		`DELETE FROM npm_versions WHERE NOT EXISTS (SELECT 1 FROM npm_packages p WHERE p.id = npm_versions.pkg_id)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
