// Package middleware 提供会话管理与登录限流。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// CookieName 会话 Cookie 名称。
const CookieName = "permaporter_session"

const sessionTTL = 24 * time.Hour

// SessionStore 内存级会话存储（重启后需重新登录）。
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> 过期时间
}

// NewSessionStore 创建会话存储。
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]time.Time)}
}

// Create 创建新会话，返回 token。
func (s *SessionStore) Create() string {
	b := make([]byte, 32)
	// crypto/rand.Read 自 Go 1.24 起文档保证永不返回错误；
	// 万一失败宁可 panic 也不签发全零可预测 token
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand 读取失败: " + err.Error())
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return token
}

// Valid 校验 token 是否有效，有效则自动续期（服务端滑动 24h）。
func (s *SessionStore) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	s.sessions[token] = time.Now().Add(sessionTTL)
	return true
}

// Remaining 返回会话剩余有效期；无效会话返回 0。
// 供 HTTP 层判断是否需要重发 Cookie（服务端续期后同步客户端有效期）。
func (s *SessionStore) Remaining(token string) time.Duration {
	if token == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return 0
	}
	if d := time.Until(exp); d > 0 {
		return d
	}
	return 0
}

// Destroy 销毁会话。
func (s *SessionStore) Destroy(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// Cleanup 清理已过期会话，返回清理条数（由后台定时任务调用，
// 避免从未被再次访问的过期 token 永久占用内存）。
func (s *SessionStore) Cleanup() int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for token, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, token)
			n++
		}
	}
	return n
}
