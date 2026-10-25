// 校验和端点（<资源>.sha1/.md5/.sha256/.sha512）的计算辅助：
// 摘要一律由本地缓存内容即时计算，与实际服务内容永远一致，不落盘、不回源。
package proxy

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

// newHash 按算法创建摘要器。
func newHash(algo ChecksumAlgo) (hash.Hash, error) {
	switch algo {
	case "sha1":
		return sha1.New(), nil
	case "md5":
		return md5.New(), nil
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	}
	return nil, fmt.Errorf("不支持的校验算法: %s", algo)
}

// digestString 计算内存内容的摘要（hex 小写）。
func digestString(algo ChecksumAlgo, data []byte) (string, error) {
	h, err := newHash(algo)
	if err != nil {
		return "", err
	}
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// digestFile 流式计算磁盘文件内容的摘要（hex 小写）。
func digestFile(algo ChecksumAlgo, path string) (string, error) {
	h, err := newHash(algo)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
