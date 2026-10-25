package utils

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

var (
	// README 里的远程图片：懒加载 + 不发送 Referer（避免泄露内网访问行为）
	imgTagRe = regexp.MustCompile(`<img\s`)
	// 外链：新窗口打开并切断 opener 引用
	extLinkRe = regexp.MustCompile(`<a\s+href="(https?://[^"]*)"`)
)

// RenderMarkdown 服务端将 README Markdown 渲染为安全的 HTML。
// goldmark 默认不输出原始 HTML（XSS 风险可控）；这里再给图片/外链补上
// 懒加载与安全属性，避免首屏被第三方图片拖慢、以及泄露访问来源。
func RenderMarkdown(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(hardenMarkup(buf.String()))
}

// hardenMarkup 为渲染结果中的 img/a 标签补充属性（只做字符串替换，不解析 DOM）。
func hardenMarkup(s string) string {
	s = imgTagRe.ReplaceAllString(s, `<img loading="lazy" decoding="async" referrerpolicy="no-referrer" `)
	s = extLinkRe.ReplaceAllString(s, `<a href="$1" target="_blank" rel="noopener noreferrer"`)
	return s
}
