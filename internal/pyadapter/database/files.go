package database

import "permaporter/internal/pyadapter/models"

// FileItem 文件列表轻量行：项目详情页/管理弹窗只需要版本、体积与缓存时间，
// 不读 source_url 等内部字段。
type FileItem struct {
	ID             int64
	Version        string
	Filename       string
	FileSizeBytes  int64
	SHA256         string
	RequiresPython string
	UploadTime     string
	CachedAt       string
}

// FileWithProject 文件全量行 + 所属项目名（/packages/<filename> 服务用）。
type FileWithProject struct {
	models.File
	ProjectName string
}

// UpsertFiles 批量写入锚点页解析出的文件清单（保留已缓存本体的状态）。
// 用 ON CONFLICT 只更新元数据字段（来源地址/摘要/Python 版本约束等），
// 已缓存状态（file_size_bytes、cached_at、file_path）不受影响。
// 同事务内重算项目的物化统计列。
func (db *DB) UpsertFiles(projectID int64, files []models.File) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT INTO py_files (project_id, version, filename, sha256, source_url, requires_python, upload_time)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, filename) DO UPDATE SET
			version=excluded.version, sha256=excluded.sha256, source_url=excluded.source_url,
			requires_python=excluded.requires_python, upload_time=excluded.upload_time`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range files {
		if _, err := stmt.Exec(projectID, f.Version, f.Filename, f.SHA256,
			f.SourceURL, f.RequiresPython, f.UploadTime); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	db.RecalcProjectStats(projectID)
	return nil
}

// ListProjectFiles 查询项目的全部文件（轻量字段，按文件名排序保证输出稳定）。
func (db *DB) ListProjectFiles(projectID int64) ([]FileItem, error) {
	rows, err := db.Query(
		`SELECT id, COALESCE(version,''), filename, COALESCE(file_size_bytes,0),
		        COALESCE(sha256,''), COALESCE(requires_python,''), COALESCE(upload_time,''), COALESCE(cached_at,'')
		 FROM py_files WHERE project_id = ? ORDER BY filename`, projectID)
	if err != nil {
		return nil, err
	}
	items := []FileItem{}
	for rows.Next() {
		var v FileItem
		if err := rows.Scan(&v.ID, &v.Version, &v.Filename, &v.FileSizeBytes,
			&v.SHA256, &v.RequiresPython, &v.UploadTime, &v.CachedAt); err != nil {
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
	return items, nil
}

// ListProjectFilesForVersion 查询项目指定版本的已缓存文件。
func (db *DB) ListProjectFilesForVersion(projectID int64, version string) ([]FileItem, error) {
	rows, err := db.Query(
		`SELECT id, COALESCE(version,''), filename, COALESCE(file_size_bytes,0),
		        COALESCE(sha256,''), COALESCE(requires_python,''), COALESCE(upload_time,''), COALESCE(cached_at,'')
		 FROM py_files WHERE project_id = ? AND version = ? ORDER BY filename`, projectID, version)
	if err != nil {
		return nil, err
	}
	items := []FileItem{}
	for rows.Next() {
		var v FileItem
		if err := rows.Scan(&v.ID, &v.Version, &v.Filename, &v.FileSizeBytes,
			&v.SHA256, &v.RequiresPython, &v.UploadTime, &v.CachedAt); err != nil {
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
	return items, nil
}

// GetFileByName 按发行文件名查询全量行（文件名全局唯一，/packages/<filename> 服务用）。
func (db *DB) GetFileByName(filename string) (*FileWithProject, error) {
	f := &FileWithProject{}
	err := db.QueryRow(
		`SELECT f.id, f.project_id, COALESCE(f.version,''), f.filename,
		        COALESCE(f.file_size_bytes,0), COALESCE(f.sha256,''), COALESCE(f.file_path,''),
		        COALESCE(f.source_url,''), COALESCE(f.requires_python,''), COALESCE(f.upload_time,''),
		        COALESCE(f.cached_at,''), p.project_name
		 FROM py_files f JOIN py_projects p ON p.id = f.project_id
		 WHERE f.filename = ?`, filename,
	).Scan(&f.ID, &f.ProjectID, &f.Version, &f.Filename,
		&f.FileSizeBytes, &f.SHA256, &f.FilePath,
		&f.SourceURL, &f.RequiresPython, &f.UploadTime,
		&f.CachedAt, &f.ProjectName)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// SetFileCached 记录文件本体缓存信息（大小 + 相对路径 + 缓存时间），
// 并重算项目的物化统计列。
func (db *DB) SetFileCached(id int64, size int64, filePath string) error {
	var projectID int64
	if err := db.QueryRow(`SELECT project_id FROM py_files WHERE id = ?`, id).Scan(&projectID); err != nil {
		return err
	}
	if _, err := db.Exec(
		`UPDATE py_files SET file_size_bytes = ?, file_path = ?, cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		size, filePath, id); err != nil {
		return err
	}
	db.RecalcProjectStats(projectID)
	return nil
}

// ClearFile 文件本体丢失时复位缓存状态（记录保留——锚点页仍会列出该文件），
// 并重算项目物化统计列。
func (db *DB) ClearFile(id int64) error {
	var projectID int64
	_ = db.QueryRow(`SELECT project_id FROM py_files WHERE id = ?`, id).Scan(&projectID)
	if _, err := db.Exec(
		`UPDATE py_files SET file_size_bytes = 0, file_path = '', cached_at = NULL WHERE id = ?`,
		id); err != nil {
		return err
	}
	if projectID > 0 {
		db.RecalcProjectStats(projectID)
	}
	return nil
}

// DeleteFileByID 按 ID 删除文件记录并重算项目统计列。
// 项目记录保留：仅元数据项目在 Simple 协议下是合法缓存。
func (db *DB) DeleteFileByID(id int64) error {
	var projectID int64
	_ = db.QueryRow(`SELECT project_id FROM py_files WHERE id = ?`, id).Scan(&projectID)
	if _, err := db.Exec(`DELETE FROM py_files WHERE id = ?`, id); err != nil {
		return err
	}
	if projectID > 0 {
		db.RecalcProjectStats(projectID)
	}
	return nil
}

// CachedFilePathRow 已缓存文件路径（清理对账用）。
type CachedFilePathRow struct {
	ID          int64
	ProjectID   int64
	ProjectName string
	FilePath    string
}

// AllCachedFilePaths 返回全部已缓存本体（file_size>0）的文件路径（清理对账用）。
func (db *DB) AllCachedFilePaths() ([]CachedFilePathRow, error) {
	rows, err := db.Query(
		`SELECT f.id, f.project_id, p.project_name, f.file_path
		 FROM py_files f JOIN py_projects p ON p.id = f.project_id
		 WHERE f.file_size_bytes > 0 AND f.file_path != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CachedFilePathRow{}
	for rows.Next() {
		var r CachedFilePathRow
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ProjectName, &r.FilePath); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
