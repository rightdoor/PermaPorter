package proxy

import (
	"errors"
	"testing"
)

func TestParseProtocolPaths(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		want    *Request
		wantErr bool // 参数非法（400）
		notProt bool // 非协议路径
	}{
		{"list", "/github.com/gin-gonic/gin/@v/list", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin", Kind: KindList}, false, false},
		{"latest", "/github.com/gin-gonic/gin/@latest", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin", Kind: KindLatest}, false, false},
		{"info", "/github.com/gin-gonic/gin/@v/v1.10.0.info", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin",
			Version: "v1.10.0", EscapedVersion: "v1.10.0", Kind: KindInfo}, false, false},
		{"mod", "/github.com/gin-gonic/gin/@v/v1.10.0.mod", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin",
			Version: "v1.10.0", EscapedVersion: "v1.10.0", Kind: KindMod}, false, false},
		{"zip", "/github.com/gin-gonic/gin/@v/v1.10.0.zip", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin",
			Version: "v1.10.0", EscapedVersion: "v1.10.0", Kind: KindZip}, false, false},
		{"ziphash", "/github.com/gin-gonic/gin/@v/v1.10.0.ziphash", &Request{
			Module: "github.com/gin-gonic/gin", EscapedModule: "github.com/gin-gonic/gin",
			Version: "v1.10.0", EscapedVersion: "v1.10.0", Kind: KindZiphash}, false, false},
		{"escaped-uppercase", "/github.com/!burnt!sushi/toml/@v/list", &Request{
			Module: "github.com/BurntSushi/toml", EscapedModule: "github.com/!burnt!sushi/toml", Kind: KindList}, false, false},
		{"escaped-version", "/github.com/x/mod/@v/v0.5.0!beta.info", &Request{
			Module: "github.com/x/mod", EscapedModule: "github.com/x/mod",
			Version: "v0.5.0Beta", EscapedVersion: "v0.5.0!beta", Kind: KindInfo}, false, false},
		{"nested-module", "/gopkg.in/yaml.v3/@v/list", &Request{
			Module: "gopkg.in/yaml.v3", EscapedModule: "gopkg.in/yaml.v3", Kind: KindList}, false, false},
		{"pseudo-version-zip", "/example.com/m/@v/v0.0.0-20200101000000-abcdef123456.zip", &Request{
			Module: "example.com/m", EscapedModule: "example.com/m",
			Version:        "v0.0.0-20200101000000-abcdef123456",
			EscapedVersion: "v0.0.0-20200101000000-abcdef123456", Kind: KindZip}, false, false},

		{"non-protocol", "/github.com/gin-gonic/gin", nil, false, true},
		{"non-protocol-subpkg", "/github.com/gin-gonic/gin/subdir", nil, false, true},
		{"empty", "/", nil, false, true},
		{"empty-module", "/@v/list", nil, true, false},
		{"bad-module", "/../etc/passwd/@v/list", nil, true, false},
		{"invalid-module", "/notadomain/@v/list", nil, true, false},
		{"unknown-suffix", "/github.com/x/@v/v1.0.0.exe", nil, true, false},
		{"path-traversal-version", "/github.com/x/@v/../../evil.zip", nil, true, false},
		{"non-canonical-zip", "/github.com/x/@v/master.zip", nil, true, false},
		{"non-canonical-mod", "/github.com/x/@v/abc123.mod", nil, true, false},
		{"info-allows-branch", "/github.com/x/@v/master.info", &Request{
			Module: "github.com/x", EscapedModule: "github.com/x",
			Version: "master", EscapedVersion: "master", Kind: KindInfo}, false, false},
		{"bare-@v", "/github.com/x/@v", nil, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := Parse(tc.path)
			if tc.notProt {
				if !errors.Is(err, ErrNotProtocol) {
					t.Fatalf("期望非协议路径，实际 req=%v err=%v", req, err)
				}
				return
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望参数错误，实际解析成功: %+v", req)
				}
				if errors.Is(err, ErrNotProtocol) {
					t.Fatalf("期望参数错误，实际却是非协议路径")
				}
				return
			}
			if err != nil {
				t.Fatalf("期望解析成功，实际错误: %v", err)
			}
			if req.Module != tc.want.Module || req.EscapedModule != tc.want.EscapedModule ||
				req.Version != tc.want.Version || req.EscapedVersion != tc.want.EscapedVersion ||
				req.Kind != tc.want.Kind {
				t.Fatalf("解析结果不符:\n got %+v\nwant %+v", req, *tc.want)
			}
		})
	}
}

