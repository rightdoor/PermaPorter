// Package cleanup 提供存储目录与数据库的孤立数据清理。
// 清理对象：
//   - 无包关联的孤立版本记录（删除记录）
//   - 已缓存版本记录但其 tarball 物理文件缺失（删除记录）
//   - 磁盘上无 DB 记录的孤立 tarball 文件（删除文件；近期修改的文件跳过，可能是下载进行中的产物）
//   - 磁盘上无 DB 记录的孤立 meta JSON（删除文件，含 @scope/ 子目录）
//   - 无任何版本记录且 meta 文件缺失的孤立包记录（删除记录）
//
// 防误删：批量下载进行中的包处于"元数据已入库、tarball 未落盘"状态，
// 清理会跳过最近活跃（30 分钟内有访问）的包。
package cleanup

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"permaporter/internal/npmadapter/database"
	"permaporter/internal/npmadapter/proxy"
)

const (
	// activeWindow 活跃包保护窗口：窗口内有访问记录的包视为下载进行中，跳过清理。
	activeWindow = 30 * time.Minute
)

// Result 一次清理的统计结果。
type Result struct {
	RemovedTarballs  int // 删除的孤立 tarball 文件
	RemovedVersions  int // 删除的物理文件缺失的版本记录
	RemovedMetas     int // 删除的孤立 meta JSON
	RemovedPackages  int // 删除的孤立包记录
	RemovedMetaOnly  int // 删除的仅元数据缓存包（无任何已缓存版本）
	RemovedOrphanVer int // 删除的无包关联的孤立版本记录
	RemovedEmptyDirs int // 删除的空目录
	RemovedMetaTmp   int // 删除的元数据临时残留（.meta-*）
}

// Run 执行一次完整清理并返回统计。
func Run(db *database.DB, storageDir string) Result {
	var res Result

	// 0) 无包关联的孤立版本记录（外键检查曾关闭时可能残留）→ 直接删除
	if n, err := db.DeleteOrphanVersions(); err == nil {
		res.RemovedOrphanVer = int(n)
	} else {
		log.Printf("[cleanup] 删除孤立版本失败: %v", err)
	}

	// 1) 已缓存版本但物理文件缺失 → 删除记录
	cached, err := db.AllCachedVersions()
	if err != nil {
		log.Printf("[cleanup] 读取已缓存版本失败: %v", err)
	}
	validPaths := map[string]bool{}
	recalcPkgIDs := map[int64]bool{}
	for _, v := range cached {
		// 越界断言：历史库中的非法版本键（如被污染上游写入的 ../../..）不触达存储树外
		full, ferr := proxy.StorageFilePath(storageDir, filepath.FromSlash(v.TarballPath))
		if ferr != nil {
			log.Printf("[cleanup] 版本记录路径越界，按文件缺失删除记录: %v", ferr)
			if err := db.DeleteVersionByID(v.ID); err == nil {
				res.RemovedVersions++
				recalcPkgIDs[v.PkgID] = true
			}
			continue
		}
		if fi, err := os.Stat(full); err != nil || fi.IsDir() {
			if err := db.DeleteVersionByID(v.ID); err == nil {
				res.RemovedVersions++
				recalcPkgIDs[v.PkgID] = true
			}
		} else {
			validPaths[v.TarballPath] = true
		}
	}
	// 被删记录若为该包最新已缓存版本，需重算物化列
	for pkgID := range recalcPkgIDs {
		db.RecalcLatestCachedVersion(pkgID)
	}

	// 2) 孤立 tarball 文件（无对应版本记录）；近期修改的文件可能是下载进行中的
	//    临时/最终产物，跳过避免误删
	pkgDir := filepath.Join(storageDir, "packages")
	if _, err := os.Stat(pkgDir); err == nil {
		_ = filepath.Walk(pkgDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if time.Since(info.ModTime()) < activeWindow {
				return nil
			}
			rel, err := filepath.Rel(storageDir, path)
			if err != nil {
				return nil
			}
			if validPaths[filepath.ToSlash(rel)] {
				return nil
			}
			if os.Remove(path) == nil {
				res.RemovedTarballs++
			}
			return nil
		})
		cleanEmptyDirs(pkgDir, &res)
	}

	// 3) 孤立 meta JSON（含 meta/@scope/ 子目录）；近期修改的文件可能是下载进行中的
	metaDir := filepath.Join(storageDir, "meta")
	rows, err := db.AllPackagesWithCounts()
	if err != nil {
		log.Printf("[cleanup] 读取包列表失败: %v", err)
		rows = nil
	}
	nameSet := map[string]bool{}
	for _, r := range rows {
		nameSet[r.PkgName] = true
	}
	_ = filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// 元数据原子写残留的临时文件（.meta-*）：无对应 .json 语义，
		// 崩溃/断电后永久残留，超过保护窗口即回收
		if strings.HasPrefix(filepath.Base(path), ".meta-") {
			if time.Since(info.ModTime()) >= activeWindow {
				if os.Remove(path) == nil {
					res.RemovedMetaTmp++
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		if time.Since(info.ModTime()) < activeWindow {
			return nil
		}
		rel, rerr := filepath.Rel(metaDir, path)
		if rerr != nil {
			return nil
		}
		name := strings.TrimSuffix(filepath.ToSlash(rel), ".json")
		if !nameSet[name] {
			if os.Remove(path) == nil {
				res.RemovedMetas++
			}
		}
		return nil
	})
	// 清理 meta 下遗留的空目录（如已删包留下的 @scope/ 空目录）
	cleanEmptyDirs(metaDir, &res)

	// 4) 仅元数据缓存包：没有任何已缓存版本的包记录视为无用缓存，
	//    连同其 meta JSON 一并删除（客户端下次请求时会重新从上游拉取）。
	//    仅跳过最近活跃（最近有访问，可能正处于批量下载 tarball 流程中）的包。
	for _, r := range rows {
		if r.CachedCount > 0 {
			continue
		}
		if withinWindow(r.LastAccessedAt, activeWindow) {
			continue
		}
		metaFile := filepath.Join(metaDir, r.PkgName+".json")
		if _, err := os.Stat(metaFile); err == nil {
			_ = os.Remove(metaFile)
			res.RemovedMetas++
		}
		if db.DeletePackage(r.PkgName) == nil {
			res.RemovedMetaOnly++
		}
		if r.VersionCount == 0 {
			res.RemovedPackages++
		}
	}
	// 5) 物化统计列校正：清理删过数据时全量重算，防止计数漂移导致包列表缺项
	if res.RemovedVersions > 0 || res.RemovedOrphanVer > 0 || res.RemovedPackages > 0 || res.RemovedMetaOnly > 0 {
		db.RecalcAllPackageStats()
	}
	return res
}

// withinWindow 判断 DB 时间串是否落在窗口内（无法解析视为不在窗口内）。
func withinWindow(s string, window time.Duration) bool {
	if s == "" {
		return false
	}
	for _, f := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return time.Since(t) < window
		}
	}
	return false
}

// cleanEmptyDirs 自底向上删除 packages 下的空目录。
func cleanEmptyDirs(root string, res *Result) {
	var dirs []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if os.Remove(dirs[i]) == nil {
			res.RemovedEmptyDirs++
		}
	}
}
