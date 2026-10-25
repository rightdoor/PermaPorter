package proxy

import (
	"path/filepath"
	"testing"
)

// TestStorageFilePath 越界断言：py_files 表中的相对路径（含被污染的历史记录）
// 不得组合出存储目录之外的绝对路径（对齐 npm 侧同名防线）。
func TestStorageFilePath(t *testing.T) {
	root := "/data/storage-py"
	want := filepath.Join(root, "packages", "torch-1.0-py3.whl")
	if got, err := StorageFilePath(root, filepath.FromSlash("packages/torch-1.0-py3.whl")); err != nil || got != want {
		t.Errorf("StorageFilePath(packages/...) = (%q,%v)，期望 (%q,nil)", got, err, want)
	}
	if got, err := StorageFilePath(root, "packages", "torch-1.0-py3.whl"); err != nil || got != want {
		t.Errorf("StorageFilePath(packages, ...) = (%q,%v)，期望 (%q,nil)", got, err, want)
	}
	bad := []string{
		filepath.FromSlash("../../etc/passwd"),
		filepath.FromSlash("packages/../../../etc/passwd"),
	}
	for _, rel := range bad {
		if got, err := StorageFilePath(root, rel); err == nil {
			t.Errorf("StorageFilePath(%q) = %q，期望越界报错", rel, got)
		}
	}
}
