package handlers

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// 设置类 JSON API：上游源 / 出站代理（均需登录）。

// SaveUpstream 保存主/备用上游源，立即生效。
func (a *App) SaveUpstream(c *gin.Context) {
	var req struct {
		UpstreamURL string `json:"upstreamUrl"`
		FallbackURL string `json:"fallbackUrl"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	upstream := strings.TrimSpace(req.UpstreamURL)
	fallback := strings.TrimSpace(req.FallbackURL)
	if err := validateHTTPURL(upstream); err != nil {
		failMsg(c, http.StatusBadRequest, "主上游源："+err.Error())
		return
	}
	if fallback != "" {
		if err := validateHTTPURL(fallback); err != nil {
			failMsg(c, http.StatusBadRequest, "备用上游："+err.Error())
			return
		}
	}
	if err := a.DB.UpdateUpstreams(upstream, fallback); err != nil {
		failMsg(c, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	okMsg(c, nil)
}

// SaveProxy 保存出站代理地址，立即生效。
func (a *App) SaveProxy(c *gin.Context) {
	var req struct {
		OutboundProxy string `json:"outboundProxy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	proxyURL := strings.TrimSpace(req.OutboundProxy)
	if proxyURL != "" && !isValidProxyScheme(proxyURL) {
		failMsg(c, http.StatusBadRequest, "代理地址仅支持 http://、https://、socks5:// 协议")
		return
	}
	if err := a.DB.UpdateOutboundProxy(proxyURL); err != nil {
		failMsg(c, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	okMsg(c, nil)
}

// TestProxy 使用请求中的代理地址测试上游连通性。
func (a *App) TestProxy(c *gin.Context) {
	var req struct {
		OutboundProxy string `json:"outboundProxy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		failMsg(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	proxyURL := strings.TrimSpace(req.OutboundProxy)
	if proxyURL != "" && !isValidProxyScheme(proxyURL) {
		failMsg(c, http.StatusBadRequest, "代理地址仅支持 http://、https://、socks5:// 协议")
		return
	}
	settings, err := a.DB.GetSettings()
	if err != nil {
		failMsg(c, http.StatusInternalServerError, "读取配置失败")
		return
	}
	latency, err := a.Engine.TestUpstream(proxyURL, settings.UpstreamURL)
	if err != nil {
		failMsg(c, http.StatusBadGateway, "连接失败: "+html.EscapeString(err.Error()))
		return
	}
	okMsg(c, gin.H{"latencyMs": latency.Milliseconds()})
}

func isValidProxyScheme(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "socks5://")
}

// validateHTTPURL 校验上游地址必须为可解析的 http(s) URL：
// 仅查前缀会放行 "http://" 或 "http://坏 url"，保存后所有回源请求都会失败。
func validateHTTPURL(u string) error {
	if u == "" {
		return errors.New("地址不能为空")
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return errors.New("地址必须是合法的 URL（含主机名）")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("地址必须以 http:// 或 https:// 开头")
	}
	return nil
}
