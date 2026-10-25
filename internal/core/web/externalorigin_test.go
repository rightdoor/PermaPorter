// externalOrigin 端口推导的单元测试（容器映射偏移 / 直连 / https 标准口 / 端口省略 / 头信任边界）。
package web

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"permaporter/internal/config"
)

// originCtx 构造带指定对端地址与转发头的请求上下文（直调 externalOrigin 用）。
func originCtx(remote, target string, hdr map[string]string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("GET", target, nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}

// directCfg 直连部署形态（五端口即外部端口）。
func directCfg() *config.Config {
	return &config.Config{UIPort: 9090, NPMPort: 9091, GoPort: 9092, PyPort: 9093, MVNPort: 9094}
}

// containerCfg 容器部署形态（后端监听 809x，宿主映射 909x，统一偏移 +1000）。
func containerCfg() *config.Config {
	return &config.Config{UIPort: 8090, NPMPort: 8091, GoPort: 8092, PyPort: 8093, MVNPort: 8094}
}

// assertOrigins 断言四生态注入地址。
func assertOrigins(t *testing.T, c *gin.Context, cfg *config.Config, goURL, npmURL, pyURL, mvnURL string) {
	t.Helper()
	if got := externalOrigin(c, cfg, cfg.GoPort); got != goURL {
		t.Fatalf("goProxy 应为 %s，实际 %s", goURL, got)
	}
	if got := externalOrigin(c, cfg, cfg.NPMPort); got != npmURL {
		t.Fatalf("npmRegistry 应为 %s，实际 %s", npmURL, got)
	}
	if got := externalOrigin(c, cfg, cfg.PyPort); got != pyURL {
		t.Fatalf("pyIndex 应为 %s，实际 %s", pyURL, got)
	}
	if got := externalOrigin(c, cfg, cfg.MVNPort); got != mvnURL {
		t.Fatalf("mavenRepo 应为 %s，实际 %s", mvnURL, got)
	}
}

// TestExternalOriginDirect 直连二进制：请求 Host 显式端口等于 UIPort，偏移 0，注入配置端口。
func TestExternalOriginDirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := directCfg()
	c := originCtx("192.0.2.10:40000", "http://example.com:9090/", nil)
	assertOrigins(t, c, cfg,
		"http://example.com:9092", "http://example.com:9091", "http://example.com:9093", "http://example.com:9094")
}

// TestExternalOriginContainer 容器形态：本机回环代理（nginx）携带外部端口 9090，
// 后端配置端口 8090，统一偏移 +1000 → 注入 9091-9094。X-Forwarded-Host 与 Host 两种携带方式等价。
func TestExternalOriginContainer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := containerCfg()
	// nginx 实际形态：设 X-Forwarded-Host $http_host（含端口）
	c := originCtx("127.0.0.1:40000", "http://127.0.0.1:8090/",
		map[string]string{"X-Forwarded-Host": "example.com:9090"})
	assertOrigins(t, c, cfg,
		"http://example.com:9092", "http://example.com:9091", "http://example.com:9093", "http://example.com:9094")
	// 无 X-Forwarded-Host 时回退请求自身 Host（同样含外部端口）
	c = originCtx("127.0.0.1:40000", "http://example.com:9090/", nil)
	assertOrigins(t, c, cfg,
		"http://example.com:9092", "http://example.com:9091", "http://example.com:9093", "http://example.com:9094")
}

// TestExternalOriginHttpsNoPort https 反代标准口（无显式端口）：维持配置端口
// （覆盖"https 反代 UI + 协议口直出"部署，此时配置端口即外部端口）。
func TestExternalOriginHttpsNoPort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := directCfg()
	c := originCtx("127.0.0.1:40000", "http://127.0.0.1:9090/",
		map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "example.com"})
	assertOrigins(t, c, cfg,
		"https://example.com:9092", "https://example.com:9091", "https://example.com:9093", "https://example.com:9094")
}

// TestExternalOriginPortOmission 端口省略约定对配置端口与推导后端口同样适用：
// http+80 / https+443 不携带端口号。
func TestExternalOriginPortOmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{UIPort: 9090, NPMPort: 9091, GoPort: 80, PyPort: 443, MVNPort: 9094}
	c := originCtx("192.0.2.10:40000", "http://example.com:9090/", nil)
	if got := externalOrigin(c, cfg, cfg.GoPort); got != "http://example.com" {
		t.Fatalf("http+80 应省略端口，实际 %s", got)
	}
	// 推导后命中 80 同样省略（UI 外部 79 → 偏移 -9011，9091 推导为 80，http+80 省略）
	c = originCtx("127.0.0.1:40000", "http://example.com:9090/",
		map[string]string{"X-Forwarded-Host": "example.com:79"})
	cfg2 := &config.Config{UIPort: 9090, NPMPort: 9091}
	if got := externalOrigin(c, cfg2, cfg2.NPMPort); got != "http://example.com" {
		t.Fatalf("推导后 http+80 场景应省略端口，实际 %s", got)
	}
}

// TestExternalOriginUntrustedPeer 非本机回环对端：转发头一概不采信——
// 伪造的 X-Forwarded-Host/X-Forwarded-Proto 既不改变 host/scheme，也不参与端口推导。
func TestExternalOriginUntrustedPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := containerCfg()
	c := originCtx("203.0.113.9:50000", "http://real.example.com:8090/",
		map[string]string{"X-Forwarded-Host": "evil.example:7090", "X-Forwarded-Proto": "https"})
	assertOrigins(t, c, cfg,
		"http://real.example.com:8092", "http://real.example.com:8091", "http://real.example.com:8093", "http://real.example.com:8094")
}
