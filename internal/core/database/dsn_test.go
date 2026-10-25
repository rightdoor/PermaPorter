package database

import (
	"strings"
	"testing"
)

// TestNormalizeDSNTranslatesMattnParams 校验 mattn 风格 DSN 参数被翻译成 modernc 的 _pragma。
// 纯 Go 驱动只认 _pragma，未翻译的参数会被静默忽略（busy_timeout 失效会直接 SQLITE_BUSY）。
func TestNormalizeDSNTranslatesMattnParams(t *testing.T) {
	dsn := "file:./data/x.db?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"

	write := normalizeDSN(dsn, false)
	for _, want := range []string{
		"_pragma=busy_timeout%2810000%29",
		"_pragma=journal_mode%28DELETE%29",
		"_pragma=foreign_keys%281%29",
	} {
		if !strings.Contains(write, want) {
			t.Errorf("写池 DSN 缺少 %s：%s", want, write)
		}
	}
	if strings.Contains(write, "_busy_timeout=") || strings.Contains(write, "_journal_mode=") {
		t.Errorf("mattn 风格参数应被移除：%s", write)
	}
	if strings.Contains(write, "query_only") {
		t.Errorf("写池不应带 query_only：%s", write)
	}

	read := normalizeDSN(dsn, true)
	if !strings.Contains(read, "_pragma=query_only%281%29") {
		t.Errorf("读池 DSN 应带 query_only：%s", read)
	}
}

// TestNormalizeDSNKeepsNativePragma 已经使用 _pragma 的 DSN 不应被重复翻译。
func TestNormalizeDSNKeepsNativePragma(t *testing.T) {
	dsn := "file:x.db?_pragma=busy_timeout(5000)&_txlock=immediate"
	got := normalizeDSN(dsn, false)
	if strings.Count(got, "busy_timeout") != 1 {
		t.Errorf("busy_timeout 应只出现一次：%s", got)
	}
	if !strings.Contains(got, "_txlock=immediate") {
		t.Errorf("未知参数应原样保留：%s", got)
	}
}

// TestNormalizeDSNDropsInvalidValue 防止 DSN 里拼出额外 SQL。
func TestNormalizeDSNDropsInvalidValue(t *testing.T) {
	// 值为 "10000);DROP TABLE packages--"，含括号/分号/空格，必须被拒绝
	dsn := "file:x.db?_busy_timeout=10000%29%3BDROP%20TABLE%20packages--"
	got := normalizeDSN(dsn, false)
	if strings.Contains(got, "DROP") {
		t.Fatalf("非法值应被丢弃：%s", got)
	}
	if strings.Contains(got, "busy_timeout") {
		t.Errorf("非法值不应生成 pragma：%s", got)
	}
}

// TestNormalizeDSNParseErrorKeepsOriginal 无法解析的查询串原样返回（驱动只读 _pragma，不会执行其余参数）。
func TestNormalizeDSNParseErrorKeepsOriginal(t *testing.T) {
	dsn := "file:x.db?_busy_timeout=10000);DROP+TABLE+packages--"
	if got := normalizeDSN(dsn, false); got != dsn {
		t.Fatalf("解析失败时应原样返回：%s", got)
	}
}

// TestNormalizeDSNIgnoresQueryOnlyOnWritePool DSN 里的 _query_only 不能把写池变成只读。
func TestNormalizeDSNIgnoresQueryOnlyOnWritePool(t *testing.T) {
	got := normalizeDSN("file:x.db?_query_only=1", false)
	if strings.Contains(got, "query_only") {
		t.Fatalf("写池不应继承 _query_only：%s", got)
	}
}

// TestNormalizeDSNWithoutQuery 无查询串时：写池原样返回，读池仍要补 query_only。
func TestNormalizeDSNWithoutQuery(t *testing.T) {
	if got := normalizeDSN("file:x.db", false); got != "file:x.db" {
		t.Fatalf("写池无查询串时不应改动：%s", got)
	}
	if got := normalizeDSN("file:x.db", true); !strings.Contains(got, "query_only") {
		t.Fatalf("读池应补 query_only：%s", got)
	}
}

func TestSanitizePragmaValue(t *testing.T) {
	ok := []string{"10000", "DELETE", "FULL", "ON", "1"}
	bad := []string{"", "1);DROP", "1 2", "a-b", strings.Repeat("a", 33)}
	for _, v := range ok {
		if _, good := sanitizePragmaValue(v); !good {
			t.Errorf("%q 应合法", v)
		}
	}
	for _, v := range bad {
		if _, good := sanitizePragmaValue(v); good {
			t.Errorf("%q 应被拒绝", v)
		}
	}
}
