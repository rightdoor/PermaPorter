package database

import "permaporter/internal/mvnadapter/models"

// FileItem 文件列表轻量行：构件详情页/管理弹窗只需要版本、体积与缓存时间，
// 不读 sha1 等内部字段。
type FileItem struct {
	ID            int64
	RepoPath      string
	Version       string
	Filename      string
	FileSizeBytes int64
	SHA1          string
	CachedAt      string
}

// FileWithArtifact 文件全量行 + 所属构件坐标（/<path>/<file> 服务用）。
type FileWithArtifact struct {
	models.File
	GroupID    string
	ArtifactID string
}

// GetFileByRepoPath 按仓库相对路径查询全量行（repo_path 全局唯一）。
func (db *DB) GetFileByRepoPath(repoPath string) (*FileWithArtifact, error) {
	f := &FileWithArtifact{}
	err := db.QueryRow(
		`SELECT f.id, f.artifact_id, f.repo_path, COALESCE(f.version,''), f.filename,
		        COALESCE(f.file_size_bytes,0), COALESCE(f.sha1,''), COALESCE(f.file_path,''),
		        COALESCE(f.cached_at,''), a.group_id, a.artifact_id
		 FROM mvn_files f JOIN mvn_artifacts a ON a.id = f.artifact_id
		 WHERE f.repo_path = ?`, repoPath,
	).Scan(&f.ID, &f.ArtifactID, &f.RepoPath, &f.Version, &f.Filename,
		&f.FileSizeBytes, &f.SHA1, &f.FilePath,
		&f.CachedAt, &f.GroupID, &f.ArtifactID)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// EnsureFileRecord 登记一条文件记录（下载成功时调用，幂等）。
// 已存在时只更新缓存状态（size/sha1/路径/时间），版本与文件名保持首次登记值。
// 并重算构件物化统计列。
func (db *DB) EnsureFileRecord(artifactID int64, f *models.File) error {
	_, err := db.Exec(
		`INSERT INTO mvn_files (artifact_id, repo_path, version, filename, file_size_bytes, sha1, file_path, cached_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(repo_path) DO UPDATE SET
			file_size_bytes = excluded.file_size_bytes, sha1 = excluded.sha1,
			file_path = excluded.file_path, cached_at = excluded.cached_at`,
		artifactID, f.RepoPath, f.Version, f.Filename, f.FileSizeBytes, f.SHA1, f.FilePath)
	if err != nil {
		return err
	}
	db.RecalcArtifactStats(artifactID)
	return nil
}

// ListArtifactFiles 查询构件的全部文件（轻量字段，按仓库路径排序保证输出稳定）。
func (db *DB) ListArtifactFiles(artifactID int64) ([]FileItem, error) {
	rows, err := db.Query(
		`SELECT id, repo_path, COALESCE(version,''), filename,
		        COALESCE(file_size_bytes,0), COALESCE(sha1,''), COALESCE(cached_at,'')
		 FROM mvn_files WHERE artifact_id = ? ORDER BY repo_path`, artifactID)
	if err != nil {
		return nil, err
	}
	items := []FileItem{}
	for rows.Next() {
		var v FileItem
		if err := rows.Scan(&v.ID, &v.RepoPath, &v.Version, &v.Filename,
			&v.FileSizeBytes, &v.SHA1, &v.CachedAt); err != nil {
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

// SetFileCached 更新文件本体缓存信息（大小 + 相对路径）并重算构件统计列。
func (db *DB) SetFileCached(id int64, size int64, filePath string) error {
	var artifactID int64
	if err := db.QueryRow(`SELECT artifact_id FROM mvn_files WHERE id = ?`, id).Scan(&artifactID); err != nil {
		return err
	}
	if _, err := db.Exec(
		`UPDATE mvn_files SET file_size_bytes = ?, file_path = ?, cached_at = CURRENT_TIMESTAMP WHERE id = ?`,
		size, filePath, id); err != nil {
		return err
	}
	db.RecalcArtifactStats(artifactID)
	return nil
}

// ClearFile 文件本体丢失时复位缓存状态（记录保留——版本目录路径仍有效，
// 客户端下次请求会重新回源），并重算构件物化统计列。
func (db *DB) ClearFile(id int64) error {
	var artifactID int64
	_ = db.QueryRow(`SELECT artifact_id FROM mvn_files WHERE id = ?`, id).Scan(&artifactID)
	if _, err := db.Exec(
		`UPDATE mvn_files SET file_size_bytes = 0, file_path = '', cached_at = NULL WHERE id = ?`,
		id); err != nil {
		return err
	}
	if artifactID > 0 {
		db.RecalcArtifactStats(artifactID)
	}
	return nil
}

// DeleteFileByRepoPath 删除文件记录并重算构件统计列。
// 构件记录保留：仅元数据构件在 Maven 仓库协议下是合法缓存。
func (db *DB) DeleteFileByRepoPath(repoPath string) error {
	var artifactID int64
	_ = db.QueryRow(`SELECT artifact_id FROM mvn_files WHERE repo_path = ?`, repoPath).Scan(&artifactID)
	if _, err := db.Exec(`DELETE FROM mvn_files WHERE repo_path = ?`, repoPath); err != nil {
		return err
	}
	if artifactID > 0 {
		db.RecalcArtifactStats(artifactID)
	}
	return nil
}

// CachedFilePathRow 已缓存文件路径（清理对账用）。
type CachedFilePathRow struct {
	ID         int64
	ArtifactID int64
	GroupID    string
	RepoPath   string
	FilePath   string
}

// AllCachedFilePaths 返回全部已缓存本体（file_size>0）的落盘路径（清理对账用）。
// 存储树镜像仓库布局，file_path 即仓库相对路径。
func (db *DB) AllCachedFilePaths() ([]CachedFilePathRow, error) {
	rows, err := db.Query(
		`SELECT f.id, f.artifact_id, a.group_id, f.file_path
		 FROM mvn_files f JOIN mvn_artifacts a ON a.id = f.artifact_id
		 WHERE f.file_size_bytes > 0 AND f.file_path != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CachedFilePathRow{}
	for rows.Next() {
		var r CachedFilePathRow
		if err := rows.Scan(&r.ID, &r.ArtifactID, &r.GroupID, &r.FilePath); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
