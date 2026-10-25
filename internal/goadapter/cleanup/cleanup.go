// Package cleanup 提供存储目录与数据库的安全清理。
//
// GOPROXY 语义下清理只做以下安全动作：
//  1. DB 中标记为已缓存但文件缺失的 zip：复位 zip 状态（保留版本记录，可能仍在 @v/list 中）；
//  2. storage/modules 下无 DB 记录且修改时间超过 30 分钟的孤立文件：删除；
//  3. storage/tmp 下超过 1 小时的临时文件：删除；
//  4. 自底向上删除空目录（只删空目录，不递归删除有内容的父目录）；
//  5. 清理后重算模块物化统计。
//
// **绝不删除**：@v/list、latest.json、任何被 DB 引用的有效产物、
// 仅元数据模块（只拉过 @v/list/@latest 的模块是合法缓存）。
//
// 注意：模块路径可以嵌套（example.com/a 与 example.com/a/b 共享父目录），
// 因此任何删除都只针对文件或 @v 目录/空目录，禁止 RemoveAll(moduleDir)。
package cleanup

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"permaporter/internal/goadapter/database"
	"permaporter/internal/goadapter/proxy"
)

const (
	// activeWindow 孤立文件保护窗口：窗口内修改的文件可能是下载进行中的产物，跳过。
	activeWindow = 30 * time.Minute
	// tmpMaxAge 临时文件保留时长。
	tmpMaxAge = time.Hour
)

// Result 一次清理的统计结果。
type Result struct {
	ResetZipRecords  int // 复位的"有记录无文件" zip 状态
	RemovedFiles     int // 删除的孤立产物文件（含孤立的 list/latest.json/info/mod/zip/ziphash）
	RemovedTmpFiles  int // 删除的过期临时文件
	RemovedEmptyDirs int // 删除的空目录
	RecalcModules    int // 重算统计的模块数
}

// Run 执行一次完整清理并返回统计。
func Run(db *database.DB, storageDir string) Result {
	var res Result

	// 0) 无模块关联的孤立版本记录（外键检查曾关闭时可能残留）→ 直接删除
	if n, err := db.DeleteOrphanVersions(); err == nil && n > 0 {
		log.Printf("[cleanup] 删除孤立版本记录 %d 条", n)
	} else if err != nil {
		log.Printf("[cleanup] 删除孤立版本失败: %v", err)
	}

	// 1) 有 zip 记录但物理文件缺失 → 复位 zip 状态（保留版本记录）
	validFiles := map[string]bool{}
	artifacts, err := db.AllCachedArtifacts()
	if err != nil {
		log.Printf("[cleanup] 读取已缓存产物失败: %v", err)
	}
	resetModuleIDs := map[int64]bool{}
	for _, a := range artifacts {
		if a.ZipSizeBytes <= 0 {
			continue
		}
		zipFull := filepath.Join(storageDir, filepath.FromSlash(a.ZipPath))
		if fi, err := os.Stat(zipFull); err == nil && !fi.IsDir() && fi.Size() > 0 {
			validFiles[filepath.ToSlash(a.ZipPath)] = true
			continue
		}
		if err := db.ClearVersionZip(a.VersionID); err == nil {
			res.ResetZipRecords++
			resetModuleIDs[a.ModuleID] = true
		}
	}

	// 2) 登记全部 DB 认可的磁盘文件。凡匹配已知 (模块, 版本) 对的产物文件一律视为有效
	//    （产物不可变，宁可保留）：这覆盖了"磁盘有文件但 DB 记录缺失"的自愈窗口
	//    （如 DB 重建后 zip 尚未被首次命中重新登记），避免把真实产物当孤立文件误删。
	mods, err := db.AllModulesForCleanup()
	if err != nil {
		log.Printf("[cleanup] 读取模块列表失败: %v", err)
		mods = nil
	}
	for _, m := range mods {
		escDir := slashJoin("modules", proxy.EscapePath(m.ModulePath), "@v")
		if m.MetaCachedAt != "" {
			validFiles[slashJoin(escDir, "list")] = true
		}
		if m.LatestCachedAt != "" {
			validFiles[slashJoin(escDir, "latest.json")] = true
		}
	}
	pairs, err := db.AllModuleVersions()
	if err != nil {
		log.Printf("[cleanup] 读取模块版本对失败: %v", err)
		pairs = nil
	}
	for _, p := range pairs {
		escDir := slashJoin("modules", proxy.EscapePath(p.ModulePath), "@v")
		escVer := proxy.EscapeVersion(p.Version)
		for _, ext := range []string{".info", ".mod", ".zip", ".ziphash"} {
			validFiles[slashJoin(escDir, escVer+ext)] = true
		}
	}
	// 步骤 1 中复位过统计的模块重算
	for id := range resetModuleIDs {
		db.RecalcModuleZipStats(id)
		res.RecalcModules++
	}

	// 3) 孤立产物文件：无 DB 记录且修改时间超过保护窗口 → 删除
	modulesRoot := filepath.Join(storageDir, "modules")
	if _, err := os.Stat(modulesRoot); err == nil {
		_ = filepath.Walk(modulesRoot, func(path string, info os.FileInfo, err error) error {
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
			if validFiles[filepath.ToSlash(rel)] {
				return nil
			}
			if os.Remove(path) == nil {
				res.RemovedFiles++
			}
			return nil
		})
	}

	// 4) 过期临时文件
	tmpDir := filepath.Join(storageDir, "tmp")
	if entries, err := os.ReadDir(tmpDir); err == nil {
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			fi, err := ent.Info()
			if err != nil || time.Since(fi.ModTime()) < tmpMaxAge {
				continue
			}
			if os.Remove(filepath.Join(tmpDir, ent.Name())) == nil {
				res.RemovedTmpFiles++
			}
		}
	}

	// 5) 自底向上删除空目录
	res.RemovedEmptyDirs = cleanEmptyDirs(modulesRoot)
	res.RemovedEmptyDirs += cleanEmptyDirs(tmpDir)

	// 6) 物化统计列校正：复位过记录时全量重算，防止计数漂移
	if res.ResetZipRecords > 0 {
		if err := db.BackfillModuleStats(); err == nil {
			res.RecalcModules++
		}
	}
	return res
}

// cleanEmptyDirs 自底向上删除 root 下的空目录，返回删除数量。
// 只删除空目录本身：模块目录可能被嵌套子模块共享，绝不递归删除非空目录。
func cleanEmptyDirs(root string) int {
	if root == "" {
		return 0
	}
	var dirs []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	removed := 0
	for i := len(dirs) - 1; i >= 0; i-- {
		if os.Remove(dirs[i]) == nil {
			removed++
		}
	}
	return removed
}

// PruneEmptyDirs 从 dir 向上删除空目录，直到 storageDir（不含）。
// 供管理员删除整个模块后剪枝其父目录（父目录可能被嵌套子模块共享，非空即停止）。
func PruneEmptyDirs(storageDir, dir string) {
	root := filepath.Clean(storageDir)
	for {
		clean := filepath.Clean(dir)
		if clean == root || !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return
		}
		// 目录非空（含嵌套子模块等）即停止
		if err := os.Remove(clean); err != nil {
			return
		}
		dir = filepath.Dir(clean)
	}
}

// slashJoin 以 / 拼接相对路径（DB 中的 zip_path 统一使用 / 分隔）。
func slashJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
