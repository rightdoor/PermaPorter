package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// sentinelBase 落盘时替换 dist.tarball 的占位站点根地址：与请求方 host 无关。
// 响应时只需一次字节替换即可得到正确地址，省去每请求反序列化整份 JSON
// （react、@types/node 这类 packument 单份 1~3MB，解析成本很高）。
const sentinelBase = "http://__STEAD_BASE__"

var sentinelBytes = []byte(sentinelBase)

// RewritePackument 将上游 packument 中所有 dist.tarball 地址重写为本地代理地址。
// 仅用于历史缓存（落盘时还是上游原始地址）与异常 JSON 的兜底路径；
// 新缓存落盘时已用 SentinelizePackument 写成哨兵地址。
func RewritePackument(raw []byte, pkgName, baseURL string) ([]byte, error) {
	return rewriteTarballs(raw, pkgName, baseURL)
}

// SentinelizePackument 把全部 dist.tarball 改写为与 host 无关的哨兵地址，用于落盘缓存。
// 落盘内容因此可在任意 Host / 协议下复用，不必按 host 分别存储。
func SentinelizePackument(raw []byte, pkgName string) ([]byte, error) {
	return rewriteTarballs(raw, pkgName, sentinelBase)
}

func rewriteTarballs(raw []byte, pkgName, baseURL string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, err
	}
	versions, ok := doc["versions"].(map[string]any)
	if !ok || len(versions) == 0 {
		return raw, nil
	}
	base := strings.TrimSuffix(baseURL, "/")
	name := path.Base(pkgName)
	for ver, rawV := range versions {
		v, ok := rawV.(map[string]any)
		if !ok {
			continue
		}
		dist, ok := v["dist"].(map[string]any)
		if !ok {
			dist = make(map[string]any)
			v["dist"] = dist
		}
		dist["tarball"] = fmt.Sprintf("%s/%s/-/%s-%s.tgz", base, pkgName, name, ver)
	}
	return json.Marshal(doc)
}

// ApplyBase 把哨兵地址替换为实际站点根地址（单次内存扫描，不做 JSON 解析）。
// ok=false 表示内容不含哨兵（历史缓存或异常 JSON），调用方应回退 RewritePackument。
func ApplyBase(raw []byte, baseURL string) ([]byte, bool) {
	if !bytes.Contains(raw, sentinelBytes) {
		return raw, false
	}
	return bytes.ReplaceAll(raw, sentinelBytes, []byte(strings.TrimSuffix(baseURL, "/"))), true
}

// ConventionalFilename 生成约定格式的 tarball 文件名：{name}-{version}.tgz。
func ConventionalFilename(pkgName, version string) string {
	return path.Base(pkgName) + "-" + version + ".tgz"
}

// VersionFromFilename 从 tarball 文件名反解版本号，无法解析时返回空字符串。
func VersionFromFilename(pkgName, filename string) string {
	prefix := path.Base(pkgName) + "-"
	if strings.HasPrefix(filename, prefix) && strings.HasSuffix(filename, ".tgz") {
		return strings.TrimSuffix(strings.TrimPrefix(filename, prefix), ".tgz")
	}
	return ""
}
