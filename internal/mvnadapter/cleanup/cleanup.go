// Package cleanup 提供存储目录与数据库的 mvn 生态安全清理。
//
// Maven 仓库协议语义下清理只做以下安全动作：
//  1. DB 中标记为已缓存但文件缺失的构件文件：复位缓存状态（保留记录——仓库路径
//     仍有效，客户端下次请求会重新回源）；
//  2. 存储树（镜像仓库布局）下无 DB 记录且修改时间超过 30 分钟的孤立文件：删除；
//  3. 存储树下残留的下载临时文件（.mvndl-*）：超过 1 小时删除；
//  4. 删除文件后剪枝空目录（镜像布局会随清理产生空版本目录）。
//
// **绝不删除**：mvn_artifacts 记录、mvn_metadata 元数据缓存与仅元数据构件
//（只拉过 maven-metadata.xml 的构件是合法缓存，版本区间/动态版本解析只读元数据
// 即可工作）、任何被 DB 引用的有效文件。
// 与 go/npm/py 清理器语义独立（mvn 侧"仅元数据"是合法终态），不得通用化复用。
package cleanup

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"permaporter/internal/mvnadapter/database"
	"permaporter/internal/mvnadapter/proxy"
)

const (
	// activeWindow 孤立文件保护窗口：窗口内修改的文件可能是下载进行中的产物，跳过。
	activeWindow = 30 * time.Minute
	// tmpMaxAge 下载临时文件保留时长。
	tmpMaxAge = time.Hour
)

// tmpPrefix 下载临时文件前缀（与引擎 downloadArtifact 的 CreateTemp 模式一致）。
const tmpPrefix = ".mvndl-"

// Result 一次清理的统计结果。
type Result struct {
	ResetFileRecords int // 复位的"有记录无文件"缓存状态
	RemovedFiles     int // 删除的孤立构件文件
	RemovedTmpFiles  int // 删除的下载临时残留
	PrunedDirs       int // 剪枝的空目录
	RecalcArtifacts  int // 重算统计的构件数
}

// Run 执行一次完整清理并返回统计。
func Run(db *database.DB, storageDir string) Result {
	var res Result

	// 1) 有缓存记录但物理文件缺失 → 复位缓存状态（记录保留）
	validPaths := map[string]bool{}
	resetArtifactIDs := map[int64]bool{}
	files, err := db.AllCachedFilePaths()
	if err != nil {
		log.Printf("[mvn-cleanup] 读取已缓存文件失败: %v", err)
	}
	for _, f := range files {
		full, aerr := proxy.StorageFilePath(storageDir, filepath.FromSlash(f.FilePath))
		if aerr != nil {
			// 越界记录不可信：复位缓存状态（保留记录，路径仍可重新回源）
			log.Printf("[mvn-cleanup] 文件记录路径越界，复位缓存状态 %s: %v", f.GroupID, aerr)
			if err := db.ClearFile(f.ID); err == nil {
				res.ResetFileRecords++
				resetArtifactIDs[f.ArtifactID] = true
			}
			continue
		}
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() && fi.Size() > 0 {
			validPaths[filepath.ToSlash(f.FilePath)] = true
			continue
		}
		if err := db.ClearFile(f.ID); err == nil {
			res.ResetFileRecords++
			resetArtifactIDs[f.ArtifactID] = true
		}
	}
	for id := range resetArtifactIDs {
		db.RecalcArtifactStats(id)
		res.RecalcArtifacts++
	}

	// 2) 孤立构件文件与临时残留：全树扫描（存储树镜像仓库布局）
	if _, err := os.Stat(storageDir); err == nil {
		allDirs := map[string]bool{}
		_ = filepath.Walk(storageDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				if path != storageDir {
					allDirs[path] = true
				}
				return nil
			}
			rel, rerr := filepath.Rel(storageDir, path)
			if rerr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.Base(relSlash), tmpPrefix) {
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
		// 3) 空目录剪枝：以剪枝时点的磁盘内容为准（本轮删除孤立文件后变空的目录
		// 同样回收），自底向上删除，保证父目录在子目录删除后才判定为空。
		dirs := make([]string, 0, len(allDirs))
		for d := range allDirs {
			dirs = append(dirs, d)
		}
		// 深度倒序排序：镜像布局的大存储树可能有数万目录，插入排序会退化
		sort.Slice(dirs, func(i, j int) bool { return depth(dirs[i]) > depth(dirs[j]) })
		for _, d := range dirs {
			if entries, derr := os.ReadDir(d); derr == nil && len(entries) == 0 {
				if os.Remove(d) == nil {
					res.PrunedDirs++
				}
			}
		}
	}

	// 4) 物化统计列校正：复位过记录时全量重算，防止计数漂移
	if res.ResetFileRecords > 0 {
		if err := db.BackfillArtifactStats(); err == nil {
			res.RecalcArtifacts++
		}
	}
	return res
}

func depth(p string) int { return strings.Count(p, string(filepath.Separator)) }
