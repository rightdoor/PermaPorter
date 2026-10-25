package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/gin-gonic/gin"

	"permaporter/internal/goadapter/models"
)

// serveList 处理 GET /<module>/@v/list：
// 本地命中且未过期直接返回；已过期时先刷新再返回（最多等 MetaRefreshWait），
// 刷新失败或超时回退旧缓存（可用性优先）；冷缓存同步回源。
func (e *Engine) serveList(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "meta", req.Module, false, start, "未初始化")
		return
	}
	path, perr := e.listPath(settings, req)
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	if m, gerr := e.DB.GetModuleLite(req.Module); gerr == nil {
		if metaStale(m.MetaCachedAt, settings.MetaTTL) && !e.refreshCoolingDown("list:"+req.Module) {
			// 缓存过期：先刷新再返回，失败/超时回退旧缓存（refreshList 内部已归并并发）
			_ = e.refreshList(req.Module, settings, e.MetaRefreshWait)
		}
		if data, rerr := os.ReadFile(path); rerr == nil {
			e.Stats.RecordMeta(true)
			e.DB.TouchModule(req.Module)
			e.serveMetaText(c, data)
			e.logReq(c, "meta", req.Module, true, start, "")
			return
		}
	}
	// 冷缓存（或 DB 有记录但文件丢失）：同步拉取
	if err := e.refreshList(req.Module, settings, 0); err != nil {
		e.logReq(c, "meta", req.Module, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, 404, err.Error())
		} else {
			e.proxyError(c, 502, err.Error())
		}
		return
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		e.logReq(c, "meta", req.Module, false, start, "读取缓存失败")
		e.proxyError(c, 500, "读取缓存失败")
		return
	}
	e.Stats.RecordMeta(false)
	e.DB.TouchModule(req.Module)
	e.serveMetaText(c, data)
	e.logReq(c, "meta", req.Module, false, start, "")
}

// serveLatest 处理 GET /<module>/@latest，策略与 @v/list 一致。
func (e *Engine) serveLatest(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "meta", req.Module, false, start, "未初始化")
		return
	}
	path, perr := e.latestPath(settings, req)
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	if m, gerr := e.DB.GetModuleLite(req.Module); gerr == nil {
		if metaStale(m.LatestCachedAt, settings.MetaTTL) && !e.refreshCoolingDown("latest:"+req.Module) {
			_ = e.refreshLatest(req.Module, settings, e.MetaRefreshWait)
		}
		if data, rerr := os.ReadFile(path); rerr == nil {
			e.Stats.RecordMeta(true)
			e.DB.TouchModule(req.Module)
			e.serveMetaJSON(c, data)
			e.logReq(c, "meta", req.Module, true, start, "")
			return
		}
	}
	if err := e.refreshLatest(req.Module, settings, 0); err != nil {
		e.logReq(c, "meta", req.Module, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, 404, err.Error())
		} else {
			e.proxyError(c, 502, err.Error())
		}
		return
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		e.logReq(c, "meta", req.Module, false, start, "读取缓存失败")
		e.proxyError(c, 500, "读取缓存失败")
		return
	}
	e.Stats.RecordMeta(false)
	e.DB.TouchModule(req.Module)
	e.serveMetaJSON(c, data)
	e.logReq(c, "meta", req.Module, false, start, "")
}

// refreshResult 刷新等待的通用结果。
type refreshResult struct{ err error }

// refreshList 同步刷新 @v/list，用 singleflight 归并同模块并发请求。
// wait > 0 时最多等待 wait：超时返回 errMetaRefreshTimeout，后台刷新继续执行。
func (e *Engine) refreshList(modulePath string, settings *models.Settings, wait time.Duration) error {
	done := make(chan refreshResult, 1)
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		key := "list:" + modulePath
		_, err, _ := e.sf.Do(key, func() (any, error) {
			// 与请求生命周期解耦：客户端断开不会连坐同模块全部等待者
			ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
			defer cancel()
			return nil, e.fetchAndCacheList(ctx, settings, modulePath)
		})
		if err != nil {
			e.markRefreshFailed(key)
			done <- refreshResult{err: err}
			return
		}
		e.clearRefreshFailure(key)
		done <- refreshResult{}
	}()
	return e.waitListRefresh(done, wait, "list:"+modulePath)
}

// refreshLatest 同步刷新 @latest，策略与 refreshList 一致。
func (e *Engine) refreshLatest(modulePath string, settings *models.Settings, wait time.Duration) error {
	done := make(chan refreshResult, 1)
	e.bgWG.Add(1)
	go func() {
		defer e.bgWG.Done()
		key := "latest:" + modulePath
		_, err, _ := e.sf.Do(key, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
			defer cancel()
			return nil, e.fetchAndCacheLatest(ctx, settings, modulePath)
		})
		if err != nil {
			e.markRefreshFailed(key)
			done <- refreshResult{err: err}
			return
		}
		e.clearRefreshFailure(key)
		done <- refreshResult{}
	}()
	return e.waitListRefresh(done, wait, "latest:"+modulePath)
}

