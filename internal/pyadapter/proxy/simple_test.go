// 锚点页解析与回源候选组装的单元测试（相对锚点解析为主）。
package proxy

import (
	"testing"

	"permaporter/internal/pyadapter/database"
	"permaporter/internal/pyadapter/models"
)

// TestParseSimpleIndexResolvesRelativeAnchors 相对锚点（阿里云镜像形态）必须
// 以锚点页自身 URL 为基准解析为绝对地址后存入 SourceURL；绝对锚点不受影响；
// 非法 href 仍被跳过。
func TestParseSimpleIndexResolvesRelativeAnchors(t *testing.T) {
	const pageURL = "http://mirrors.aliyun.com/pypi/simple/six/"
	raw := []byte(`<!DOCTYPE html><html><body>
		<a href="../../packages/0e/f9/66471f9b/six-0.9.0.tar.gz#sha256=14fd1ed3dd0e1a46cc53b8fc890b5a3b11737515aeb7f42c3af9f38e8d8975d7">six-0.9.0.tar.gz</a><br/>
		<a href="../packages/4e/02/f08545a9/six-1.0.0.tar.gz">six-1.0.0.tar.gz</a><br/>
		<a href="six-1.16.0-py2.py3-none-any.whl#sha256=1e61c37477a1626458e36f7b1d82aa5c9b094db480289faf9739d5f84e573559">six-1.16.0-py2.py3-none-any.whl</a><br/>
		<a href="https://files.pythonhosted.org/packages/aa/bb9f38/six-1.17.0.tar.gz#sha256=a8d1c8a6d97b1d0f8d7c8d6b6f04b4c9e2f1a3b5d7e9c0f2a4b6d8e0c1f3a5">six-1.17.0.tar.gz</a><br/>
		<a href="javascript:alert(1)">js</a><br/>
		<a href="#sha256=abc">frag-only</a><br/>
		<a href="../../packages/0e/bad~name.tgz">bad-name</a><br/>
	</body></html>`)
	_, files, err := ParseSimpleIndex(raw, "six", pageURL)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	byName := map[string]models.File{}
	for _, f := range files {
		byName[f.Filename] = f
	}
	// ../../packages/…：相对锚点页 .../pypi/simple/six/ 上两级 → .../pypi/packages/…
	if f := byName["six-0.9.0.tar.gz"]; f.SourceURL != "http://mirrors.aliyun.com/pypi/packages/0e/f9/66471f9b/six-0.9.0.tar.gz" {
		t.Fatalf("../../ 锚点解析错误: %s", f.SourceURL)
	} else if f.SHA256 != "14fd1ed3dd0e1a46cc53b8fc890b5a3b11737515aeb7f42c3af9f38e8d8975d7" {
		t.Fatalf("sha256 片段丢失: %s", f.SHA256)
	}
	// ../packages/…：相对锚点页上一级 → .../pypi/simple/packages/…
	if f := byName["six-1.0.0.tar.gz"]; f.SourceURL != "http://mirrors.aliyun.com/pypi/simple/packages/4e/02/f08545a9/six-1.0.0.tar.gz" {
		t.Fatalf("../ 锚点解析错误: %s", f.SourceURL)
	}
	// 裸文件名：直接相对锚点页本身
	if f := byName["six-1.16.0-py2.py3-none-any.whl"]; f.SourceURL != "http://mirrors.aliyun.com/pypi/simple/six/six-1.16.0-py2.py3-none-any.whl" {
		t.Fatalf("裸文件名锚点解析错误: %s", f.SourceURL)
	}
	// 绝对锚点（PyPI 形态）：原样保留
	if f := byName["six-1.17.0.tar.gz"]; f.SourceURL != "https://files.pythonhosted.org/packages/aa/bb9f38/six-1.17.0.tar.gz" {
		t.Fatalf("绝对锚点不应被改写: %s", f.SourceURL)
	}
	// 非法锚点：非文件路径（javascript:）、纯片段、非法文件名均跳过
	if len(files) != 4 {
		t.Fatalf("应只保留 4 个合法锚点，实际 %d: %+v", len(files), files)
	}
}

// TestParseSimpleIndexWithoutBaseURL 页 URL 缺失（异常场景）退回旧行为：原样保存。
func TestParseSimpleIndexWithoutBaseURL(t *testing.T) {
	raw := []byte(`<a href="../../packages/x/six-0.9.0.tar.gz">six-0.9.0.tar.gz</a>`)
	_, files, err := ParseSimpleIndex(raw, "six", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(files) != 1 || files[0].SourceURL != "../../packages/x/six-0.9.0.tar.gz" {
		t.Fatalf("无基准 URL 应原样保存: %+v", files)
	}
}

// TestFileDownloadCandidates 回源候选顺序：存量相对 SourceURL 先按当前上游
// 锚点页解析为绝对地址、排在原样值之前；镜像根猜测兜底最后。
func TestFileDownloadCandidates(t *testing.T) {
	legacy := &database.FileWithProject{
		File:        models.File{Filename: "six-0.9.0.tar.gz", SourceURL: "../../packages/0e/f9/66471f9b/six-0.9.0.tar.gz"},
		ProjectName: "six",
	}
	got := fileDownloadCandidates(&models.Settings{UpstreamURL: "http://mirrors.aliyun.com/pypi/simple/"}, legacy)
	want := []string{
		"http://mirrors.aliyun.com/pypi/packages/0e/f9/66471f9b/six-0.9.0.tar.gz",
		"../../packages/0e/f9/66471f9b/six-0.9.0.tar.gz",
		"http://mirrors.aliyun.com/pypi/packages/six-0.9.0.tar.gz",
	}
	if len(got) != len(want) {
		t.Fatalf("候选数量不符: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("候选[%d] 应为 %s，实际 %s（全部: %v）", i, want[i], got[i], got)
		}
	}

	// 新解析落库的绝对地址：不加解析前缀，直接原样在前
	abs := &database.FileWithProject{
		File:        models.File{Filename: "six-1.17.0.tar.gz", SourceURL: "https://files.pythonhosted.org/packages/aa/six-1.17.0.tar.gz"},
		ProjectName: "six",
	}
	got = fileDownloadCandidates(&models.Settings{UpstreamURL: "http://mirrors.aliyun.com/pypi/simple/"}, abs)
	if got[0] != "https://files.pythonhosted.org/packages/aa/six-1.17.0.tar.gz" {
		t.Fatalf("绝对地址应排在首位: %v", got)
	}
	if len(got) != 2 || got[1] != "http://mirrors.aliyun.com/pypi/packages/six-1.17.0.tar.gz" {
		t.Fatalf("绝对地址后应仅剩镜像根兜底: %v", got)
	}
}
