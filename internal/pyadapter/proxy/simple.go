// PEP 503 Simple Repository API 的协议工具：项目名规范化、锚点页解析与生成。
package proxy

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"permaporter/internal/core/utils"
	"permaporter/internal/pyadapter/models"
)

// pyFileRe 合法发行文件名（wheel/sdist 字符集）。用于 /packages/<filename>
// 请求与 DB 落库前的防御：挡住路径分隔符与任意垃圾输入。
var pyFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+!-]{0,200}$`)

// ValidFilename 判断发行文件名是否合法。
func ValidFilename(name string) bool { return pyFileRe.MatchString(name) }

// projectSepRe PEP 503 项目名中可折叠的分隔符组（[-_.]+）。
// 提升为包级变量：每次调用编译正则会拖慢高频的锚点页/文件名请求路径。
var projectSepRe = regexp.MustCompile(`[-_.]+`)

// NormalizeProjectName PEP 503 项目名规范化：连续的 -_. 折叠为单个 - 并转小写。
func NormalizeProjectName(name string) string {
	return strings.ToLower(projectSepRe.ReplaceAllString(strings.TrimSpace(name), "-"))
}

// ValidProjectName 判断规范化名是否合法（PEP 503：[a-z0-9] 加分隔符）。
var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,200}$`)

func ValidProjectName(name string) bool { return projectNameRe.MatchString(name) }

// sdistVersionRe sdist 文件名中的版本段：紧跟在最后一个 "-数字" 之后。
var sdistVersionRe = regexp.MustCompile(`-([A-Za-z0-9.!+]+)\.(?:tar\.gz|tar\.bz2|zip)$`)

// VersionFromFilename 从发行文件名解析版本号。
// wheel: <dist>-<version>-<lang>-<abi>-<plat>.whl（第 2 段即版本）；
// sdist: <name>-<version>.tar.gz/.zip（取最后一个以数字开头的版本段）。
// 解析不出返回空串（记录仍保留，仅不参与 latest 版本比较）。
func VersionFromFilename(filename string) string {
	if strings.HasSuffix(filename, ".whl") {
		parts := strings.Split(strings.TrimSuffix(filename, ".whl"), "-")
		if len(parts) >= 5 {
			return parts[1]
		}
		return ""
	}
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".zip"} {
		if strings.HasSuffix(filename, ext) {
			if m := sdistVersionRe.FindStringSubmatch(filename); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// ParseSimpleIndex 解析上游 /simple/<project>/ 锚点页为项目与文件清单。
// 锚点 href 的最后一个路径段即发行文件名，#sha256= 片段为内容摘要；
// data-requires-python 属性为 Python 版本约束（解析器已解码 HTML 实体）。
// pageURL 为本次抓取该锚点页的上游地址（锚点页自身 URL，含尾斜杠语义）：
// 相对锚点（阿里云镜像形态 ../../packages/<hash>/<file>）据此解析为绝对 URL
// 后存入 SourceURL；绝对锚点不受影响，非法 href 仍跳过。
func ParseSimpleIndex(raw []byte, projectName, pageURL string) (*models.Project, []models.File, error) {
	var base *url.URL
	if u, err := url.Parse(pageURL); err == nil && u.Scheme != "" {
		base = u
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("解析锚点页失败: %w", err)
	}
	now := dbTimeFormatNow()
	p := &models.Project{ProjectName: NormalizeProjectName(projectName), DisplayName: projectName, MetaCachedAt: now}
	var files []models.File
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			href := nodeAttr(n, "href")
			if href != "" {
				f := parseAnchor(p.ProjectName, href, nodeAttr(n, "data-requires-python"), now, base)
				if f != nil {
					files = append(files, *f)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// latest 版本：对可解析版本号的文件取最高（口径与 npm 侧一致，比较失败按字典序兜底）
	for i := range files {
		if files[i].Version == "" {
			continue
		}
		if p.LatestVersion == "" || utils.VersionCompare(files[i].Version, p.LatestVersion) > 0 {
			p.LatestVersion = files[i].Version
		}
	}
	return p, files, nil
}

// parseAnchor 从单个锚点 href 解析发行文件记录；无法取得文件名时返回 nil。
// base 非空时 href（相对或绝对均兼容）解析为绝对 URL 后存入 SourceURL。
func parseAnchor(projectName, href, requiresPython, now string, base *url.URL) *models.File {
	// 分离 #sha256= 片段
	frag := ""
	if i := strings.IndexByte(href, '#'); i >= 0 {
		frag = href[i+1:]
		href = href[:i]
	}
	u, err := url.Parse(href)
	if err != nil || u.Path == "" {
		return nil
	}
	seg := u.Path
	if i := strings.LastIndexByte(seg, '/'); i >= 0 {
		seg = seg[i+1:]
	}
	filename, derr := url.PathUnescape(seg)
	if derr != nil || !ValidFilename(filename) {
		return nil
	}
	sha := ""
	if strings.HasPrefix(frag, "sha256=") {
		sha = strings.TrimPrefix(frag, "sha256=")
	}
	source := href
	if base != nil {
		source = base.ResolveReference(u).String()
	}
	return &models.File{
		ProjectID:      0, // 由 UpsertFiles 的调用方填入
		Version:        VersionFromFilename(filename),
		Filename:       filename,
		SHA256:         strings.ToLower(sha),
		SourceURL:      source,
		RequiresPython: requiresPython,
		UploadTime:     "",
		CachedAt:       now,
		FilePath:       "",
	}
}

func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// GenerateProjectPage 生成 /simple/<project>/ 锚点页 HTML。
// 锚点使用绝对地址（base 为本次请求还原的 Simple 端点基地址）——
// 页内容由 py_files 表按请求生成、不落盘，因此无需哨兵改写。
func GenerateProjectPage(displayName string, files []FileItemView, base string) []byte {
	var b bytes.Buffer
	b.WriteString(`<!DOCTYPE html>
<html><head><meta name="pypi:repository-version" content="1.0"><title>Links for ` + htmlEscape(displayName) + `</title></head><body><h1>Links for ` + htmlEscape(displayName) + `</h1>
`)
	for _, f := range files {
		href := base + "/packages/" + f.Filename
		b.WriteString(`<a href="` + htmlEscape(href))
		if f.SHA256 != "" {
			b.WriteString(`#sha256=` + f.SHA256)
		}
		if f.RequiresPython != "" {
			b.WriteString(`" data-requires-python="` + htmlEscape(f.RequiresPython))
		}
		b.WriteString(`">` + htmlEscape(f.Filename) + `</a><br/>`)
	}
	b.WriteString(`</body></html>`)
	return b.Bytes()
}

// GenerateSimpleIndexHTML 生成 /simple/ 根索引（本地已收录项目清单）。
func GenerateSimpleIndexHTML(names []string) []byte {
	var b bytes.Buffer
	b.WriteString(`<!DOCTYPE html>
<html><head><meta name="pypi:repository-version" content="1.0"><title>Simple index</title></head><body>
`)
	for _, n := range names {
		b.WriteString(`<a href="` + (&url.URL{Path: n + "/"}).String() + `">` + htmlEscape(n) + `</a><br/>`)
	}
	b.WriteString(`</body></html>`)
	return b.Bytes()
}

// FileItemView 锚点页生成所需的文件视图（字段来自 py_files）。
type FileItemView struct {
	Filename       string
	SHA256         string
	RequiresPython string
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}
