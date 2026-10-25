//go:build !windows

package utils

import "syscall"

// DiskFree 返回 path 所在磁盘的剩余空间与总空间（字节）（Linux/macOS）。
func DiskFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	free = uint64(st.Bavail) * uint64(st.Bsize)
	total = uint64(st.Blocks) * uint64(st.Bsize)
	return free, total, nil
}
