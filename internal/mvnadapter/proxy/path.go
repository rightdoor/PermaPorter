// Package proxy 实现核心代理引擎：Maven 仓库协议（maven-metadata.xml 元数据 +
// 版本目录构件文件）的按需懒加载与缓存。Gradle 与 Maven 共用同一仓库布局。
package proxy

import (
	"regexp"
	"strings"
)

// segRe 合法路径段（groupId/版本目录/文件名共用的字符集）。
// 首字符限字母数字即天然挡住 ".."（点开头）与 "%2e" 类编码残留（% 不在字符集内）；
// 请求 URL 在进入此处前已被 net/url 解码，编码绕过不可能出现。
var segRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)

// 路径形状上限：防御异常超长/超深请求拖垮解析与存储拼接。
const (
	maxPathSegs = 12 // g/a/v/f 最少 4 段，深 groupId 留足余量
	maxRepoPath = 512
)

// PathKind 请求路径的分类。
type PathKind int

const (
	KindInvalid PathKind = iota
	KindMeta             // maven-metadata.xml（构件级 / 版本级）
	KindFile             // 版本目录下的构件文件（jar/pom/module/asc…）
)

// ParsedFile 构件文件路径解析结果：g/…/a/v/filename。
type ParsedFile struct {
	RepoPath   string // 仓库相对路径（/ 分隔），存储与 DB 的唯一键
	GroupID    string // groupId（点分）
	ArtifactID string
	Version    string
	Filename   string
}

// checksumAlgos 支持的校验和后缀（按后缀长度降序匹配，避免 .sha1 误吃 .sha256）。
var checksumAlgos = []string{".sha512", ".sha256", ".sha1", ".md5"}

// ChecksumAlgo 校验和算法。
type ChecksumAlgo string

// ParseChecksumSuffix 剥掉路径末尾的校验和后缀，返回目标路径与算法。
// 非校验和请求返回 ok=false。
func ParseChecksumSuffix(p string) (target string, algo ChecksumAlgo, ok bool) {
	lower := strings.ToLower(p)
	for _, s := range checksumAlgos {
		if strings.HasSuffix(lower, s) {
			return p[:len(p)-len(s)], ChecksumAlgo(strings.TrimPrefix(s, ".")), true
		}
	}
	return p, "", false
}

// ParseFileRepoPath 校验并解析构件文件路径（g/…/a/v/filename）。
// filename 必须以 "<artifactId>-<version>" 开头（classifier 文件同样满足），
// 不满足说明路径与 Maven 布局不符，直接拒绝。
func ParseFileRepoPath(repoPath string) (*ParsedFile, bool) {
	repoPath = strings.Trim(repoPath, "/")
	if repoPath == "" || len(repoPath) > maxRepoPath {
		return nil, false
	}
	segs := strings.Split(repoPath, "/")
	if len(segs) < 4 || len(segs) > maxPathSegs {
		return nil, false
	}
	for _, s := range segs {
		if !segRe.MatchString(s) {
			return nil, false
		}
	}
	filename := segs[len(segs)-1]
	version := segs[len(segs)-2]
	artifactID := segs[len(segs)-3]
	group := strings.Join(segs[:len(segs)-3], ".")
	// 文件名应以 "<artifactId>-<version>" 开头；SNAPSHOT 版本目录例外：
	// 实际文件名带时间戳段（c-1.0-20240306.082314-42.pom 于 1.0-SNAPSHOT/ 下）。
	if !fileMatchesVersion(filename, artifactID, version) {
		return nil, false
	}
	return &ParsedFile{
		RepoPath:   repoPath,
		GroupID:    group,
		ArtifactID: artifactID,
		Version:    version,
		Filename:   filename,
	}, true
}

// fileMatchesVersion 文件名前缀与版本目录的一致性校验。
func fileMatchesVersion(filename, artifactID, version string) bool {
	if strings.HasPrefix(filename, artifactID+"-"+version) {
		return true
	}
	if base, ok := strings.CutSuffix(version, "-SNAPSHOT"); ok {
		return strings.HasPrefix(filename, artifactID+"-"+base+"-")
	}
	return false
}

// MetaFilename maven-metadata.xml 的文件名（构件级与版本级相同）。
const MetaFilename = "maven-metadata.xml"

// ParseMetaRepoPath 校验 maven-metadata.xml 路径（g/…/a/maven-metadata.xml 或
// g/…/a/v/maven-metadata.xml）。构件级/版本级的区分不依赖路径形状：
// XML 内容里带 groupId/artifactId，是否存在 <versions> 由解析层判定。
func ParseMetaRepoPath(repoPath string) (string, bool) {
	repoPath = strings.Trim(repoPath, "/")
	if repoPath == "" || len(repoPath) > maxRepoPath {
		return "", false
	}
	segs := strings.Split(repoPath, "/")
	if len(segs) < 3 || len(segs) > maxPathSegs {
		return "", false
	}
	for _, s := range segs {
		if !segRe.MatchString(s) {
			return "", false
		}
	}
	if segs[len(segs)-1] != MetaFilename {
		return "", false
	}
	return repoPath, true
}

// ArtifactMetaPath 构件级 metadata 的仓库相对路径（g.a → g/a）。
func ArtifactMetaPath(groupID, artifactID string) string {
	return strings.ReplaceAll(groupID, ".", "/") + "/" + artifactID + "/" + MetaFilename
}

// ValidGroupArtifact 校验 groupId / artifactId 形态（预热与管理端参数入口）。
// groupId 每段与 artifactId 都走路径段白名单，保证拼出的仓库路径安全。
func ValidGroupArtifact(groupID, artifactID string) bool {
	if groupID == "" || artifactID == "" {
		return false
	}
	if len(groupID) > 200 || len(artifactID) > 200 {
		return false
	}
	for _, seg := range strings.Split(groupID, ".") {
		if !segRe.MatchString(seg) {
			return false
		}
	}
	return segRe.MatchString(artifactID)
}
