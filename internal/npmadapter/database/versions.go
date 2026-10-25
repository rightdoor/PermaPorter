package database

import (
	"sort"

	"permaporter/internal/npmadapter/models"
	"permaporter/internal/core/utils"
)

const versionColumns = `id, pkg_id, version, tarball_path, COALESCE(tarball_url,''),
	COALESCE(file_size_bytes,0), COALESCE(dependencies_json,''), COALESCE(dev_dependencies_json,''),
	COALESCE(peer_dependencies_json,''), COALESCE(integrity,''), COALESCE(published_at,''), COALESCE(cached_at,'')`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(row rowScanner) (*models.Version, error) {
	v := &models.Version{}
	err := row.Scan(&v.ID, &v.PkgID, &v.Version, &v.TarballPath, &v.TarballURL,
		&v.FileSizeBytes, &v.DependenciesJSON, &v.DevDependenciesJSON, &v.PeerDependenciesJSON,
		&v.Integrity, &v.PublishedAt, &v.CachedAt)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// UpsertVersions 批量写入版本明细（保留已缓存的 tarball 信息）。
// 用 ON CONFLICT 只更新依赖/地址等元数据字段，已缓存状态（file_size_bytes、
// cached_at）不受影响；未变化的版本仍会产生一次 no-op 更新，但不再整表重写。
func (db *DB) UpsertVersions(pkgID int64, npm_versions []models.Version) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO npm_versions (pkg_id, version, tarball_path, tarball_url,
			dependencies_json, dev_dependencies_json, peer_dependencies_json, integrity, published_at, cached_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(pkg_id, version) DO UPDATE SET
			tarball_url=excluded.tarball_url,
			dependencies_json=excluded.dependencies_json,
			dev_dependencies_json=excluded.dev_dependencies_json,
			peer_dependencies_json=excluded.peer_dependencies_json,
			integrity=excluded.integrity,
			published_at=excluded.published_at
		 WHERE npm_versions.tarball_url IS DISTINCT FROM excluded.tarball_url
		    OR npm_versions.dependencies_json IS DISTINCT FROM excluded.dependencies_json
		    OR npm_versions.dev_dependencies_json IS DISTINCT FROM excluded.dev_dependencies_json
		    OR npm_versions.peer_dependencies_json IS DISTINCT FROM excluded.peer_dependencies_json
		    OR npm_versions.integrity IS DISTINCT FROM excluded.integrity
		    OR npm_versions.published_at IS DISTINCT FROM excluded.published_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range npm_versions {
		if _, err := stmt.Exec(pkgID, v.Version, v.TarballPath, v.TarballURL,
			v.DependenciesJSON, v.DevDependenciesJSON, v.PeerDependenciesJSON,
			v.Integrity, v.PublishedAt, v.CachedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetVersionByName 按包名+版本号查询。
func (db *DB) GetVersionByName(pkgName, version string) (*models.Version, error) {
	row := db.QueryRow(
		`SELECT `+versionColumns+` FROM npm_versions
		 WHERE pkg_id = (SELECT id FROM npm_packages WHERE pkg_name = ?) AND version = ?`,
		pkgName, version)
	return scanVersion(row)
}

// VersionLite 已缓存版本的轻量行：列表/弹窗/搜索场景只需要版本号、体积与缓存时间，
// 避免为每行读取 dependencies_json 等大字段。
type VersionLite struct {
	ID            int64
	Version       string
	FileSizeBytes int64
	CachedAt      string
}

// CachedVersionsLite 查询包所有已缓存的版本（file_size > 0，按版本号降序）。
func (db *DB) CachedVersionsLite(pkgID int64) ([]VersionLite, error) {
	rows, err := db.Query(
		`SELECT id, version, COALESCE(file_size_bytes,0), COALESCE(cached_at,'')
		 FROM npm_versions WHERE pkg_id = ? AND file_size_bytes > 0`, pkgID)
	if err != nil {
		return nil, err
	}
	items := []VersionLite{}
	for rows.Next() {
		var v VersionLite
		if err := rows.Scan(&v.ID, &v.Version, &v.FileSizeBytes, &v.CachedAt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sortVersionsLiteDesc(items)
	return items, nil
}

// VersionItem 版本列表轻量行：不含 dependencies_json 等大字段。
// 包详情页的版本表只需要版本号/体积/时间，用它可以避免为上千个版本读取依赖 JSON。
type VersionItem struct {
	ID            int64
	Version       string
	FileSizeBytes int64
	PublishedAt   string
	CachedAt      string
}

// ListVersionItems 查询包的全部版本（轻量字段，按版本号降序）。
func (db *DB) ListVersionItems(pkgID int64) ([]VersionItem, error) {
	rows, err := db.Query(
		`SELECT id, version, COALESCE(file_size_bytes,0),
		        COALESCE(published_at,''), COALESCE(cached_at,'')
		 FROM npm_versions WHERE pkg_id = ?`, pkgID)
	if err != nil {
		return nil, err
	}
	items := []VersionItem{}
	for rows.Next() {
		var v VersionItem
		if err := rows.Scan(&v.ID, &v.Version, &v.FileSizeBytes, &v.PublishedAt, &v.CachedAt); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	keys := make([]utils.VersionKey, len(items))
	for i, v := range items {
		keys[i] = utils.NewVersionKey(v.Version)
	}
	sort.Slice(items, func(i, j int) bool { return keys[i].Compare(keys[j]) > 0 })
	return items, nil
}

// SetVersionFile 更新版本 tarball 缓存信息（大小 + 缓存时间），
// 并在同事务内物化维护 npm_packages 的统计列（最新已缓存版本号/版本数/体积/最近缓存时间）。
func (db *DB) SetVersionFile(id int64, size int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pkgID int64
	var version string
	if err := tx.QueryRow(`SELECT pkg_id, version FROM npm_versions WHERE id = ?`, id).
		Scan(&pkgID, &version); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE npm_versions SET file_size_bytes = ?, cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		size, id); err != nil {
		return err
	}
	var cur string
	_ = tx.QueryRow(`SELECT COALESCE(latest_cached_version,'') FROM npm_packages WHERE id = ?`, pkgID).Scan(&cur)
	latest := cur
	if cur == "" || utils.VersionCompare(version, cur) > 0 {
		latest = version
	}
	if err := updatePackageStatsTx(tx, pkgID, latest); err != nil {
		return err
	}
	return tx.Commit()
}

// updatePackageStatsTx 用子查询重算并写入包的物化统计列（调用方需在写事务内）。
// 子查询走 npm_versions(pkg_id) 索引，单包版本数级别，成本可忽略。
func updatePackageStatsTx(tx *guardTx, pkgID int64, latest string) error {
	_, err := tx.Exec(
		`UPDATE npm_packages SET
			latest_cached_version = ?,
			cached_version_count = (SELECT COUNT(*) FROM npm_versions WHERE pkg_id = ? AND file_size_bytes > 0),
			cached_total_size = (SELECT COALESCE(SUM(file_size_bytes),0) FROM npm_versions WHERE pkg_id = ? AND file_size_bytes > 0),
			last_cached_at = (SELECT COALESCE(MAX(cached_at),'') FROM npm_versions WHERE pkg_id = ? AND file_size_bytes > 0)
		 WHERE id = ?`, latest, pkgID, pkgID, pkgID, pkgID)
	return err
}

// DeleteVersion 删除版本记录并重算物化统计列。
func (db *DB) DeleteVersion(id int64) error {
	var pkgID int64
	_ = db.QueryRow(`SELECT pkg_id FROM npm_versions WHERE id = ?`, id).Scan(&pkgID)
	if _, err := db.Exec(`DELETE FROM npm_versions WHERE id = ?`, id); err != nil {
		return err
	}
	if pkgID > 0 {
		db.RecalcLatestCachedVersion(pkgID)
	}
	return nil
}

// RecalcLatestCachedVersion 重算并物化包的最新已缓存版本号与统计列
// （供删除/清理后修正）。CachedVersionsLite 已按版本号降序返回。
func (db *DB) RecalcLatestCachedVersion(pkgID int64) {
	vs, err := db.CachedVersionsLite(pkgID)
	if err != nil {
		return
	}
	latest, lastCached := "", ""
	var total int64
	for i, v := range vs {
		if i == 0 {
			latest = v.Version
		}
		if v.CachedAt > lastCached {
			lastCached = v.CachedAt
		}
		total += v.FileSizeBytes
	}
	_, _ = db.Exec(
		`UPDATE npm_packages SET latest_cached_version = ?, cached_version_count = ?,
			cached_total_size = ?, last_cached_at = ? WHERE id = ?`,
		latest, len(vs), total, lastCached, pkgID)
}

func sortVersionsLiteDesc(items []VersionLite) {
	keys := make([]utils.VersionKey, len(items))
	for i, v := range items {
		keys[i] = utils.NewVersionKey(v.Version)
	}
	sort.Slice(items, func(i, j int) bool { return keys[i].Compare(keys[j]) > 0 })
}
