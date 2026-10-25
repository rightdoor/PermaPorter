//go:build windows

package utils

import "golang.org/x/sys/windows"

// DiskFree 返回 path 所在磁盘的剩余空间与总空间（字节）。
func DiskFree(path string) (free, total uint64, err error) {
	var freeBytes, totalBytes, availBytes uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &availBytes, &totalBytes, &freeBytes); err != nil {
		return 0, 0, err
	}
	return freeBytes, totalBytes, nil
}
