package proxy

import (
	"strings"
	"testing"
)

const demoPackument = `{
  "name": "demo",
  "dist-tags": {"latest": "1.0.0"},
  "versions": {
    "1.0.0": {"version": "1.0.0", "dist": {"tarball": "https://registry.npmjs.org/demo/-/demo-1.0.0.tgz", "integrity": "sha512-abc"}},
    "1.1.0": {"version": "1.1.0", "dist": {"tarball": "https://registry.npmjs.org/demo/-/demo-1.1.0.tgz"}}
  }
}`

// TestSentinelizeAndApplyBase 校验「落盘写哨兵 + 响应换 base」的往返正确性。
func TestSentinelizeAndApplyBase(t *testing.T) {
	stored, err := SentinelizePackument([]byte(demoPackument), "demo")
	if err != nil {
		t.Fatalf("哨兵化失败: %v", err)
	}
	if !strings.Contains(string(stored), sentinelBase) {
		t.Fatalf("落盘内容应包含哨兵地址，实际: %s", stored)
	}
	if strings.Contains(string(stored), "registry.npmjs.org") {
		t.Fatalf("落盘内容不应残留上游地址: %s", stored)
	}

	out, ok := ApplyBase(stored, "https://mirror.local:9090")
	if !ok {
		t.Fatal("ApplyBase 应识别哨兵地址")
	}
	if strings.Contains(string(out), sentinelBase) {
		t.Fatalf("替换后不应残留哨兵: %s", out)
	}
	if !strings.Contains(string(out), "https://mirror.local:9090/demo/-/demo-1.1.0.tgz") {
		t.Fatalf("tarball 地址未正确改写: %s", out)
	}
	// 尾斜杠不应产生双斜杠
	if strings.Contains(string(out), "9090//demo") {
		t.Fatalf("base 尾斜杠未处理: %s", out)
	}
}

// TestApplyBaseFallsBackForLegacyCache 历史缓存（无哨兵）应返回 ok=false 让调用方走 JSON 改写。
func TestApplyBaseFallsBackForLegacyCache(t *testing.T) {
	raw := []byte(demoPackument)
	if _, ok := ApplyBase(raw, "http://localhost:9090"); ok {
		t.Fatal("不含哨兵的历史缓存应返回 ok=false")
	}
	out, err := RewritePackument(raw, "demo", "http://localhost:9090")
	if err != nil {
		t.Fatalf("改写失败: %v", err)
	}
	if !strings.Contains(string(out), "http://localhost:9090/demo/-/demo-1.0.0.tgz") {
		t.Fatalf("历史缓存改写结果不正确: %s", out)
	}
}

// TestRewritePackumentScopedPackage 校验 scoped 包的 tarball 路径（文件名取 basename）。
func TestRewritePackumentScopedPackage(t *testing.T) {
	raw := `{"versions":{"1.0.0":{"dist":{"tarball":"https://x/types-node-1.0.0.tgz"}}}}`
	out, err := RewritePackument([]byte(raw), "@types/node", "http://h")
	if err != nil {
		t.Fatalf("改写失败: %v", err)
	}
	if !strings.Contains(string(out), "http://h/@types/node/-/node-1.0.0.tgz") {
		t.Fatalf("scoped 包路径不正确: %s", out)
	}
}

func TestParseIntegrity(t *testing.T) {
	// sha512("hello") 的 base64
	const hello512 = "m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="
	cases := []struct {
		name    string
		input   string
		wantOK  bool
		wantLen int
	}{
		{"sha512 正常", "sha512-" + hello512, true, 64},
		{"多算法取首个可用", "md5-xxx sha512-" + hello512, true, 64},
		{"未知算法", "md5-abc", false, 0},
		{"空串", "", false, 0},
		{"base64 非法", "sha512-***", false, 0},
		{"长度不符", "sha512-YWJj", false, 0},
	}
	for _, c := range cases {
		algo, want, ok := parseIntegrity(c.input)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, 期望 %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && (len(want) != c.wantLen || newIntegrityHash(algo) == nil) {
			t.Errorf("%s: 摘要长度 = %d（期望 %d），算法 = %s", c.name, len(want), c.wantLen, algo)
		}
	}
}

func TestVersionFromFilename(t *testing.T) {
	cases := []struct{ pkg, file, want string }{
		{"demo", "demo-1.2.3.tgz", "1.2.3"},
		{"@types/node", "node-20.1.0.tgz", "20.1.0"},
		{"demo", "other-1.0.0.tgz", ""},
		{"demo", "demo-1.0.0.zip", ""},
	}
	for _, c := range cases {
		if got := VersionFromFilename(c.pkg, c.file); got != c.want {
			t.Errorf("VersionFromFilename(%q,%q) = %q, 期望 %q", c.pkg, c.file, got, c.want)
		}
	}
}
