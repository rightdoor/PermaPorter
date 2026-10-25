package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/gin-gonic/gin"

	"permaporter/internal/core/utils"
	"permaporter/internal/goadapter/models"
)

// serveInfo 处理 GET /<module>/@v/<ver>.info：
// 规范版本永久缓存（磁盘命中即返回）；非规范 token（分支/提交号）透传上游不落盘。
func (e *Engine) serveInfo(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "meta", req.Module, false, start, "未初始化")
		return
	}
	name := req.Module + "@" + req.Version
	if req.IsCanonical() {
		path, perr := e.artifactPath(settings, req, ".info")
		if perr == nil {
			if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() {
				e.Stats.RecordMeta(true)
				e.DB.TouchModule(req.Module)
				e.serveImmutable(c, path, "application/json")
				e.logReq(c, "meta", name, true, start, "")
				return
			}
		}
	} else {
		// 非规范 token：透传上游，不写 DB、不落盘
		body, _, _, err := e.fetchBytes(c.Request.Context(), settings,
			"/"+req.EscapedModule+"/@v/"+req.EscapedVersion+".info", "", maxInfoSize)
		if err != nil {
			e.logReq(c, "meta", name, false, start, err.Error())
			if errors.Is(err, errUpstreamNotFound) {
				e.proxyError(c, 404, err.Error())
			} else {
				e.proxyError(c, 502, err.Error())
			}
			return
		}
		e.Stats.RecordMeta(false)
		e.serveMetaJSON(c, body)
		e.logReq(c, "meta", name, false, start, "")
		return
	}
	cached, err := e.ensureInfo(settings, req)
	if err != nil {
		e.logReq(c, "meta", name, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, 404, err.Error())
		} else {
			e.proxyError(c, 502, err.Error())
		}
		return
	}
	_ = cached // 走到这里必然是新拉取（命中分支已提前返回）
	path, perr := e.artifactPath(settings, req, ".info")
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	e.Stats.RecordMeta(false)
	e.DB.TouchModule(req.Module)
	e.serveImmutable(c, path, "application/json")
	e.logReq(c, "meta", name, false, start, "")
}

// serveMod 处理 GET /<module>/@v/<ver>.mod（规范版本，永久缓存）。
func (e *Engine) serveMod(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "meta", req.Module, false, start, "未初始化")
		return
	}
	name := req.Module + "@" + req.Version
	path, perr := e.artifactPath(settings, req, ".mod")
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() {
		e.Stats.RecordMeta(true)
		e.DB.TouchModule(req.Module)
		e.serveImmutable(c, path, "text/plain; charset=utf-8")
		e.logReq(c, "meta", name, true, start, "")
		return
	}
	if _, err := e.ensureMod(settings, req); err != nil {
		e.logReq(c, "meta", name, false, start, err.Error())
		if errors.Is(err, errUpstreamNotFound) {
			e.proxyError(c, 404, err.Error())
		} else {
			e.proxyError(c, 502, err.Error())
		}
		return
	}
	e.Stats.RecordMeta(false)
	e.DB.TouchModule(req.Module)
	e.serveImmutable(c, path, "text/plain; charset=utf-8")
	e.logReq(c, "meta", name, false, start, "")
}

// serveZip 处理 GET /<module>/@v/<ver>.zip（规范版本，永久缓存，支持 Range/304）。
func (e *Engine) serveZip(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "zip", req.Module, false, start, "未初始化")
		return
	}
	name := req.Module + "@" + req.Version
	path, perr := e.artifactPath(settings, req, ".zip")
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() && fi.Size() > 0 {
		e.reconcileZipRecord(settings, req, path, fi.Size())
		e.Stats.RecordZip(true)
		e.DB.TouchModule(req.Module)
		e.serveImmutable(c, path, "application/zip")
		e.logReq(c, "zip", name, true, start, "")
		return
	}
	// DB 记录已缓存但文件丢失：复位 zip 状态，重新下载并校验
	if v, gerr := e.DB.GetVersionLiteByPath(req.Module, req.Version); gerr == nil && v.ZipSizeBytes > 0 {
		_ = e.DB.ClearVersionZip(v.ID)
	}
	if _, err := e.ensureZip(settings, req); err != nil {
		e.logReq(c, "zip", name, false, start, err.Error())
		switch {
		case errors.Is(err, errUpstreamNotFound):
			e.proxyError(c, 404, err.Error())
		case errors.Is(err, errDiskFull):
			e.proxyError(c, 507, err.Error())
		default:
			e.proxyError(c, 502, err.Error())
		}
		return
	}
	e.Stats.RecordZip(false)
	e.DB.TouchModule(req.Module)
	e.serveImmutable(c, path, "application/zip")
	e.logReq(c, "zip", name, false, start, "")
}

