package utils

import (
	"strings"
	"testing"
)

// TestRenderMarkdownHardensRemoteResources 校验 README 渲染后的图片/外链属性。
func TestRenderMarkdownHardensRemoteResources(t *testing.T) {
	src := "![logo](https://example.com/logo.png)\n\n[文档](https://example.com/doc)\n\n[站内](/packages)\n"
	out := string(RenderMarkdown(src))

	if !strings.Contains(out, `loading="lazy"`) {
		t.Errorf("图片应带 loading=lazy: %s", out)
	}
	if !strings.Contains(out, `referrerpolicy="no-referrer"`) {
		t.Errorf("图片应带 referrerpolicy=no-referrer: %s", out)
	}
	if !strings.Contains(out, `rel="noopener noreferrer"`) {
		t.Errorf("外链应带 rel=noopener noreferrer: %s", out)
	}
	if !strings.Contains(out, `target="_blank"`) {
		t.Errorf("外链应在新窗口打开: %s", out)
	}
	// 站内相对链接不应被改写为外链
	if strings.Contains(out, `href="/packages" target="_blank"`) {
		t.Errorf("站内链接不应加 target=_blank: %s", out)
	}
}

// TestRenderMarkdownEscapesRawHTML 确认未开启 Unsafe 时原始 HTML 被转义（XSS 防线）。
func TestRenderMarkdownEscapesRawHTML(t *testing.T) {
	out := string(RenderMarkdown(`<script>alert(1)</script>`))
	if strings.Contains(out, "<script>") {
		t.Fatalf("原始 HTML 不应原样输出: %s", out)
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	if got := RenderMarkdown("   \n\t "); got != "" {
		t.Fatalf("空白输入应返回空字符串，实际 %q", got)
	}
}
