package handlers

import (
	"testing"
)

// TestSplitPkgPath 覆盖普通包、scoped 包、tarball 路径与路径穿越。
func TestSplitPkgPath(t *testing.T) {
	cases := []struct {
		path     string
		wantPkg  string
		wantFile string
		wantOK   bool
	}{
		{"/react", "react", "", true},
		{"/@types/node", "@types/node", "", true},
		{"/vue/-/vue-3.5.13.tgz", "vue", "vue-3.5.13.tgz", true},
		{"/@scope/pkg/-/pkg-1.0.0.tgz", "@scope/pkg", "pkg-1.0.0.tgz", true},
		{"/", "", "", false},
		{"/a/b", "", "", false},
		{"/@scope", "", "", false},
		{"/@scope/a/b", "", "", false},
		{"/../etc/passwd", "", "", false},
		{"/vue/-/../x.tgz", "", "", false},
		{"/vue/-/", "", "", false},
		{"/vue/-/a/b.tgz", "", "", false},
		{"/%2e%2e/x", "", "", false},
	}
	for _, c := range cases {
		pkg, file, ok := splitPkgPath(c.path)
		if ok != c.wantOK || pkg != c.wantPkg || file != c.wantFile {
			t.Errorf("splitPkgPath(%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
				c.path, pkg, file, ok, c.wantPkg, c.wantFile, c.wantOK)
		}
	}
}

// FuzzSplitPkgPath 保证任意输入都不会 panic，且成功解析出的包名一定符合 pkgNameRe。
func FuzzSplitPkgPath(f *testing.F) {
	seeds := []string{"/react", "/@types/node", "/vue/-/vue-3.5.13.tgz", "/../x", "/@a/b/-/c.tgz", ""}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		pkg, file, ok := splitPkgPath(p)
		if !ok {
			return
		}
		if !pkgNameRe.MatchString(pkg) {
			t.Fatalf("解析出的包名不符合规则: %q（输入 %q）", pkg, p)
		}
		if file != "" && (file == "" || containsSlash(file)) {
			t.Fatalf("文件名不应包含斜杠: %q（输入 %q）", file, p)
		}
	})
}

func containsSlash(s string) bool {
	for _, r := range s {
		if r == '/' {
			return true
		}
	}
	return false
}

func TestValidPkgName(t *testing.T) {
	valid := []string{"react", "@types/node", "a.b_c-d~e"}
	// "." / ".." 与含这些段的 scoped 名能通过正则但会拼出存储树外路径，必须整体拒绝
	invalid := []string{"", ".", "..", "@x/..", "@x/.", "a/..", "../x", "a/b", "@scope", "@scope/a/b", "a b"}
	for _, n := range valid {
		if !validPkgName(n) {
			t.Errorf("%q 应被视为合法包名", n)
		}
	}
	for _, n := range invalid {
		if validPkgName(n) {
			t.Errorf("%q 应被视为非法包名", n)
		}
	}
}