// serveZiphash 处理 GET /<module>/@v/<ver>.ziphash（扩展端点）：
// 优先本地缓存文件；否则确保 zip 已缓存后返回本地计算的 h1 校验和。
func (e *Engine) serveZiphash(c *gin.Context, req *Request) {
	start := time.Now()
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, 503, "服务未完成初始化")
		e.logReq(c, "meta", req.Module, false, start, "未初始化")
		return
	}
	name := req.Module + "@" + req.Version
	hashPath, perr := e.artifactPath(settings, req, ".ziphash")
	if perr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	if _, rerr := os.Stat(hashPath); rerr == nil {
		e.Stats.RecordMeta(true)
		e.DB.TouchModule(req.Module)
		e.serveImmutable(c, hashPath, "text/plain; charset=utf-8")
		e.logReq(c, "meta", name, true, start, "")
		return
	}
	zipPath, zperr := e.artifactPath(settings, req, ".zip")
	if zperr != nil {
		e.proxyError(c, 500, "内部路径错误")
		return
	}
	// zip 未缓存则回源下载（下载流程会顺带计算并落盘 ziphash）
	if fi, serr := os.Stat(zipPath); serr != nil || fi.IsDir() || fi.Size() == 0 {
		if _, zerr := e.ensureZip(settings, req); zerr != nil {
			e.logReq(c, "meta", name, false, start, zerr.Error())
			if errors.Is(zerr, errUpstreamNotFound) {
				e.proxyError(c, 404, zerr.Error())
			} else {
				e.proxyError(c, 502, zerr.Error())
			}
			return
		}
	}
	if _, rerr := os.Stat(hashPath); rerr != nil {
		// zip 在库但 ziphash 仍缺失（如历史数据）：现算现写
		hash, herr := HashZipFile(zipPath)
		if herr != nil {
			e.logReq(c, "meta", name, false, start, herr.Error())
			e.proxyError(c, 500, "计算校验和失败")
			return
		}
		_ = writeFileAtomic(hashPath, []byte(hash+"\n"), 0o644)
	}
	e.Stats.RecordMeta(false)
	e.DB.TouchModule(req.Module)
	e.serveImmutable(c, hashPath, "text/plain; charset=utf-8")
	e.logReq(c, "meta", name, false, start, "")
}

// ---------- 产物缓存（供 serve* 与预热共用） ----------

// ensureInfo 确保 .info 已缓存，返回是否原本已缓存。
func (e *Engine) ensureInfo(settings *models.Settings, req *Request) (bool, error) {
	path, err := e.artifactPath(settings, req, ".info")
	if err != nil {
		return false, err
	}
	if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() {
		return true, nil
	}
	if !req.IsCanonical() {
		return false, errors.New("非规范版本不缓存")
	}
	key := "info:" + req.Module + "@" + req.Version
	_, ferr, _ := e.sf.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
		defer cancel()
		return nil, e.downloadInfo(ctx, settings, req)
	})
	if ferr != nil {
		return false, ferr
	}
	return false, nil
}

// ensureMod 确保 .mod 已缓存，返回是否原本已缓存。
func (e *Engine) ensureMod(settings *models.Settings, req *Request) (bool, error) {
	path, err := e.artifactPath(settings, req, ".mod")
	if err != nil {
		return false, err
	}
	if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() {
		return true, nil
	}
	key := "mod:" + req.Module + "@" + req.Version
	_, ferr, _ := e.sf.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), metaUpdateTimeout)
		defer cancel()
		return nil, e.downloadMod(ctx, settings, req)
	})
	if ferr != nil {
		return false, ferr
	}
	return false, nil
}

