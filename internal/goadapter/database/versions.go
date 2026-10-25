package database

import (
	"sort"

	"golang.org/x/mod/semver"

	"permaporter/internal/goadapter/models"
)

// MarkListed 把 @v/list 中的版本写入 go_versions 表（只记 listed_at，不创建产物记录）。
// 已存在的版本顺带补全 is_pseudo；不触碰任何已缓存状态。
func (db *DB) MarkListed(moduleID int64, go_versions []string, isPseudo func(string) bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO go_versions (module_id, version, listed_at, is_pseudo)
		 VALUES (?, ?, CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(module_id, version) DO UPDATE SET
			listed_at = CURRENT_TIMESTAMP,
			is_pseudo = excluded.is_pseudo`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range go_versions {
		if _, err := stmt.Exec(moduleID, v, isPseudo(v)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// version_count 物化列由子查询重算（单模块版本数级别，成本可忽略）
	_, err = db.Exec(
		`UPDATE go_modules SET version_count = (SELECT COUNT(*) FROM go_versions WHERE module_id = ?) WHERE id = ?`,
		moduleID, moduleID)
	return err
}

// UpsertVersionRow 确保版本记录存在（产物下载流程用），返回版本记录 ID。
func (db *DB) UpsertVersionRow(moduleID int64, version string, isPseudo bool) (int64, error) {
	pseudo := 0
	if isPseudo {
		pseudo = 1
	}
	_, err := db.Exec(
		`INSERT INTO go_versions (module_id, version, is_pseudo) VALUES (?, ?, ?)
		 ON CONFLICT(module_id, version) DO NOTHING`, moduleID, version, pseudo)
	if err != nil {
		return 0, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM go_versions WHERE module_id = ? AND version = ?`, moduleID, version).Scan(&id)
	return id, err
}

const versionColumns = `id, module_id, version, COALESCE(info_time,''), COALESCE(info_cached_at,''),
	COALESCE(mod_cached_at,''), COALESCE(zip_cached_at,''), COALESCE(zip_size_bytes,0),
	COALESCE(mod_size_bytes,0), COALESCE(zip_path,''), COALESCE(ziphash,''),
	COALESCE(mod_content,''), COALESCE(requirements_json,''), COALESCE(go_version,''),
	COALESCE(listed_at,''), is_pseudo`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(row rowScanner) (*models.Version, error) {
	v := &models.Version{}
	var pseudo int
	err := row.Scan(&v.ID, &v.ModuleID, &v.Version, &v.InfoTime, &v.InfoCachedAt,
		&v.ModCachedAt, &v.ZipCachedAt, &v.ZipSizeBytes,
		&v.ModSizeBytes, &v.ZipPath, &v.Ziphash,
		&v.ModContent, &v.RequirementsJSON, &v.GoVersion,
		&v.ListedAt, &pseudo)
	if err != nil {
		return nil, err
	}
	v.IsPseudo = pseudo != 0
	return v, nil
}

// GetVersion 按模块路径+版本号查询全量字段。
func (db *DB) GetVersion(modulePath, version string) (*models.Version, error) {
	row := db.QueryRow(
		`SELECT `+versionColumns+` FROM go_versions
		 WHERE module_id = (SELECT id FROM go_modules WHERE module_path = ?) AND version = ?`,
		modulePath, version)
	return scanVersion(row)
}

// GetVersionLite 代理热路径的轻量版本行（不加载 mod_content 大字段）。
type VersionLite struct {
	ID           int64
	ModuleID     int64
	Version      string
	ZipSizeBytes int64
	ZipPath      string
	Ziphash      string
}

