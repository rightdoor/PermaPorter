package utils

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MaxPasswordBytes bcrypt 明文长度上限：bcrypt 只处理前 72 字节，超长输入
// 直接判错而非静默截断（否则同一前缀的不同密码会生成相同哈希）。
const MaxPasswordBytes = 72

// ErrPasswordTooLong 密码超过 bcrypt 明文长度上限。
var ErrPasswordTooLong = errors.New("密码长度超过 72 字节上限")

// HashPassword 使用 Bcrypt 加密密码。任何失败都返回错误，绝不返回空哈希——
// 空哈希落库会让 IsInitialized 判定失效，导致初始化接口重新开放。
func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(h), nil
}

// CheckPassword 校验密码与哈希是否匹配。
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