// ensureZip 确保 .zip 已缓存（含下载、校验、README 提取），返回是否原本已缓存。
// 与请求生命周期解耦（后台 ctx + 30 分钟预算），客户端断开不会导致同组等待者全部失败。
func (e *Engine) ensureZip(settings *models.Settings, req *Request) (bool, error) {
	path, err := e.artifactPath(settings, req, ".zip")
	if err != nil {
		return false, err
	}
	if fi, serr := os.Stat(path); serr == nil && !fi.IsDir() && fi.Size() > 0 {
		return true, nil
	}
	key := "zip:" + req.Module + "@" + req.Version
	_, ferr, _ := e.sf.Do(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), zipDownloadTimeout)
		defer cancel()
		return nil, e.downloadZip(ctx, settings, req, path)
	})
	if ferr != nil {
		return false, ferr
	}
	return false, nil
}

// reconcileZipRecord 自愈对账：磁盘 zip 命中但 DB 无 zip 记录时（如 DB 重建后首次命中），
// 把文件大小与校验和补登进索引并重算模块物化统计，保证"本地存储"统计与磁盘一致，
// 也让清理任务把该文件识别为有效缓存。失败只记日志，不影响本次响应。
func (e *Engine) reconcileZipRecord(settings *models.Settings, req *Request, fullPath string, size int64) {
	v, err := e.DB.GetVersionLiteByPath(req.Module, req.Version)
	if err == nil && v.ZipSizeBytes > 0 {
		return // 已一致
	}
	versionID, err := e.ensureVersionRow(req)
	if err != nil {
		log.Printf("[proxy] zip 对账建版本记录失败 %s@%s: %v", req.Module, req.Version, err)
		return
	}
	hash := ""
	if data, rerr := os.ReadFile(strings.TrimSuffix(fullPath, ".zip") + ".ziphash"); rerr == nil {
		hash = strings.TrimSpace(string(data))
	} else if h, herr := HashZipFile(fullPath); herr == nil {
		hash = h
	}
	relPath, rerr := filepath.Rel(settings.StorageDir, fullPath)
	if rerr != nil {
		return
	}
	if err := e.DB.SetVersionZip(versionID, size, filepath.ToSlash(relPath), hash); err != nil {
		log.Printf("[proxy] zip 对账更新索引失败 %s@%s: %v", req.Module, req.Version, err)
		return
	}
	log.Printf("[proxy] 已对账补登 zip 索引: %s@%s (%d bytes)", req.Module, req.Version, size)
}

// ensureVersionRow 确保模块与版本记录存在，返回版本记录 ID。
func (e *Engine) ensureVersionRow(req *Request) (int64, error) {
	moduleID, err := e.DB.UpsertModulePath(req.Module)
	if err != nil {
		return 0, err
	}
	return e.DB.UpsertVersionRow(moduleID, req.Version, module.IsPseudoVersion(req.Version))
}

// downloadInfo 下载 .info：落盘 + 建立版本记录（info_time/info_cached_at）。
func (e *Engine) downloadInfo(ctx context.Context, settings *models.Settings, req *Request) error {
	body, status, _, err := e.fetchBytes(ctx, settings,
		"/"+req.EscapedModule+"/@v/"+req.EscapedVersion+".info", "", maxInfoSize)
	if err != nil {
		return err
	}
	_ = status
	info, err := ParseInfoJSON(body)
	if err != nil {
		return fmt.Errorf("上游 .info 响应格式非法: %w", err)
	}
	path, err := e.artifactPath(settings, req, ".info")
	if err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := writeFileAtomic(path, body, 0o644); err != nil {
		return err
	}
	versionID, err := e.ensureVersionRow(req)
	if err != nil {
		return err
	}
	return e.DB.SetVersionInfo(versionID, info.Time)
}

// downloadMod 下载 .mod：落盘 + 解析 require/go 指令入库。
func (e *Engine) downloadMod(ctx context.Context, settings *models.Settings, req *Request) error {
	body, _, _, err := e.fetchBytes(ctx, settings,
		"/"+req.EscapedModule+"/@v/"+req.EscapedVersion+".mod", "", maxModSize)
	if err != nil {
		return err
	}
	path, err := e.artifactPath(settings, req, ".mod")
	if err != nil {
		return err
	}
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := writeFileAtomic(path, body, 0o644); err != nil {
		return err
	}
	requirementsJSON, goVersion := ParseGoMod(body)
	versionID, err := e.ensureVersionRow(req)
	if err != nil {
		return err
	}
	return e.DB.SetVersionMod(versionID, int64(len(body)), string(body), requirementsJSON, goVersion)
}