// waitListRefresh 等待刷新结果；wait > 0 时超时先返回 errMetaRefreshTimeout
// （后台刷新继续执行，成功后自动清除冷却），并对该 key 进入短暂冷却，
// 避免上游故障期间每个请求都排队等待同一个慢刷新。
func (e *Engine) waitListRefresh(done <-chan refreshResult, wait time.Duration, key string) error {
	if wait <= 0 {
		return (<-done).err
	}
	select {
	case r := <-done:
		return r.err
	case <-time.After(wait):
		// 超时瞬间后台可能刚好完成：再取一次，避免把成功的刷新误标为失败（否则会白等一分钟冷却）
		select {
		case r := <-done:
			return r.err
		default:
		}
		e.markRefreshFailed(key)
		return errMetaRefreshTimeout
	}
}

// fetchAndCacheList 拉取上游 @v/list，规范化后落盘并更新索引。
func (e *Engine) fetchAndCacheList(ctx context.Context, settings *models.Settings, modulePath string) error {
	etag := ""
	if m, err := e.DB.GetModuleLite(modulePath); err == nil {
		etag = m.ListETag
	}
	esc := escapePath(modulePath)
	req := &Request{Module: modulePath, EscapedModule: esc}
	path, perr := e.listPath(settings, req)
	if perr != nil {
		return perr
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}

	listURL := "/" + esc + "/@v/list"
	body, status, newETag, err := e.fetchBytes(ctx, settings, listURL, etag, maxListSize)
	if status == 304 {
		// 缓存文件丢失（如被手工删除）时忽略条件头重新拉取：
		// 否则 ETag 永远命中 304、文件永不重建，该模块将永久无法提供 list。
		if _, serr := os.Stat(path); serr == nil {
			e.DB.TouchMetaCachedAt(modulePath)
			return nil
		}
		body, status, newETag, err = e.fetchBytes(ctx, settings, listURL, "", maxListSize)
	}
	if err != nil {
		return err
	}
	versions := NormalizeList(body)
	// 空列表是合法响应：写空文件并返回 200
	stored := strings.Join(versions, "\n")
	if len(versions) > 0 {
		stored += "\n"
	}
	if err := writeFileAtomic(path, []byte(stored), 0o644); err != nil {
		return err
	}
	moduleID, err := e.DB.UpsertModulePath(modulePath)
	if err != nil {
		return err
	}
	if err := e.DB.MarkListed(moduleID, versions, module.IsPseudoVersion); err != nil {
		return err
	}
	if err := e.DB.UpdateListCached(modulePath, newETag); err != nil {
		return err
	}
	// @latest 尚未拉取时，按最高 release 推导最新版本供 UI 展示
	if latest := deriveLatest(versions); latest != "" {
		_ = e.DB.UpdateLatestFromList(moduleID, latest, "")
	}
	return nil
}

// fetchAndCacheLatest 拉取上游 @latest，落盘原始 JSON 并更新索引。
func (e *Engine) fetchAndCacheLatest(ctx context.Context, settings *models.Settings, modulePath string) error {
	esc := escapePath(modulePath)
	req := &Request{Module: modulePath, EscapedModule: esc}
	path, perr := e.latestPath(settings, req)
	if perr != nil {
		return perr
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}

	body, _, _, err := e.fetchBytes(ctx, settings, "/"+esc+"/@latest", "", maxInfoSize)
	if err != nil {
		return err
	}
	info, err := ParseInfoJSON(body)
	if err != nil || info.Version == "" {
		return errors.New("上游 @latest 响应格式非法")
	}
	if err := writeFileAtomic(path, body, 0o644); err != nil {
		return err
	}
	if err := e.DB.UpdateLatestCached(modulePath, info.Version, info.Time); err != nil {
		return err
	}
	// 最新版本顺带标记为 listed（不覆盖已有产物状态）
	if moduleID, uerr := e.DB.UpsertModulePath(modulePath); uerr == nil {
		_ = e.DB.MarkListed(moduleID, []string{info.Version}, module.IsPseudoVersion)
	}
	return nil
}

// ---------- @v/list 规范化 ----------

// NormalizeList 规范化上游 @v/list 内容：
// 只保留可解析的 semver 行，过滤伪版本，去重后按 semver 升序（相等按字符串）排序。
func NormalizeList(body []byte) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		v := strings.TrimSpace(line)
		if v == "" || !semver.IsValid(v) || module.IsPseudoVersion(v) {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if c := semver.Compare(out[i], out[j]); c != 0 {
			return c < 0
		}
		return out[i] < out[j]
	})
	return out
}

// deriveLatest 从升序版本列表推导最新版本：优先最高 release，无 release 时取最高 prerelease。
func deriveLatest(versions []string) string {
	for i := len(versions) - 1; i >= 0; i-- {
		if semver.Prerelease(versions[i]) == "" {
			return versions[i]
		}
	}
	if len(versions) > 0 {
		return versions[len(versions)-1]
	}
	return ""
}

// InfoJSON .info / @latest 响应结构。
type InfoJSON struct {
	Version string `json:"Version"`
	Time    string `json:"Time"`
}

// ParseInfoJSON 解析 .info JSON（Version 必填）。
func ParseInfoJSON(body []byte) (*InfoJSON, error) {
	var info InfoJSON
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	if info.Version == "" {
		return nil, errors.New("缺少 Version 字段")
	}
	return &info, nil
}
