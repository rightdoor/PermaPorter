package utils

import (
	"strconv"
	"strings"
)

// VersionCompare 比较两个 semver 风格的版本号：a > b 返回 1，a < b 返回 -1，相等返回 0。
// 分段数值比较，非数值段按字典序比较（对预发布版本号是近似处理）。
// 实现为 VersionKey 的薄包装：全仓库只有一份排序语义，避免两份实现漂移。
func VersionCompare(a, b string) int {
	return NewVersionKey(a).Compare(NewVersionKey(b))
}

// VersionKey 预解析的版本号排序键：排序场景先解析一次，避免比较过程中反复 Split/Atoi。
type VersionKey struct {
	segs []versionSeg
}

type versionSeg struct {
	num   int
	str   string
	isNum bool
}

// NewVersionKey 解析版本号为排序键。
func NewVersionKey(v string) VersionKey {
	parts := strings.Split(v, ".")
	segs := make([]versionSeg, len(parts))
	for i, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			segs[i] = versionSeg{num: n, isNum: true}
		} else {
			segs[i] = versionSeg{str: p}
		}
	}
	return VersionKey{segs: segs}
}

// Compare 与 VersionCompare 语义一致（a > b 返回 1，a < b 返回 -1，相等返回 0）。
func (k VersionKey) Compare(o VersionKey) int {
	n := len(k.segs)
	if len(o.segs) > n {
		n = len(o.segs)
	}
	for i := 0; i < n; i++ {
		var x, y versionSeg
		if i < len(k.segs) {
			x = k.segs[i]
		}
		if i < len(o.segs) {
			y = o.segs[i]
		}
		if c := compareSeg(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func compareSeg(x, y versionSeg) int {
	switch {
	case x.isNum && y.isNum:
		switch {
		case x.num > y.num:
			return 1
		case x.num < y.num:
			return -1
		}
		return 0
	case x.isNum:
		return 1 // 数值段大于非数值段
	case y.isNum:
		return -1
	default:
		return strings.Compare(x.str, y.str)
	}
}