// downloadZip 下载 .zip 到临时文件，校验后原子落盘，并更新索引与 README。
// 上游闸只覆盖「发起请求 + 响应体写入临时文件」阶段：校验与 .ziphash 探测在
// 释放闸之后进行——fetchZiphash 会再次申请同一把闸，若校验期间仍持闸，
// 恰好 16 个并发下载会各自持槽等待第 17 个槽位，互相等待直至下载超时（自锁）。
func (e *Engine) downloadZip(ctx context.Context, settings *models.Settings, req *Request, destPath string) error {
	if err := ensureDir(filepath.Dir(destPath)); err != nil {
		return err
	}
	versionID, err := e.ensureVersionRow(req)
	if err != nil {
		return err
	}
	if err := ensureDir(filepath.Join(settings.StorageDir, "tmp")); err != nil {
		return err
	}
	tmpDir := filepath.Join(settings.StorageDir, "tmp")
	client := e.clients.Get(settings.OutboundProxy)
	candidates := upstreamCandidates(settings, "/"+req.EscapedModule+"/@v/"+req.EscapedVersion+".zip")
	for _, u := range candidates {
		tmpName, size, err := e.fetchZipToTemp(ctx, settings, client, req, u, tmpDir)
		if err != nil {
			if errors.Is(err, errUpstreamNotFound) || errors.Is(err, errDiskFull) {
				return err // 不可恢复：换上游也无济于事
			}
			log.Printf("[proxy] 下载 zip 失败 %s: %v", u, err)
			continue
		}
		if tmpName == "" {
			continue // 该候选上游不可用（状态异常或网络失败，已记日志）
		}
		// 此处已释放上游闸：校验、ziphash 探测、README 提取不占下载额度
		err = e.verifyAndStoreZip(ctx, settings, req, tmpName, destPath)
		os.Remove(tmpName) // 成功改名后为无害空操作
		if err != nil {
			if errors.Is(err, errUpstreamNotFound) || errors.Is(err, errDiskFull) || errors.Is(err, errZipVerify) {
				return err
			}
			log.Printf("[proxy] zip 下载失败 %s: %v", u, err)
			continue
		}
		// 更新 DB（zip 状态 + 物化统计），失败只影响统计不影响产物可用性
		relPath, _ := filepath.Rel(settings.StorageDir, destPath)
		ziphashPath := strings.TrimSuffix(destPath, ".zip") + ".ziphash"
		var hash string
		if data, rerr := os.ReadFile(ziphashPath); rerr == nil {
			hash = strings.TrimSpace(string(data))
		}
		if serr := e.DB.SetVersionZip(versionID, size, filepath.ToSlash(relPath), hash); serr != nil {
			log.Printf("[proxy] 更新 zip 索引失败: %v", serr)
		}
		return nil
	}
	return errors.New("所有上游均无法提供该 zip")
}

// fetchZipToTemp 持上游闸请求单个候选上游，把 200 响应体完整写入临时文件，
// 返回临时文件路径与大小。路径为空串表示该候选不可用（状态异常或网络失败，已记日志），
// 调用方应尝试下一候选；上游 404/410 与磁盘余量不足原样返回错误。
func (e *Engine) fetchZipToTemp(ctx context.Context, settings *models.Settings, client *http.Client, req *Request, url, tmpDir string) (string, int64, error) {
	release, err := e.acquireUpstream(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("上游并发已满: %w", err)
	}
	defer release()
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Printf("[proxy] 构建下载请求失败 %s: %v", url, err)
		return "", 0, nil
	}
	resp, err := client.Do(dlReq)
	if err != nil {
		e.Stats.RecordUpstreamError(errKindNetwork)
		log.Printf("[proxy] 下载 zip 失败 %s: %v", url, err)
		return "", 0, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		readAndClose(resp)
		e.Stats.RecordUpstreamError(errKindNotFound)
		return "", 0, errUpstreamNotFound
	}
	if resp.StatusCode != http.StatusOK {
		readAndClose(resp)
		e.Stats.RecordUpstreamError(errKindStatus)
		log.Printf("[proxy] zip 下载返回 %d: %s", resp.StatusCode, url)
		return "", 0, nil // 5xx/403 等切备用上游
	}
	expected := resp.ContentLength // 上游声明长度；chunked 时为 -1
	if expected > maxZipSize {
		return "", 0, fmt.Errorf("zip 超过大小上限")
	}
	// 下载前检查磁盘剩余空间（至少 Content-Length + 256MB 余量）
	if err := utils.DiskHasRoom(settings.StorageDir, expected); err != nil {
		log.Printf("[proxy] %v，拒绝下载 %s@%s", err, req.Module, req.Version)
		return "", 0, err
	}
	tmp, err := os.CreateTemp(tmpDir, ".zip-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	limited := io.LimitReader(resp.Body, maxZipSize+1)
	size, copyErr := io.Copy(tmp, limited)
	// Close 的写缓存落盘失败（如磁盘满）与复制失败同等处理：截断/损坏 zip 不得进入缓存
	closeErr := tmp.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tmpName)
		return "", 0, copyErr
	}
	if size > maxZipSize {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("zip 超过大小上限")
	}
	// 截断检测：声明了长度但实际不符说明连接中断，不能落盘
	if expected > 0 && size != expected {
		os.Remove(tmpName)
		return "", 0, fmt.Errorf("zip 长度不符（期望 %d，实际 %d）", expected, size)
	}
	return tmpName, size, nil
}