// GetVersionLiteByPath 查询版本的部分缓存状态字段。
func (db *DB) GetVersionLiteByPath(modulePath, version string) (*VersionLite, error) {
	v := &VersionLite{}
	err := db.QueryRow(
		`SELECT id, module_id, version, COALESCE(zip_size_bytes,0), COALESCE(zip_path,''), COALESCE(ziphash,'')
		 FROM go_versions
		 WHERE module_id = (SELECT id FROM go_modules WHERE module_path = ?) AND version = ?`,
		modulePath, version).Scan(&v.ID, &v.ModuleID, &v.Version, &v.ZipSizeBytes, &v.ZipPath, &v.Ziphash)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// VersionItem 版本列表轻量行：模块详情页版本表只需要状态字段，不读 mod_content。
type VersionItem struct {
	ID           int64
	Version      string
	InfoTime     string
	InfoCachedAt string
	ModCachedAt  string
	ZipCachedAt  string
	ZipSizeBytes int64
	ModSizeBytes int64
	ListedAt     string
	IsPseudo     bool
}

// ListVersionItems 查询模块的全部版本（轻量字段，semver 降序，伪版本沉底）。
func (db *DB) ListVersionItems(moduleID int64) ([]VersionItem, error) {
	rows, err := db.Query(
		`SELECT id, version, COALESCE(info_time,''), COALESCE(info_cached_at,''),
		        COALESCE(mod_cached_at,''), COALESCE(zip_cached_at,''),
		        COALESCE(zip_size_bytes,0), COALESCE(mod_size_bytes,0),
		        COALESCE(listed_at,''), is_pseudo
		 FROM go_versions WHERE module_id = ?`, moduleID)
	if err != nil {
		return nil, err
	}
	items := []VersionItem{}
	for rows.Next() {
		var v VersionItem
		var pseudo int
		if err := rows.Scan(&v.ID, &v.Version, &v.InfoTime, &v.InfoCachedAt,
			&v.ModCachedAt, &v.ZipCachedAt, &v.ZipSizeBytes, &v.ModSizeBytes,
			&v.ListedAt, &pseudo); err != nil {
			rows.Close()
			return nil, err
		}
		v.IsPseudo = pseudo != 0
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	SortVersionItemsDesc(items)
	return items, nil
}

// SetVersionInfo 记录 .info 缓存信息（发布时间 + 缓存时间戳）。
func (db *DB) SetVersionInfo(id int64, infoTime string) error {
	_, err := db.Exec(
		`UPDATE go_versions SET info_time = ?, info_cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		infoTime, id)
	return err
}

// SetVersionMod 记录 .mod 缓存信息（大小、原文、require 解析结果、go 版本）。
func (db *DB) SetVersionMod(id int64, size int64, content, requirementsJSON, goVersion string) error {
	_, err := db.Exec(
		`UPDATE go_versions SET mod_size_bytes = ?, mod_content = ?, requirements_json = ?,
			go_version = ?, mod_cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		size, content, requirementsJSON, goVersion, id)
	return err
}

// SetVersionZip 记录 .zip 缓存信息，并在同事务内维护模块的物化统计列。
func (db *DB) SetVersionZip(id int64, size int64, zipPath, ziphash string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var moduleID int64
	if err := tx.QueryRow(`SELECT module_id FROM go_versions WHERE id = ?`, id).Scan(&moduleID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE go_versions SET zip_size_bytes = ?, zip_path = ?, ziphash = ?,
			zip_cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		size, zipPath, ziphash, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE go_modules SET
			cached_version_count = (SELECT COUNT(*) FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0),
			cached_total_size = (SELECT COALESCE(SUM(zip_size_bytes),0) FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0),
			last_cached_at = (SELECT COALESCE(MAX(zip_cached_at),'') FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0)
		 WHERE id = ?`, moduleID, moduleID, moduleID, moduleID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearVersionZip zip 文件丢失时复位 zip 缓存状态（版本记录保留，可能仍在 @v/list 中），
// 并重算模块物化统计列。
func (db *DB) ClearVersionZip(id int64) error {
	var moduleID int64
	_ = db.QueryRow(`SELECT module_id FROM go_versions WHERE id = ?`, id).Scan(&moduleID)
	if _, err := db.Exec(
		`UPDATE go_versions SET zip_size_bytes = 0, zip_path = '', ziphash = '', zip_cached_at = NULL WHERE id = ?`,
		id); err != nil {
		return err
	}
	if moduleID > 0 {
		db.RecalcModuleZipStats(moduleID)
	}
	return nil
}

// DeleteVersionByPath 删除版本记录并重算模块物化统计列，返回该模块剩余版本数。
func (db *DB) DeleteVersionByPath(modulePath, version string) (int64, error) {
	var moduleID int64
	_ = db.QueryRow(`SELECT id FROM go_modules WHERE module_path = ?`, modulePath).Scan(&moduleID)
	if _, err := db.Exec(
		`DELETE FROM go_versions
		 WHERE module_id = (SELECT id FROM go_modules WHERE module_path = ?) AND version = ?`,
		modulePath, version); err != nil {
		return 0, err
	}
	if moduleID > 0 {
		db.RecalcModuleZipStats(moduleID)
		var remaining int64
		err := db.QueryRow(`SELECT COUNT(*) FROM go_versions WHERE module_id = ?`, moduleID).Scan(&remaining)
		return remaining, err
	}
	return 0, nil
}

// RecalcModuleZipStats 重算并物化模块的 zip 统计列（删除/清理后修正）。
func (db *DB) RecalcModuleZipStats(moduleID int64) {
	_, _ = db.Exec(
		`UPDATE go_modules SET
			version_count = (SELECT COUNT(*) FROM go_versions WHERE module_id = ?),
			cached_version_count = (SELECT COUNT(*) FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0),
			cached_total_size = (SELECT COALESCE(SUM(zip_size_bytes),0) FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0),
			last_cached_at = (SELECT COALESCE(MAX(zip_cached_at),'') FROM go_versions WHERE module_id = ? AND zip_size_bytes > 0)
		 WHERE id = ?`, moduleID, moduleID, moduleID, moduleID, moduleID)
}

// SortVersionItemsDesc 按 semver 降序排序（伪版本按其语义版本参与比较；
// 无法解析的版本字符串按字典序沉底，保证输出稳定）。
func SortVersionItemsDesc(items []VersionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return semverLess(items[j].Version, items[i].Version)
	})
}

// semverLess 严格升序比较：两侧都能解析时用 semver.Compare，
// 否则退化为字符串比较（保持稳定输出）。
func semverLess(a, b string) bool {
	if semver.IsValid(a) && semver.IsValid(b) {
		return semver.Compare(a, b) < 0
	}
	return a < b
}

// CachedArtifactRow 已缓存产物记录（清理对账用）。
type CachedArtifactRow struct {
	VersionID    int64
	ModuleID     int64
	ModulePath   string // 规范模块路径
	Version      string
	ZipPath      string // 相对存储目录的路径（/ 分隔）
	ZipSizeBytes int64
	ModSizeBytes int64
	ModCached    bool
	InfoCached   bool
	MetaCachedAt string // 模块 @v/list 刷新时间
	LatestCachedAt string // 模块 @latest 刷新时间
}

// AllCachedArtifacts 返回全部已缓存产物与其模块元数据缓存状态（清理对账用）。
func (db *DB) AllCachedArtifacts() ([]CachedArtifactRow, error) {
	rows, err := db.Query(
		`SELECT v.id, v.module_id, p.module_path, v.version, COALESCE(v.zip_path,''),
		        COALESCE(v.zip_size_bytes,0), COALESCE(v.mod_size_bytes,0),
		        COALESCE(v.mod_cached_at,'') != '', COALESCE(v.info_cached_at,'') != '',
		        COALESCE(p.meta_cached_at,''), COALESCE(p.latest_cached_at,'')
		 FROM go_versions v JOIN go_modules p ON p.id = v.module_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CachedArtifactRow{}
	for rows.Next() {
		var r CachedArtifactRow
		if err := rows.Scan(&r.VersionID, &r.ModuleID, &r.ModulePath, &r.Version, &r.ZipPath,
			&r.ZipSizeBytes, &r.ModSizeBytes, &r.ModCached, &r.InfoCached,
			&r.MetaCachedAt, &r.LatestCachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// AllModulesForCleanup 返回全部模块路径及其元数据缓存时间（清理对账用，
// 用于判定 list/latest.json 文件是否有效）。
type ModuleMetaRow struct {
	ModulePath     string
	MetaCachedAt   string
	LatestCachedAt string
}

func (db *DB) AllModulesForCleanup() ([]ModuleMetaRow, error) {
	rows, err := db.Query(
		`SELECT module_path, COALESCE(meta_cached_at,''), COALESCE(latest_cached_at,'') FROM go_modules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModuleMetaRow{}
	for rows.Next() {
		var r ModuleMetaRow
		if err := rows.Scan(&r.ModulePath, &r.MetaCachedAt, &r.LatestCachedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// AllModuleVersions 返回全部已知 (模块路径, 版本) 对（清理对账用）：
// 任何匹配已知对的产物文件都不是孤立文件（产物不可变，宁可保留）。
type ModuleVersionRow struct {
	ModulePath string
	Version    string
}

func (db *DB) AllModuleVersions() ([]ModuleVersionRow, error) {
	rows, err := db.Query(
		`SELECT p.module_path, v.version FROM go_versions v JOIN go_modules p ON p.id = v.module_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ModuleVersionRow{}
	for rows.Next() {
		var r ModuleVersionRow
		if err := rows.Scan(&r.ModulePath, &r.Version); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// DeleteOrphanVersions 删除无对应模块记录的孤立版本（外键关闭期间可能残留）。
func (db *DB) DeleteOrphanVersions() (int64, error) {
	res, err := db.Exec(
		`DELETE FROM go_versions WHERE NOT EXISTS (SELECT 1 FROM go_modules p WHERE p.id = go_versions.module_id)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
