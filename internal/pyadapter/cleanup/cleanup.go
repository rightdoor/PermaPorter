// Package cleanup 提供存储目录与数据库的 py 生态安全清理。
//
// Simple 协议语义下清理只做以下安全动作：
//  1. DB 中标记为已缓存但文件缺失的发行文件：复位缓存状态（保留记录——锚点页仍会列出它，
//     客户端下次请求会重新回源）；
//  2. storage/packages 下无 DB 记录且修改时间超过 30 分钟的孤立文件：删除；
//  3. storage/packages 下残留的下载临时文件（.pydl-*）：超过 1 小时删除。
//
// **绝不删除**：py_projects 记录与仅元数据项目（只拉过锚点页的项目是合法缓存，
// pip 只查锚点页即可拿到可用版本列表）、任何被 DB 引用的有效文件。
// 与 go/npm 清理器语义独立（py 侧"仅元数据"是合法终态），不得通用化复用。
package cleanup

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/proxy"
)

const (
	// activeWindow 孤立文件保护窗口：窗口内修改的文件可能是下载进行中的产物，跳过。
	activeWindow = 30 * time.Minute
	// tmpMaxAge 下载临时文件保留时长。
	tmpMaxAge = time.Hour
)

// Result 一次清理的统计结果。
type Result struct {
	ResetFileRecords int // 复位的"有记录无文件"缓存状态
	RemovedFiles     int // 删除的孤立发行文件
	RemovedTmpFiles  int // 删除的下载临时残留
	RecalcProjects   int // 重算统计的项目数
}

// Run 执行一次完整清理并返回统计。
func Run(db *database.DB, storageDir string) Result {
	var res Result

	// 1) 有缓存记录但物理文件缺失 → 复位缓存状态（保留记录）
	validPaths := map[string]bool{}
	resetProjectIDs := map[int64]bool{}
	files, err := db.AllCachedFilePaths()
	if err != nil {
		log.Printf("[cleanup] 读取已缓存文件失败: %v", err)
	}
	for _, f := range files {
		full, aerr := proxy.StorageFilePath(storageDir, filepath.FromSlash(f.FilePath))
		if aerr != nil {
			// 越界记录不可信：复位缓存状态（保留记录，锚点页仍可服务）
			log.Printf("[cleanup] 文件记录路径越界，复位缓存状态 %s: %v", f.ProjectName, aerr)
			if err := db.ClearFile(f.ID); err == nil {
				res.ResetFileRecords++
				resetProjectIDs[f.ProjectID] = true
			}
			continue
		}
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() && fi.Size() > 0 {
			validPaths[filepath.ToSlash(f.FilePath)] = true
			continue
		}
		if err := db.ClearFile(f.ID); err == nil {
			res.ResetFileRecords++
			resetProjectIDs[f.ProjectID] = true
		}
	}
	for id := range resetProjectIDs {
		db.RecalcProjectStats(id)
		res.RecalcProjects++
	}

	// 2) 孤立发行文件：无 DB 记录且修改时间超过保护窗口 → 删除
	pkgDir := filepath.Join(storageDir, "packages")
	if _, err := os.Stat(pkgDir); err == nil {
		_ = filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(storageDir, path)
			if err != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.Base(relSlash), ".pydl-") {
				// 下载临时残留：超过保留时长即删
				if time.Since(info.ModTime()) >= tmpMaxAge {
					if os.Remove(path) == nil {
						res.RemovedTmpFiles++
					}
				}
				return nil
			}
			if time.Since(info.ModTime()) < activeWindow {
				return nil
			}
			if validPaths[relSlash] {
				return nil
			}
			if os.Remove(path) == nil {
				res.RemovedFiles++
			}
			return nil
		})
	}

	// 3) 物化统计列校正：复位过记录时全量重算，防止计数漂移
	if res.ResetFileRecords > 0 {
		if err := db.BackfillProjectStats(); err == nil {
			res.RecalcProjects++
		}
	}
	return res
}
