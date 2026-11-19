package idle

import (
	"testing"
	"time"
)

func sleepAtLeast(d time.Duration) { time.Sleep(d) }

// TestReclaimFiresOnEdge 验证：空闲达阈值触发一次；同一空闲周期不重复；
// Touch 唤醒后进入下一轮，再次空闲才再次触发。
func TestReclaimFiresOnEdge(t *testing.T) {
	const threshold = 120 * time.Millisecond

	tr := NewTracker()
	// 把最近活动时间回拨到阈值之前，模拟已经空闲很久
	tr.lastActivity.Store(time.Now().Add(-time.Second).UnixNano())

	if !tr.reclaimIfIdle(threshold) {
		t.Fatal("空闲超过阈值应触发回收")
	}
	if got := tr.ReclaimCount(); got != 1 {
		t.Fatalf("回收次数应为 1，实际 %d", got)
	}
	if tr.reclaimIfIdle(threshold) {
		t.Fatal("同一空闲周期不应重复回收")
	}
	if got := tr.ReclaimCount(); got != 1 {
		t.Fatalf("重复回收后次数不应变化，实际 %d", got)
	}

	// 新请求唤醒：重置计时，此时不算空闲
	tr.Touch()
	if tr.reclaimIfIdle(threshold) {
		t.Fatal("刚有请求，不应立即回收")
	}
	// 再次空闲
	tr.lastActivity.Store(time.Now().Add(-time.Second).UnixNano())
	if !tr.reclaimIfIdle(threshold) {
		t.Fatal("新一轮空闲应再次触发回收")
	}
	if got := tr.ReclaimCount(); got != 2 {
		t.Fatalf("回收次数应为 2，实际 %d", got)
	}
}

// TestRunStopsOnClose 后台循环在 stop 关闭后退出，且空闲达阈值时会回收。
func TestRunStopsOnClose(t *testing.T) {
	tr := NewTracker()
	tr.lastActivity.Store(time.Now().Add(-time.Second).UnixNano())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		tr.run(50*time.Millisecond, 20*time.Millisecond, stop)
		close(done)
	}()

	// 等待至少触发一次
	deadline := time.Now().Add(time.Second)
	for tr.ReclaimCount() == 0 && time.Now().Before(deadline) {
		sleepAtLeast(10 * time.Millisecond)
	}
	if tr.ReclaimCount() == 0 {
		t.Fatal("后台循环应在空闲达阈值后回收")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop 关闭后后台循环应退出")
	}
}

// TestRunDisabled 阈值 <=0 时 Run 立即返回、不回收。
func TestRunDisabled(t *testing.T) {
	tr := NewTracker()
	tr.lastActivity.Store(time.Now().Add(-time.Hour).UnixNano())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		tr.Run(0, stop)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("阈值 0 时 Run 应立即返回")
	}
	if tr.ReclaimCount() != 0 {
		t.Fatalf("禁用时不应回收，实际 %d 次", tr.ReclaimCount())
	}
}