// verifyAndStoreZip 校验临时文件（结构 + h1 + 上游 ziphash 比对），提取 README，
// 写入 .ziphash 后原子改名到目标路径。调用方必须已释放上游闸：内部的
// fetchZiphash 会再次申请闸，持闸调用会重新引入自锁。
func (e *Engine) verifyAndStoreZip(ctx context.Context, settings *models.Settings, req *Request, tmpName, destPath string) error {
	// 校验：魔数 + 结构（zip.CheckZip）+ h1 校验和
	if err := VerifyZipFile(req.Module, req.Version, tmpName); err != nil {
		e.Stats.RecordUpstreamError(errKindVerify)
		return errors.Join(errZipVerify, err)
	}
	hash, err := HashZipFile(tmpName)
	if err != nil {
		e.Stats.RecordUpstreamError(errKindVerify)
		return errors.Join(errZipVerify, err)
	}
	// 上游提供 .ziphash 时比对一致；否则以本地计算值为准
	if wantHash, zerr := e.fetchZiphash(ctx, settings, req); zerr == nil && wantHash != "" {
		if wantHash != hash {
			log.Printf("[proxy] 上游 ziphash 不一致（期望 %s，本地 %s）: %s@%s", wantHash, hash, req.Module, req.Version)
			e.Stats.RecordUpstreamError(errKindVerify)
			return errors.Join(errZipVerify, errors.New("上游 ziphash 校验失败"))
		}
	}
	// 从 zip 提取根目录 README（失败不影响 zip 缓存）
	readme, rerr := ExtractZipReadme(tmpName, maxReadmeSize)
	if rerr == nil && strings.TrimSpace(readme) != "" {
		moduleID, uerr := e.DB.UpsertModulePath(req.Module)
		if uerr == nil {
			_ = e.DB.SaveReadme(moduleID, req.Version, readme, Summarize(readme))
		}
	}
	if err := writeFileAtomic(strings.TrimSuffix(destPath, ".zip")+".ziphash", []byte(hash+"\n"), 0o644); err != nil {
		log.Printf("[proxy] 写入 ziphash 失败: %v", err)
	}
	return renameReplace(tmpName, destPath)
}

// errZipVerify zip 内容校验失败（结构非法/校验和不一致）。
var errZipVerify = errors.New("zip 校验失败")

// readAndClose 读完并丢弃响应体并关闭连接，使其可归还复用。
func readAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// Requirement go.mod 的 require 条目（详情页展示）。
type Requirement struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}

// ParseGoMod 解析 go.mod 原文，返回 require 列表 JSON 与 go 指令版本。
// 解析失败（如上游合成的最小 go.mod 变体）时返回空串，不影响 .mod 缓存。
func ParseGoMod(body []byte) (requirementsJSON, goVersion string) {
	f, err := modfile.Parse("go.mod", body, nil)
	if err != nil {
		return "", ""
	}
	if f.Go != nil {
		goVersion = f.Go.Version
	}
	var reqs []Requirement
	for _, r := range f.Require {
		reqs = append(reqs, Requirement{Path: r.Mod.Path, Version: r.Mod.Version, Indirect: r.Indirect})
	}
	if len(reqs) == 0 {
		return "", goVersion
	}
	data, err := json.Marshal(reqs)
	if err != nil {
		return "", goVersion
	}
	return string(data), goVersion
}
