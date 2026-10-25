package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"permaporter/internal/npmadapter/models"
	"permaporter/internal/core/utils"
)

const dbTimeFormat = "2006-01-02 15:04:05"

// ExtractPackument 从上游 packument JSON 中提取包信息与全部版本明细。
func ExtractPackument(raw []byte, pkgName string) (*models.Package, []models.Version, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("解析 packument 失败: %w", err)
	}
	now := time.Now().UTC().Format(dbTimeFormat)

	pkg := &models.Package{PkgName: pkgName, MetaCachedAt: now}
	pkg.Description = asString(doc["description"])
	if dt, ok := doc["dist-tags"].(map[string]any); ok {
		pkg.LatestVersion = asString(dt["latest"])
	}
	pkg.HomepageURL = asString(doc["homepage"])
	pkg.License = parseLicense(doc["license"])
	pkg.RepositoryURL = parseRepository(doc["repository"])
	pkg.ReadmeContent = asString(doc["readme"])

	times := map[string]string{}
	if t, ok := doc["time"].(map[string]any); ok {
		for k, v := range t {
			times[k] = asString(v)
		}
	}

	var versions []models.Version
	if vs, ok := doc["versions"].(map[string]any); ok {
		for ver, rawV := range vs {
			// 版本键直接拼入磁盘路径（packages/<pkg>/<ver>.tgz）：白名单校验，
			// 拒绝 `../`、分隔符与控制字符等穿越载荷，非法版本不入库
			if !ValidVersionKey(ver) {
				log.Printf("[proxy] 跳过非法版本键 %q（包 %s）", ver, pkgName)
				continue
			}
			v, ok := rawV.(map[string]any)
			if !ok {
				continue
			}
			mv := models.Version{
				Version:              ver,
				TarballPath:          fmt.Sprintf("packages/%s/%s.tgz", pkgName, ver),
				PublishedAt:          times[ver],
				CachedAt:             now,
				DependenciesJSON:     depsJSON(v["dependencies"]),
				DevDependenciesJSON:  depsJSON(v["devDependencies"]),
				PeerDependenciesJSON: depsJSON(v["peerDependencies"]),
			}
			if dist, ok := v["dist"].(map[string]any); ok {
				mv.TarballURL = asString(dist["tarball"])
				mv.Integrity = asString(dist["integrity"])
			}
			versions = append(versions, mv)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return utils.VersionCompare(versions[i].Version, versions[j].Version) > 0
	})
	return pkg, versions, nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func depsJSON(v any) string {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		if b, err := json.Marshal(m); err == nil {
			return string(b)
		}
	}
	return ""
}

func parseLicense(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		return asString(t["type"])
	}
	return ""
}

func parseRepository(v any) string {
	var u string
	switch t := v.(type) {
	case string:
		u = t
	case map[string]any:
		u = asString(t["url"])
	}
	return cleanGitURL(u)
}

func cleanGitURL(u string) string {
	if u == "" {
		return ""
	}
	u = strings.TrimPrefix(u, "git+")
	u = strings.TrimSuffix(u, ".git")
	return u
}
