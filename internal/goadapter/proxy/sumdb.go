package proxy

import (
	"context"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"permaporter/internal/goadapter/models"
)

// sumdbTTL /supported 探测结果缓存时长。
const sumdbProbeTTL = 10 * time.Minute

// sumdbNameRe sumdb 名称校验：小写字母数字点横线（如 sum.golang.org）。
var sumdbNameRe = regexp.MustCompile(`^[a-z0-9.-]+$`)

// sumdbSumGolangOrg 最常见的校验和数据库（管理接口测试用）。
const sumdbSumGolangOrg = "sum.golang.org"

// ValidSumdbName 校验 sumdb 名称：仅小写域名形式，拒绝 `..` 与超长输入。
func ValidSumdbName(name string) bool {
	if name == "" || len(name) > 253 || strings.Contains(name, "..") {
		return false
	}
	return sumdbNameRe.MatchString(name)
}

// ServeSumdb 处理 GET /sumdb/<name>/...：
//   - /supported：探测上游同样路径（结果缓存 10 分钟），200 → 200，否则 404
//     （404 让 go 客户端回退直连 sumdb，客户端不会因此报错）；
//   - 其它（latest / lookup / tile）：透传上游状态码与响应体，不落盘、不缓存，
//     避免透明日志的一致性风险。
//
// 目标始终是 <上游>/sumdb/<name>/...，不直接访问 <name> 主机，避免 SSRF。
func (e *Engine) ServeSumdb(c *gin.Context, name, rest string) {
	start := time.Now()
	if !ValidSumdbName(name) {
		e.proxyError(c, http.StatusBadRequest, "非法的 sumdb 名称")
		return
	}
	settings, err := e.DB.GetSettings()
	if err != nil {
		e.proxyError(c, http.StatusServiceUnavailable, "服务未完成初始化")
		e.logReq(c, "sumdb", name, false, start, "未初始化")
		return
	}
	switch {
	case rest == "supported":
		if e.sumdbSupported(settings, name) {
			c.Status(http.StatusOK)
		} else {
			c.Status(http.StatusNotFound)
		}
		e.logReq(c, "sumdb", name+"/supported", false, start, "")
		return
	case rest == "latest" || strings.HasPrefix(rest, "lookup/") || strings.HasPrefix(rest, "tile/"):
	default:
		e.proxyError(c, http.StatusNotFound, "未知的 sumdb 端点")
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		e.proxyError(c, http.StatusMethodNotAllowed, "只读代理")
		return
	}

	release, err := e.acquireUpstream(c.Request.Context())
	if err != nil {
		e.proxyError(c, http.StatusBadGateway, "上游并发已满")
		return
	}
	defer release()
	client := e.clients.Get(settings.OutboundProxy)
	urlPath := "/sumdb/" + name + "/" + rest
	ok := false
	for _, base := range upstreamCandidates(settings, urlPath) {
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, base, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[proxy] sumdb 透传失败 %s: %v", base, err)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxTileSize+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if rerr != nil {
			log.Printf("[proxy] 读取 sumdb 响应失败 %s: %v", base, rerr)
			continue
		}
		if int64(len(body)) > maxTileSize {
			// 超限的 tile 截断透传会让 go 客户端校验失败：按上游错误处理并换候选
			log.Printf("[proxy] sumdb 响应超过大小上限 %d: %s", maxTileSize, base)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
			log.Printf("[proxy] sumdb 上游返回 %d: %s", resp.StatusCode, base)
			continue // 其它状态尝试备用上游
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			c.Header("Content-Type", ct)
		}
		if rest == "latest" {
			c.Header("Cache-Control", "no-store")
		}
		c.Status(resp.StatusCode)
		if c.Request.Method != http.MethodHead {
			_, _ = c.Writer.Write(body)
		}
		e.logReq(c, "sumdb", name+"/"+rest, false, start, "")
		ok = true
		break
	}
	if !ok {
		// 上游不可用：立即失效 /supported 探测缓存，让下一个客户端会话重新探测，
		// 得到 404 后回退直连 sumdb，而不是继续拿着过期的
		// "supported" 结论在代理模式下失败。
		e.sumMu.Lock()
		delete(e.sumdbOK, name)
		e.sumMu.Unlock()
		e.logReq(c, "sumdb", name+"/"+rest, false, start, "上游不可用")
		e.proxyError(c, http.StatusBadGateway, "上游不可用")
	}
}

// sumdbSupported 探测上游是否支持转发指定 sumdb（结果缓存 10 分钟）。
func (e *Engine) sumdbSupported(settings *models.Settings, name string) bool {
	e.sumMu.Lock()
	if p, ok := e.sumdbOK[name]; ok && time.Now().Before(p.until) {
		e.sumMu.Unlock()
		return p.supported
	}
	e.sumMu.Unlock()

	supported := false
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := e.clients.Get(settings.OutboundProxy)
	for _, base := range upstreamCandidates(settings, "/sumdb/"+name+"/supported") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			supported = true
			break
		}
		if resp.StatusCode == http.StatusNotFound {
			break // 上游明确不支持，不再尝试备用（内容一致性）
		}
	}
	e.sumMu.Lock()
	e.sumdbOK[name] = sumdbProbe{supported: supported, until: time.Now().Add(sumdbProbeTTL)}
	e.sumMu.Unlock()
	return supported
}
