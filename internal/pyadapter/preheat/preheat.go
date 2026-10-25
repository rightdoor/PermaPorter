// Package preheat 提供依赖清单预热：后台批量拉取项目锚点页与指定版本的发行文件，
// 把"首次安装赌网络"变成"先预热、后离线装"。
package preheat

import (
	"sync"
	"sync/atomic"
	"time"

	"permaporter/internal/pyadapter/proxy"
)

// 预热项状态。
const (
	StatePending = "pending" // 排队中
	StateRunning = "running" // 拉取中
	StateDone    = "done"    // 本次新拉取
	StateCached  = "cached"  // 原本已就绪
	StateFail    = "fail"    // 失败
)

// workerCount 单个预热任务的并发 worker 数（受引擎上游并发闸 16 全局限流）。
const workerCount = 4

// Item 单个项目的预热状态。
type Item struct {
	Name    string
	State   string
	Version string
	Size    int64
	Err     string
}

// Job 一次预热任务。
type Job struct {
	ID        int64
	WithFiles bool
	CreatedAt time.Time

	mu       sync.Mutex
	items    []*Item
	finished bool
	endedAt  time.Time
}

// JobView 任务快照（模板渲染用，值副本避免竞争）。
type JobView struct {
	ID        int64
	WithFiles bool
	CreatedAt time.Time
	Finished  bool
	EndedAt   time.Time
	Items     []Item
	Done      int
	Failed    int
	Total     int
}

// View 返回任务当前快照。
func (j *Job) View() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{
		ID:        j.ID,
		WithFiles: j.WithFiles,
		CreatedAt: j.CreatedAt,
		Finished:  j.finished,
		EndedAt:   j.endedAt,
		Total:     len(j.items),
		Items:     make([]Item, len(j.items)),
	}
	for i, it := range j.items {
		v.Items[i] = *it
		switch it.State {
		case StateDone, StateCached, StateFail:
			v.Done++
		}
		if it.State == StateFail {
			v.Failed++
		}
	}
	return v
}

func (j *Job) setItem(it *Item, state, version string, size int64, errMsg string) {
	j.mu.Lock()
	it.State = state
	it.Version = version
	it.Size = size
	it.Err = errMsg
	j.mu.Unlock()
}

// Manager 管理最近若干预热任务（内存态，重启清空）。
type Manager struct {
	engine *proxy.Engine
	mu     sync.Mutex
	jobs   []*Job // 最近 maxJobs 个
	nextID atomic.Int64
}

const maxJobs = 5

// NewManager 创建预热管理器。
func NewManager(engine *proxy.Engine) *Manager {
	return &Manager{engine: engine}
}

// Start 创建并异步执行预热任务（names 应已通过项目名校验）。
func (m *Manager) Start(names []string, withFiles bool) *Job {
	j := &Job{ID: m.nextID.Add(1), WithFiles: withFiles, CreatedAt: time.Now()}
	for _, n := range names {
		j.items = append(j.items, &Item{Name: n, State: StatePending})
	}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	if len(m.jobs) > maxJobs {
		m.jobs = m.jobs[len(m.jobs)-maxJobs:]
	}
	m.mu.Unlock()
	go m.run(j)
	return j
}

// Latest 返回最近一次任务（无任务时为 nil）。
func (m *Manager) Latest() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) == 0 {
		return nil
	}
	return m.jobs[len(m.jobs)-1]
}

// LatestView 返回最近一次任务的快照（无任务时为 nil）。
func (m *Manager) LatestView() *JobView {
	j := m.Latest()
	if j == nil {
		return nil
	}
	v := j.View()
	return &v
}

func (m *Manager) run(j *Job) {
	sem := make(chan struct{}, workerCount)
	var wg sync.WaitGroup
	for _, it := range j.items {
		wg.Add(1)
		go func(it *Item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m.process(j, it)
		}(it)
	}
	wg.Wait()
	j.mu.Lock()
	j.finished = true
	j.endedAt = time.Now()
	j.mu.Unlock()
}

func (m *Manager) process(j *Job, it *Item) {
	j.setItem(it, StateRunning, "", 0, "")
	state, version, size, err := m.engine.PreheatProject(it.Name, "", j.WithFiles)
	if err != nil {
		j.setItem(it, StateFail, version, size, err.Error())
		return
	}
	j.setItem(it, state, version, size, "")
}
