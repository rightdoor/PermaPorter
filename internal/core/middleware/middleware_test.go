package middleware

import (
	"testing"
	"time"
)

// TestSessionStoreLifecycle 会话创建/校验/续期/销毁。
func TestSessionStoreLifecycle(t *testing.T) {
	s := NewSessionStore()
	token := s.Create()
	if token == "" {
		t.Fatal("Create 应返回非空 token")
	}
	if !s.Valid(token) {
		t.Fatal("新会话应有效")
	}
	if s.Valid("") || s.Valid("not-exist") {
		t.Fatal("空与未知 token 应无效")
	}
	s.Destroy(token)
	if s.Valid(token) {
		t.Fatal("销毁后的会话应无效")
	}
}

// TestSessionStoreCleanup 清理已过期且未被再次访问的会话（Janitor 调用路径）。
func TestSessionStoreCleanup(t *testing.T) {
	s := NewSessionStore()
	live := s.Create()
	expired := s.Create()
	s.mu.Lock()
	s.sessions[expired] = time.Now().Add(-time.Hour) // 直接改成过期
	s.mu.Unlock()

	if n := s.Cleanup(); n != 1 {
		t.Fatalf("应清理 1 个过期会话，实际 %d", n)
	}
	if s.Valid(expired) {
		t.Fatal("过期会话应已被清理")
	}
	if !s.Valid(live) {
		t.Fatal("未过期会话不应被清理")
	}
}

// TestLoginLimiterRecordAndBan 失败计数、封禁与解封后的允许。
func TestLoginLimiterRecordAndBan(t *testing.T) {
	l := NewLoginLimiter()
	ip := "10.0.0.9"
	for i := 1; i < MaxLoginFails; i++ {
		if left := l.RecordFailure(ip); left != MaxLoginFails-i {
			t.Fatalf("第 %d 次失败后应剩 %d 次，实际 %d", i, MaxLoginFails-i, left)
		}
		if !l.Allow(ip) {
			t.Fatalf("未达封禁阈值前应允许（第 %d 次）", i)
		}
	}
	if left := l.RecordFailure(ip); left != 0 {
		t.Fatalf("达到阈值应返回 0（已封禁），实际 %d", left)
	}
	if l.Allow(ip) {
		t.Fatal("封禁期内应拒绝登录")
	}
	// 解封后允许再次尝试
	l.mu.Lock()
	l.entries[ip].bannedUntil = time.Now().Add(-time.Minute)
	l.mu.Unlock()
	if !l.Allow(ip) {
		t.Fatal("解封后应允许登录")
	}
	// 登录成功清除记录
	l.Reset(ip)
	l.mu.Lock()
	_, ok := l.entries[ip]
	l.mu.Unlock()
	if ok {
		t.Fatal("登录成功后应清除限流记录")
	}
}

// TestLoginLimiterCleanup 清理已解封且超过封禁时长没有新失败的条目（Janitor 调用路径）。
func TestLoginLimiterCleanup(t *testing.T) {
	l := NewLoginLimiter()
	l.RecordFailure("10.0.0.1") // 一次失败，未封禁
	l.RecordFailure("10.0.0.2")

	// 近期失败的条目应保留（可能处于新一轮计数中）
	if n := l.Cleanup(); n != 0 {
		t.Fatalf("近期失败条目不应清理，实际清理 %d", n)
	}

	// 改成超过封禁时长没有新失败 → 应清理
	old := time.Now().Add(-2 * BanDuration)
	l.mu.Lock()
	for _, e := range l.entries {
		e.lastFail = old
	}
	l.mu.Unlock()
	if n := l.Cleanup(); n != 2 {
		t.Fatalf("应清理 2 个过期条目，实际 %d", n)
	}
	l.mu.Lock()
	remaining := len(l.entries)
	l.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("清理后不应残留条目，实际 %d", remaining)
	}
}
