// Package idle 提供进程级空闲检测与闲时内存回收。
//
// 所有协议端口与控制台的请求经 Activity 中间件触碰 Tracker；当所有端口连续
// 无请求达到阈值后，后台任务主动归还空闲堆内存给操作系统（debug.FreeOSMemory，
// 内部含一次 GC），降低长期运行的常驻内存（RSS）。回收采用"活动边沿"
// 触发：同一空闲周期只回收一次，直到下一次请求把计时器唤醒才会重新计时。
//
// 本包不依赖任何 adapter，也不触碰任何生态缓存内容——只做堆内存归还。
package idle

import (
	"log"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Tracker 记录最近一次活动时间与回收状态，并发安全。
//
// 不使用"布尔标记 + CAS"而是比较活动时间戳：lastActivity 只由 Touch 推进，
// lastReclaim 记录上次回收时所见的 lastActivity 值。两者相等即表示"这一轮
// 空闲已经回收过"，无需担心判断与执行之间恰好来请求的竞态——即便发生，
// 最坏只是一次多余的无害 GC，且下一轮仍会针对新活动点正确触发。
type Tracker struct {
	lastActivity atomic.Int64 // 最近一次请求时间（UnixNano）
	lastReclaim  atomic.Int64 // 上次回收时所见的 lastActivity 值；0 表示从未回收
	reclaims     atomic.Int64 // 累计回收次数（观测与测试用）
}

// NewTracker 创建追踪器，以当前时刻作为初始活动时间。
func NewTracker() *Tracker {
	t := &Tracker{}
	t.lastActivity.Store(time.Now().UnixNano())
	return t
}

// Touch 记录一次活动（任一端口的请求）。重置空闲计时与"已回收"状态。
func (t *Tracker) Touch() { t.lastActivity.Store(time.Now().UnixNano()) }

// IdleFor 返回当前空闲时长（距最近一次活动）。
func (t *Tracker) IdleFor() time.Duration {
	return time.Duration(time.Now().UnixNano() - t.lastActivity.Load())
}

// ReclaimCount 返回累计回收次数。
func (t *Tracker) ReclaimCount() int64 { return t.reclaims.Load() }

// Run 阻塞运行空闲检测循环，直到 stop 关闭。threshold <= 0 时直接返回（禁用）。
// 检测间隔随阈值自适应并夹在 [200ms, 10s]：既不会在大阈值下频繁空转，
// 也不会在小阈值（测试/自定义）下迟钝。
func (t *Tracker) Run(threshold time.Duration, stop <-chan struct{}) {
	if threshold <= 0 {
		return
	}
	interval := threshold / 10
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	t.run(threshold, interval, stop)
}

func (t *Tracker) run(threshold, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.reclaimIfIdle(threshold)
		}
	}
}

// reclaimIfIdle 在空闲达到阈值且本轮尚未回收时执行一次内存归还，返回是否回收。
// debug.FreeOSMemory 内部会先触发一次 GC，再把空闲 span 归还操作系统，降低 RSS，
// 因此单次调用即可完成"GC + 归还"，无需再显式调 runtime.GC（避免空闲时两次全量 GC）。
func (t *Tracker) reclaimIfIdle(threshold time.Duration) bool {
	now := time.Now().UnixNano()
	last := t.lastActivity.Load()
	if now-last < int64(threshold) {
		return false
	}
	if t.lastReclaim.Load() == last {
		return false // 这一轮空闲已回收过，等下一次 Touch 唤醒
	}
	debug.FreeOSMemory()
	t.lastReclaim.Store(last)
	t.reclaims.Add(1)
	log.Printf("[idle] 空闲 %s 达到阈值 %s，已归还空闲内存（累计第 %d 次）",
		time.Duration(now-last), threshold, t.reclaims.Load())
	return true
}
