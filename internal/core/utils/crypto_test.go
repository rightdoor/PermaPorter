package utils

import (
	"errors"
	"strings"
	"testing"
)

// TestHashPassword 超过 bcrypt 72 字节上限必须报错而非截断/返回空哈希；
// 正常输入可生成可校验的哈希。
func TestHashPassword(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("超长密码应返回 ErrPasswordTooLong，得到 %v", err)
	}
	hash, err := HashPassword("secret-1")
	if err != nil || hash == "" {
		t.Fatalf("正常密码应成功: %q %v", hash, err)
	}
	if !CheckPassword(hash, "secret-1") || CheckPassword(hash, "secret-2") {
		t.Fatal("哈希校验不正确")
	}
	// 边界值：恰好 72 字节可接受
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Fatalf("72 字节密码应成功: %v", err)
	}
}
