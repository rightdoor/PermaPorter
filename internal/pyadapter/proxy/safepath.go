// 磁盘路径安全辅助：py 的发行文件记录（py_files.file_path / filename 列）来自
// 上游锚点页解析入库，读取/落盘/删除时会直接拼入磁盘路径。入库前已有文件名
// 白名单校验，但历史库可能存在被污染记录：所有用 DB 记录拼磁盘路径的入口
// 必须再经 StorageFilePath 越界断言，防止路径穿越（对齐 npm 侧防线）。
package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StorageFilePath 在存储目录下安全拼接路径：兜底确认最终路径仍在存储目录内，
// 防止 DB 中的相对路径（如历史库中的越界记录）组合出存储树外的绝对路径。
func StorageFilePath(root string, parts ...string) (string, error) {
	p := filepath.Join(append([]string{root}, parts...)...)
	cleanRoot := filepath.Clean(root)
	if p != cleanRoot && !strings.HasPrefix(p, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界: %s", p)
	}
	return p, nil
}
