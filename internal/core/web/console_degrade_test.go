package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// brokenEco 生态句柄替身：指定方法返回异常（非 JSON 或 >= 400），
// 用于验证聚合层的降级语义——单生态异常不影响其他生态与整体 200。
type brokenEco struct{ fakeEco }

func (f *brokenEco) Summary(c *gin.Context) {
	c.Data(http.StatusBadGateway, "text/plain; charset=utf-8", []byte("upstream boom"))
}

func (f *brokenEco) DashboardAPI(c *gin.Context) {
	c.Data(http.StatusInternalServerError, "application/json", []byte(`not-json`))
}

// TestSummaryDegradesPerEco 单生态句柄异常时：整体仍 200，异常段降级为
// {"ok":false,"error":"生态句柄响应异常","status":…}，正常生态段不受影响。
func TestSummaryDegradesPerEco(t *testing.T) {
	con := newTestConsole(t)
	con.Npm = &brokenEco{fakeEco{eco: "npm"}}
	w := call(con.SummaryAggregated, "/api/summary")
	if w.Code != http.StatusOK {
		t.Fatalf("聚合层整体应保持 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error":"生态句柄响应异常"`) {
		t.Fatalf("异常生态段应降级: %s", body)
	}
	if !strings.Contains(body, `"eco":"go"`) || !strings.Contains(body, `"eco":"py"`) {
		t.Fatalf("正常生态段应保留: %s", body)
	}
}

// TestDashboardDegradesPerEco 仪表盘聚合同样按生态降级。
func TestDashboardDegradesPerEco(t *testing.T) {
	con := newTestConsole(t)
	con.Go = &brokenEco{fakeEco{eco: "go"}}
	w := call(con.DashboardAggregated, "/api/admin/dashboard")
	if w.Code != http.StatusOK {
		t.Fatalf("聚合层整体应保持 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error":"生态句柄响应异常"`) {
		t.Fatalf("异常生态段应降级: %s", body)
	}
	if !strings.Contains(body, `"eco":"npm"`) {
		t.Fatalf("正常生态段应保留: %s", body)
	}
}
