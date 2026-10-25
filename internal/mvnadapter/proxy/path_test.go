package proxy

import (
	"testing"
)

// TestParseFileRepoPath 构件文件路径解析：合法布局、坐标拆分与拒绝项。
func TestParseFileRepoPath(t *testing.T) {
	okCases := []struct {
		path            string
		group, artifact string
		version, file   string
	}{
		{"com/example/demo/1.0.0/demo-1.0.0.jar", "com.example", "demo", "1.0.0", "demo-1.0.0.jar"},
		{"com/google/guava/guava/33.0.0-jre/guava-33.0.0-jre.pom", "com.google.guava", "guava", "33.0.0-jre", "guava-33.0.0-jre.pom"},
		{"junit/junit/4.13.2/junit-4.13.2-sources.jar", "junit", "junit", "4.13.2", "junit-4.13.2-sources.jar"},
		{"a/b/c/1.0-SNAPSHOT/c-1.0-20240306.082314-42.pom", "a.b", "c", "1.0-SNAPSHOT", "c-1.0-20240306.082314-42.pom"},
	}
	for _, c := range okCases {
		pf, ok := ParseFileRepoPath(c.path)
		if !ok {
			t.Errorf("应解析成功: %s", c.path)
			continue
		}
		if pf.GroupID != c.group || pf.ArtifactID != c.artifact || pf.Version != c.version || pf.Filename != c.file {
			t.Errorf("解析不符: %+v", pf)
		}
	}

	badCases := []string{
		"", "/", "/demo.jar", "demo/demo.jar", "com/demo/1.0/demo.jar", // 段数不足
		"com/example/demo/1.0.0/other-1.0.0.jar", // 文件名不以 artifactId-version 开头
		"com/example/demo/1.0.0/..",              // 路径穿越（点开头段）
		"com/exa%20mple/demo/1.0.0/demo-1.0.0.jar",
		"com/example/demo/1.0.0/../2.0.0/demo-2.0.0.jar",
		"com/example/demo/1.0.0/Demo~1.0.0.jar", // 非法字符
		"com/example/demo/1.0.0/",               // 空文件名
	}
	for _, p := range badCases {
		if _, ok := ParseFileRepoPath(p); ok {
			t.Errorf("应拒绝: %q", p)
		}
	}
}

// TestParseMetaRepoPath metadata 路径校验（构件级与版本级均合法）。
func TestParseMetaRepoPath(t *testing.T) {
	okCases := []string{
		"/com/example/demo/maven-metadata.xml",
		"/com/example/demo/1.0-SNAPSHOT/maven-metadata.xml",
		"/junit/junit/maven-metadata.xml",
	}
	for _, p := range okCases {
		if _, ok := ParseMetaRepoPath(p); !ok {
			t.Errorf("应解析成功: %s", p)
		}
	}
	badCases := []string{
		"", "/", "/maven-metadata.xml", "/demo/maven-metadata.xml", // 段数不足
		"/com/example/demo/maven-metadata.xml/extra", // 末段不是 metadata 文件名
		"/com/exa%20mple/demo/maven-metadata.xml",
		"/com/example/../demo/maven-metadata.xml",
	}
	for _, p := range badCases {
		if _, ok := ParseMetaRepoPath(p); ok {
			t.Errorf("应拒绝: %q", p)
		}
	}
}

// TestArtifactMetaPath 构件级 metadata 路径拼接。
func TestArtifactMetaPath(t *testing.T) {
	if got := ArtifactMetaPath("com.google.guava", "guava"); got != "com/google/guava/guava/maven-metadata.xml" {
		t.Fatalf("拼接不符: %s", got)
	}
}

// TestParseChecksumSuffix 校验和后缀剥离（长后缀优先）。
func TestParseChecksumSuffix(t *testing.T) {
	cases := []struct {
		in     string
		target string
		algo   ChecksumAlgo
		ok     bool
	}{
		{"/a/b/c/1.0/c-1.0.jar.sha1", "/a/b/c/1.0/c-1.0.jar", "sha1", true},
		{"/a/b/c/1.0/c-1.0.jar.sha256", "/a/b/c/1.0/c-1.0.jar", "sha256", true},
		{"/a/b/maven-metadata.xml.sha1", "/a/b/maven-metadata.xml", "sha1", true},
		{"/a/b/c/1.0/c-1.0.jar", "/a/b/c/1.0/c-1.0.jar", "", false},
	}
	for _, c := range cases {
		target, algo, ok := ParseChecksumSuffix(c.in)
		if ok != c.ok || target != c.target || algo != c.algo {
			t.Errorf("ParseChecksumSuffix(%q) = %q %q %v", c.in, target, algo, ok)
		}
	}
}

// TestValidGroupArtifact 坐标校验。
func TestValidGroupArtifact(t *testing.T) {
	if !ValidGroupArtifact("com.google.guava", "guava") {
		t.Error("合法坐标应通过")
	}
	for _, g := range []string{"", ".lead", "trail.", "a..b", "a b", "../x"} {
		if ValidGroupArtifact(g, "demo") {
			t.Errorf("非法 groupId 应拒绝: %q", g)
		}
	}
	for _, a := range []string{"", ".hidden", "de mo", "../../etc"} {
		if ValidGroupArtifact("com.example", a) {
			t.Errorf("非法 artifactId 应拒绝: %q", a)
		}
	}
}
