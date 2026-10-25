package database

import (
	"log"
	"net/url"
	"strings"
)

// dsnPragmaOrder mattn 风格 DSN 参数 → modernc `_pragma` 的翻译表。
// 顺序即执行顺序：先设置连接/文件级参数，最后才开 query_only（它会禁止后续写操作）。
var dsnPragmaOrder = []struct{ key, pragma string }{
	{"_busy_timeout", "busy_timeout"},
	{"_journal_mode", "journal_mode"},
	{"_synchronous", "synchronous"},
	{"_foreign_keys", "foreign_keys"},
}

// normalizeDSN 把 mattn/go-sqlite3 风格的 DSN 参数翻译成 modernc.org/sqlite 的 `_pragma` 形式。
//
// 为什么需要它：modernc 早期版本只认 `_pragma`，mattn 风格参数会被**静默忽略**
// （曾导致 busy_timeout 失效、读池没有只读保护）。当前依赖的 v1.61 已原生支持两种写法，
// 但这里仍统一归一为 `_pragma`：行为不随驱动版本漂移，且能按连接池角色强制只读。
// readOnly 为 true 时追加 `query_only(1)`，从连接层面杜绝读池误写。
func normalizeDSN(dsn string, readOnly bool) string {
	base, rawQuery, _ := strings.Cut(dsn, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		// DSN 非法时保持原样，交给驱动报错
		return dsn
	}
	pragmas := append([]string(nil), q["_pragma"]...)
	for _, p := range dsnPragmaOrder {
		v := q.Get(p.key)
		if v == "" {
			continue
		}
		q.Del(p.key)
		if sv, ok := sanitizePragmaValue(v); ok {
			pragmas = append(pragmas, p.pragma+"("+sv+")")
		} else {
			log.Printf("[database] DSN 参数 %s=%q 非法，已忽略", p.key, v)
		}
	}
	// 是否只读由连接池角色决定，不沿用 DSN 里的 _query_only
	q.Del("_query_only")
	if readOnly {
		pragmas = append(pragmas, "query_only(1)")
	}
	q.Del("_pragma")
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// sanitizePragmaValue 只允许字母数字下划线，避免 DSN 里拼出额外 SQL。
func sanitizePragmaValue(v string) (string, bool) {
	if v == "" || len(v) > 32 {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		default:
			return "", false
		}
	}
	return v, true
}
