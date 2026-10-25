// 磁盘路径安全辅助：npm 的版本键来自上游 packument 的 versions 对象 key，
// 会直接拼入 tarball 磁盘路径（packages/<pkg>/<ver>.tgz），入库前必须白名单校验，
// 落盘/删除前必须越界断言，防止路径穿越。
package proxy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// versionKeyRe 版本键白名单：字母或数字开头，仅允许 npm 版本号会用到的字符集。
// `.`、`..`、路径分隔符与控制字符均不在允许范围内。
var versionKeyRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$`)

// ValidVersionKey 校验 packument 版本键是否可安全入库与拼磁盘路径。
func ValidVersionKey(ver string) bool {
	return versionKeyRe.MatchString(ver)
}

// StorageFilePath 在存储目录下安全拼接路径：兜底确认最终路径仍在存储目录内，
// 防止 DB 或上游数据中的相对路径（如历史库中的越界版本键）组合出存储树外的
// 绝对路径。所有落盘/删除入口必须经过本函数。
func StorageFilePath(root string, parts ...string) (string, error) {
	p := filepath.Join(append([]string{root}, parts...)...)
	cleanRoot := filepath.Clean(root)
	if p != cleanRoot && !strings.HasPrefix(p, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越界: %s", p)
	}
	return p, nil
}
