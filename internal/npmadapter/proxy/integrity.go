package proxy

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"hash"
	"io"
	"os"
	"strings"
)

// 上游 tarball 内容校验：npm 的 dist.integrity 是 SRI 字符串
// （如 "sha512-BASE64"），下载完成后比对摘要，避免把截断/错误页面永久缓存。

// parseIntegrity 解析 SRI 字符串，返回算法名与期望摘要。
// 支持 sha512/sha256/sha1（npm 官方源通常是 sha512）；无法解析时 ok=false，跳过校验。
func parseIntegrity(s string) (algo string, want []byte, ok bool) {
	for _, tok := range strings.Fields(s) {
		i := strings.IndexByte(tok, '-')
		if i <= 0 {
			continue
		}
		a := strings.ToLower(tok[:i])
		size := integrityHashSize(a)
		if size == 0 {
			continue
		}
		digest, err := base64.StdEncoding.DecodeString(tok[i+1:])
		if err != nil || len(digest) != size {
			continue
		}
		return a, digest, true
	}
	return "", nil, false
}

// newIntegrityHash 按算法名创建哈希器；未知算法返回 nil。
func newIntegrityHash(algo string) hash.Hash {
	switch algo {
	case "sha512":
		return sha512.New()
	case "sha256":
		return sha256.New()
	case "sha1":
		return sha1.New()
	}
	return nil
}

func integrityHashSize(algo string) int {
	switch algo {
	case "sha512":
		return sha512.Size
	case "sha256":
		return sha256.Size
	case "sha1":
		return sha1.Size
	}
	return 0
}

// isGzipFile 检查文件头是否为 gzip 魔数：tarball 一定是 gzip，
// 上游返回 200 + HTML 错误页时能在此拦下。
func isGzipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic[0] == 0x1f && magic[1] == 0x8b
}
