// Package preheat 提供依赖清单预热：后台批量缓存模块产物，
// 把"首次安装赌网络"变成"先预热、后离线装"。
// 输入支持完整 go.mod、go.sum 与每行 module / module@version 的纯文本。
package preheat

import (
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"permaporter/internal/goadapter/proxy"
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

// maxTargets 单次预热条目上限（去重后）。
const maxTargets = 500

// Target 单个预热目标：模块路径 + 版本（版本可为空，执行时解析最新）。
type Target struct {
	Module  string
	Version string
}

// Key 目标的去重键。
func (t Target) Key() string {
	if t.Version == "" {
		return t.Module
	}
	return t.Module + "@" + t.Version
}

// Item 单个模块的预热状态。
type Item struct {
	Name    string
	State   string
	Version string
	Size    int64
	Err     string
}

// Job 一次预热任务。
type Job struct {
	ID          int64
	WithZip     bool
	CreatedAt   time.Time

	mu       sync.Mutex
	items    []*Item
	targets  []Target
	finished bool
	endedAt  time.Time
}

// JobView 任务快照（模板渲染用，值副本避免竞争）。
type JobView struct {
	ID        int64
	WithZip   bool
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
		WithZip:   j.WithZip,
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

// Start 创建并异步执行预热任务（targets 应已通过校验与去重）。
func (m *Manager) Start(targets []Target, withZip bool) *Job {
	j := &Job{ID: m.nextID.Add(1), WithZip: withZip, CreatedAt: time.Now(), targets: targets}
	for _, t := range targets {
		name := t.Module
		if t.Version != "" {
			name += "@" + t.Version
		}
		j.items = append(j.items, &Item{Name: name, State: StatePending})
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
	for i, t := range j.targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m.process(j, j.items[i], t)
		}(i, t)
	}
	wg.Wait()
	j.mu.Lock()
	j.finished = true
	j.endedAt = time.Now()
	j.mu.Unlock()
}

func (m *Manager) process(j *Job, it *Item, t Target) {
	j.setItem(it, StateRunning, "", 0, "")
	state, version, size, err := m.engine.PreheatModule(t.Module, t.Version, j.WithZip)
	if err != nil {
		j.setItem(it, StateFail, version, size, err.Error())
		return
	}
	j.setItem(it, state, version, size, "")
}

// ---------- 输入解析 ----------

// goSumLineRe go.sum 行：<module> <version>[/go.mod] <hash>。
var goSumLineRe = regexp.MustCompile(`^(\S+)\s+(v\S+?)(?:/go\.mod)?\s+(\S+)$`)

// ParseInput 解析预热输入，返回去重后的有效目标与无法识别的行。
// 优先按完整 go.mod 解析（require 含 // indirect）；否则按 go.sum 逐行解析；
// 都不匹配时按纯文本逐行解析（module 或 module@version）。
func ParseInput(raw string) (targets []Target, invalid []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if f, err := modfile.Parse("go.mod", []byte(raw), nil); err == nil && len(f.Require) > 0 {
		seen := map[string]bool{}
		for _, r := range f.Require {
			t := Target{Module: r.Mod.Path, Version: r.Mod.Version}
			if seen[t.Key()] {
				continue
			}
			if proxy.ValidateModuleInput(t.Module) != nil || (t.Version != "" && !semver.IsValid(t.Version)) {
				invalid = append(invalid, t.Key())
				continue
			}
			seen[t.Key()] = true
			targets = append(targets, t)
		}
		if len(targets) > 0 || len(invalid) > 0 {
			return dedupeCap(targets), invalid
		}
	}
	seen := map[string]bool{}
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 行内注释：从 // 或 # 起截断
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		// go.sum 行：<module> <version>[/go.mod] <hash>
		if m := goSumLineRe.FindStringSubmatch(line); m != nil {
			t := Target{Module: m[1], Version: m[2]}
			if seen[t.Key()] {
				continue
			}
			// 版本校验与纯文本分支同一口径：非法版本不直达上游
			if proxy.ValidateModuleInput(t.Module) != nil || !semver.IsValid(t.Version) {
				invalid = append(invalid, line)
				continue
			}
			seen[t.Key()] = true
			targets = append(targets, t)
			continue
		}
		// 纯文本：module 或 module@version（模块路径自身可能含 @ 吗？不含，@ 只出现在编码外）
		mod, ver := splitModuleVersion(line)
		if seen[Target{Module: mod, Version: ver}.Key()] {
			continue
		}
		if proxy.ValidateModuleInput(mod) != nil {
			invalid = append(invalid, line)
			continue
		}
		if ver != "" && !semver.IsValid(ver) {
			invalid = append(invalid, line)
			continue
		}
		seen[Target{Module: mod, Version: ver}.Key()] = true
		targets = append(targets, Target{Module: mod, Version: ver})
	}
	return dedupeCap(targets), invalid
}

// splitModuleVersion 拆分 module@version；无 @ 时版本为空（执行时解析最新版本）。
func splitModuleVersion(s string) (string, string) {
	if i := strings.LastIndexByte(s, '@'); i > 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// dedupeCap 去重并截断到 maxTargets。
func dedupeCap(targets []Target) []Target {
	seen := map[string]bool{}
	out := targets[:0]
	for _, t := range targets {
		if seen[t.Key()] {
			continue
		}
		seen[t.Key()] = true
		out = append(out, t)
	}
	if len(out) > maxTargets {
		out = out[:maxTargets]
	}
	return out
}
