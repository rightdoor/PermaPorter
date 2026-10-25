package logring

import (
	"testing"
	"time"
)

func TestLogRingWraparound(t *testing.T) {
	r := NewLogRing(3)
	base := time.Now()
	for i := 0; i < 5; i++ {
		r.Add(LogEntry{Time: base.Add(time.Duration(i) * time.Second), Pkg: "p", Kind: "meta"})
	}
	got := r.Recent(10)
	if len(got) != 3 {
		t.Fatalf("应只保留最近 3 条，实际 %d", len(got))
	}
	// 新→旧：最新为 i=4
	if got[0].Time.Before(got[2].Time) {
		t.Fatal("Recent 应按新→旧排序")
	}
}

func TestLogRingEmpty(t *testing.T) {
	r := NewLogRing(5)
	if got := r.Recent(10); len(got) != 0 {
		t.Fatalf("空缓冲应返回 0 条，实际 %d", len(got))
	}
}

func TestLogRingLimit(t *testing.T) {
	r := NewLogRing(10)
	for i := 0; i < 4; i++ {
		r.Add(LogEntry{Time: time.Now(), Pkg: "p", Kind: "meta"})
	}
	if got := r.Recent(2); len(got) != 2 {
		t.Fatalf("限数应返回 2 条，实际 %d", len(got))
	}
}
