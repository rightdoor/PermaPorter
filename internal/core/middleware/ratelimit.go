package middleware

import (
	"sync"
	"time"
)

// 登录暴力破解防护参数。
const (
	MaxLoginFails = 5                // 最大失败次数
	BanDuration   = 15 * time.Minute // IP 封禁时长
)

type loginEntry struct {
	fails       int
	bannedUntil time.Time
	lastFail    time.Time
}

// LoginLimiter 登录失败 IP 级封禁（内存缓存）。
type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

// NewLoginLimiter 创建限流器。
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{entries: make(map[string]*loginEntry)}
}

// Allow 判断 IP 当前是否允许尝试登录。
func (l *LoginLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		return true
	}
	return !time.Now().Before(e.bannedUntil)
}

// RecordFailure 记录一次登录失败，返回距封禁还剩的可尝试次数（0 表示已封禁）。
func (l *LoginLimiter) RecordFailure(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		e = &loginEntry{}
		l.entries[ip] = e
	}
	// 距上次失败超过封禁时长视为新一轮
	if !e.lastFail.IsZero() && time.Since(e.lastFail) > BanDuration {
		e.fails = 0
	}
	e.fails++
	e.lastFail = time.Now()
	if e.fails >= MaxLoginFails {
		e.bannedUntil = time.Now().Add(BanDuration)
		e.fails = 0
		return 0
	}
	return MaxLoginFails - e.fails
}

// Reset 登录成功后清除记录。
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}

// Cleanup 清理已解封且超过封禁时长没有新失败的条目，返回清理条数。
// 否则历史 IP 会永久留在 map 里（SetTrustedProxies 之后 IP 不可伪造，但仍会自然累积）。
func (l *LoginLimiter) Cleanup() int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for ip, e := range l.entries {
		if now.Before(e.bannedUntil) {
			continue
		}
		if !e.lastFail.IsZero() && now.Sub(e.lastFail) <= BanDuration {
			continue
		}
		delete(l.entries, ip)
		n++
	}
	return n
}
