package webutil

import (
	"fmt"
	"testing"
	"time"
)

// TestViewCacheEvictsBeyondLimit 校验缓存条目有上限（公开搜索接口可被随机查询撑爆内存）。
func TestViewCacheEvictsBeyondLimit(t *testing.T) {
	var data, stats uint64
	vc := NewViewCache(func() uint64 { return data }, func() uint64 { return stats }, 16)
	for i := 0; i < 200; i++ {
		vc.Set(fmt.Sprintf("k%d", i), i, CacheData, time.Minute)
	}
	if n := vc.Len(); n > 16 {
		t.Fatalf("条目数 = %d，应不超过上限 16", n)
	}
}

// TestViewCacheClassInvalidation 校验两类代次互不干扰：
// 统计落库不应打掉包列表缓存，数据变更才应打掉。
func TestViewCacheClassInvalidation(t *testing.T) {
	var data, stats uint64
	vc := NewViewCache(func() uint64 { return data }, func() uint64 { return stats }, 0)

	vc.Set("pkglist", "list", CacheData, time.Minute)
	vc.Set("dash", "dash", CacheData|CacheStats, time.Minute)

	// 统计写入：列表缓存应保留，仪表盘缓存应失效
	stats++
	if _, ok := vc.Get("pkglist", CacheData); !ok {
		t.Fatal("统计代次变化不应使包列表缓存失效")
	}
	if _, ok := vc.Get("dash", CacheData|CacheStats); ok {
		t.Fatal("统计代次变化应使仪表盘缓存失效")
	}

	// 数据写入：两类都应失效
	vc.Set("dash", "dash", CacheData|CacheStats, time.Minute)
	data++
	if _, ok := vc.Get("pkglist", CacheData); ok {
		t.Fatal("数据代次变化应使包列表缓存失效")
	}
	if _, ok := vc.Get("dash", CacheData|CacheStats); ok {
		t.Fatal("数据代次变化应使仪表盘缓存失效")
	}
}

// TestViewCacheTTL 过期条目不可命中。
func TestViewCacheTTL(t *testing.T) {
	var gen uint64
	vc := NewViewCache(func() uint64 { return gen }, func() uint64 { return gen }, 0)
	vc.Set("k", 1, CacheData, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := vc.Get("k", CacheData); ok {
		t.Fatal("过期条目不应命中")
	}
}

func TestNormalizeCacheKey(t *testing.T) {
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'A'
	}
	lowered := make([]byte, viewCacheMaxQueryLen)
	for i := range lowered {
		lowered[i] = 'a'
	}
	cases := []struct{ in, want string }{
		{"  Vue  ", "vue"},
		{"React", "react"},
		{string(long), string(lowered)}, // 超长截断 + 转小写
	}
	for _, c := range cases {
		if got := NormalizeCacheKey(c.in); got != c.want {
			t.Errorf("NormalizeCacheKey(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeSort 排序白名单：合法键归一，非法键返回空串。
func TestNormalizeSort(t *testing.T) {
	if k, d := NormalizeSort("name", "asc"); k != "name" || d != "asc" {
		t.Fatalf("合法升序被改写: %s %s", k, d)
	}
	if k, d := NormalizeSort("size", "DESC"); k != "size" || d != "desc" {
		t.Fatalf("方向应归一为小写 desc: %s %s", k, d)
	}
	if k, d := NormalizeSort("; DROP TABLE", "asc"); k != "" || d != "" {
		t.Fatalf("非法键应返回空串: %s %s", k, d)
	}
}


// TestCleanupJobSubmitAndPayload 状态机：提交、防重入、完成消息与轮询载荷。
func TestCleanupJobSubmitAndPayload(t *testing.T) {
	var j CleanupJob
	if !j.Submit(func() string { return "清理完成：1 个" }) {
		t.Fatal("空闲时应允许提交")
	}
	// 等后台任务完成
	deadline := time.Now().Add(2 * time.Second)
	for j.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p := j.Payload()
	if p["running"] != false || p["message"] != "清理完成：1 个" {
		t.Fatalf("完成后载荷异常: %v", p)
	}
	if p["durationMs"] == nil || p["finishedAt"] == nil {
		t.Fatalf("完成载荷应含时间信息: %v", p)
	}
	// panic 被捕获并记入 errMsg
	if !j.Submit(func() string { panic("boom") }) {
		t.Fatal("完成后应允许再次提交")
	}
	deadline = time.Now().Add(2 * time.Second)
	for j.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p := j.Payload(); p["err"] == nil || p["err"] == "" {
		t.Fatalf("panic 应记入 err: %v", p)
	}
}