func TestNormalizeList(t *testing.T) {
	in := "v1.2.0\n\nv1.0.0\nv0.1.0+build\nv1.0.0-rc.1\nv0.0.0-20200101000000-abcdef123456\nv1.0.0\tnotsemver\n"
	got := NormalizeList([]byte(in))
	want := []string{"v0.1.0+build", "v1.0.0-rc.1", "v1.0.0", "v1.2.0"}
	if len(got) != len(want) {
		t.Fatalf("过滤结果数量不符: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序/过滤结果不符: got %v want %v", got, want)
		}
	}
}

func TestDeriveLatest(t *testing.T) {
	if got := deriveLatest([]string{"v1.0.0", "v1.1.0-rc.1", "v1.2.0"}); got != "v1.2.0" {
		t.Fatalf("应取最高 release: %s", got)
	}
	if got := deriveLatest([]string{"v1.0.0-rc.1", "v1.0.0-rc.2"}); got != "v1.0.0-rc.2" {
		t.Fatalf("无 release 时应取最高 prerelease: %s", got)
	}
	if got := deriveLatest(nil); got != "" {
		t.Fatalf("空列表应返回空: %q", got)
	}
}

func TestStorageFilePathContainment(t *testing.T) {
	if _, err := storageFilePath("/data/storage", "modules", "github.com/x/@v", "list"); err != nil {
		t.Fatalf("合法路径不应报错: %v", err)
	}
	if _, err := storageFilePath("/data/storage", "modules", "../../etc", "passwd"); err == nil {
		t.Fatal("越界路径应报错")
	}
}

func TestValidSumdbName(t *testing.T) {
	for _, ok := range []string{"sum.golang.org", "sum.golang.google.cn", "a.b"} {
		if !ValidSumdbName(ok) {
			t.Errorf("%s 应合法", ok)
		}
	}
	for _, bad := range []string{"", "..", "a..b", "SUM.golang.org", "sum golang org", "x/y"} {
		if ValidSumdbName(bad) {
			t.Errorf("%s 应非法", bad)
		}
	}
}

func TestParseGoModExtraction(t *testing.T) {
	src := `module github.com/gin-gonic/gin

go 1.21

require (
	github.com/bytedance/sonic v1.11.6
	github.com/stretchr/testify v1.9.0 // indirect
)
`
	reqs, goVer := ParseGoMod([]byte(src))
	if goVer != "1.21" {
		t.Fatalf("go 版本解析不符: %q", goVer)
	}
	if reqs == "" {
		t.Fatal("require 解析结果不应为空")
	}
	if !contains(reqs, `"path":"github.com/bytedance/sonic"`) ||
		!contains(reqs, `"indirect":true`) {
		t.Fatalf("require 解析结果不符: %s", reqs)
	}
	// 非法 go.mod 不应报错，返回空
	if r, g := ParseGoMod([]byte("not a go.mod")); r != "" || g != "" {
		t.Fatalf("非法 go.mod 应返回空: %q %q", r, g)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestSummarize(t *testing.T) {
	md := "# Gin\n\n[![badge](https://badge)](https://x)\n\nGin is a HTTP web framework written in Go. It features a Martini-like API."
	s := Summarize(md)
	if s == "" || len([]rune(s)) > 200 {
		t.Fatalf("摘要长度异常: %q", s)
	}
	if !contains(s, "Gin is a HTTP web framework") {
		t.Fatalf("摘要应保留正文: %q", s)
	}
}
