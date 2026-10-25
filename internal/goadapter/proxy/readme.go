package proxy

import (
	"archive/zip"
	"io"
	"regexp"
	"strings"
)

// readmeCandidates 按优先级排列的 README 文件名（根目录内大小写不敏感匹配）。
var readmeCandidates = []string{"readme.md", "readme.txt", "readme.rst", "readme"}

// ExtractZipReadme 从模块 zip 的根目录提取 README 原文。
// 模块 zip 的全部文件都位于单一根目录 <module>@<version>/ 下（该段目录名含 @，
// 且模块路径本身可含斜杠，因此以"含 @ 的段"定位根目录，而非首段）；
// 提取失败（无 README/读取错误）返回错误，调用方应忽略而不影响 zip 缓存。
func ExtractZipReadme(zipPath string, maxBytes int64) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	prefix := ""
	byLower := map[string]*zip.File{}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if prefix == "" {
			root, ok := zipRootPrefix(f.Name)
			if !ok {
				continue
			}
			prefix = root
		}
		rest := strings.TrimPrefix(f.Name, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			continue
		}
		byLower[strings.ToLower(rest)] = f
	}
	if prefix == "" {
		return "", errReadmeNotFound
	}
	for _, name := range readmeCandidates {
		f, ok := byLower[name]
		if !ok {
			continue
		}
		if f.UncompressedSize64 > uint64(maxBytes) {
			continue // 超大 README 跳过，避免占用数据库与内存
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
		rc.Close()
		if err != nil || int64(len(data)) > maxBytes {
			continue
		}
		return string(data), nil
	}
	return "", errReadmeNotFound
}

// zipRootPrefix 从 zip 条目路径定位模块根目录前缀（<...>/<seg 含 @>/<）。
func zipRootPrefix(name string) (string, bool) {
	segs := strings.Split(name, "/")
	for i, seg := range segs {
		if strings.Contains(seg, "@") {
			if i == len(segs)-1 {
				return "", false // 根目录本身不是文件
			}
			return strings.Join(segs[:i+1], "/") + "/", true
		}
	}
	return "", false
}

// errReadmeNotFound zip 内没有可用的 README。
var errReadmeNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "zip 内无 README" }

// 去除 Markdown 标记的规则集合（摘要生成用，只做轻量清洗）。
var (
	mdBadgeRe    = regexp.MustCompile(`\[!\[[^\]]*\]\([^)]*\)\]\([^)]*\)`)
	mdImageRe    = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdCodeFence  = regexp.MustCompile("(?s)```.*?```")
	mdInlineCode = regexp.MustCompile("`([^`]*)`")
	mdHeadingRe  = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	mdEmphRe     = regexp.MustCompile(`[*_]{1,3}([^*_]+)[*_]{1,3}`)
	htmlTagRe    = regexp.MustCompile(`<[^>]+>`)
	wsRe         = regexp.MustCompile(`\s+`)
)

// Summarize 生成 README 的纯文本摘要（约 200 字符，列表/搜索展示用）。
func Summarize(readme string) string {
	s := mdBadgeRe.ReplaceAllString(readme, "")
	s = mdCodeFence.ReplaceAllString(s, " ")
	s = mdImageRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = mdInlineCode.ReplaceAllString(s, "$1")
	s = mdHeadingRe.ReplaceAllString(s, "")
	s = mdEmphRe.ReplaceAllString(s, "$1")
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = wsRe.ReplaceAllString(strings.TrimSpace(s), " ")
	runes := []rune(s)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return strings.TrimSpace(string(runes))
}
