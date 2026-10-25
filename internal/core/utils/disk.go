package utils

import "errors"

// 磁盘剩余空间检查：下载产物前的余量预检（四生态共用）。

// ErrDiskFull 磁盘剩余空间不足（下载前预检失败）。
var ErrDiskFull = errors.New("磁盘剩余空间不足")

// diskReserveBytes 下载前要求磁盘剩余空间至少为 expected + 该余量。
const diskReserveBytes = 256 << 20

// DiskHasRoom 检查目录所在磁盘剩余空间是否满足 expected + 256MB 余量。
// 无法读取磁盘信息时放行（后续写入失败自然报错），不阻塞下载。
func DiskHasRoom(dir string, expected int64) error {
	free, _, err := DiskFree(dir)
	if err != nil {
		return nil
	}
	if expected < 0 {
		expected = 0
	}
	if free < uint64(expected)+diskReserveBytes {
		return ErrDiskFull
	}
	return nil
}
