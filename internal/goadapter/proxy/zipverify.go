package proxy

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/zip"
)

// zipMagic 标准 zip 文件头（PK\x03\x04）。
var zipMagic = [4]byte{'P', 'K', 0x03, 0x04}

// VerifyZipFile 校验模块 zip：
//  1. 魔数 PK\x03\x04（快速拒绝 HTML 错误页等异常响应）；
//  2. zip.CheckZip 按模块 zip 规范校验结构、大小与文件路径限制
//     （文件路径必须位于 <module>@<version>/ 下，单文件与总大小受限）。
func VerifyZipFile(modulePath, version, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		f.Close()
		return fmt.Errorf("读取文件头失败: %w", err)
	}
	f.Close()
	if magic != zipMagic {
		return fmt.Errorf("文件不是有效的 zip（魔数不符）")
	}
	if _, err := zip.CheckZip(module.Version{Path: modulePath, Version: version}, path); err != nil {
		return fmt.Errorf("模块 zip 结构校验失败: %w", err)
	}
	return nil
}

// HashZipFile 计算模块 zip 的 h1: 校验和（dirhash.Hash1，与 go.sum 口径一致）。
func HashZipFile(path string) (string, error) {
	return dirhash.HashZip(path, dirhash.Hash1)
}
