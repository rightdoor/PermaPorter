package proxy

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Kind GOPROXY 协议请求的资源类型。
type Kind uint8

const (
	KindList   Kind = iota // /@v/list
	KindLatest             // /@latest
	KindInfo               // /@v/<ver>.info
	KindMod                // /@v/<ver>.mod
	KindZip                // /@v/<ver>.zip
	// KindZiphash 扩展端点 /@v/<ver>.ziphash：goproxy 协议外，部分代理（如 goproxy.io）
	// 会提供；本服务在 zip 落盘后自行计算 h1 校验和并缓存。
	KindZiphash
)

// Request 解析后的 GOPROXY 协议请求。
type Request struct {
	// Module 解码后的规范模块路径（大写原样），如 github.com/BurntSushi/toml
	Module string
	// EscapedModule GOPROXY 大小写编码后的模块路径（用于上游 URL 与磁盘目录），
	// 如 github.com/!burnt!sushi/toml
	EscapedModule string
	// Version 解码后的版本（.info 允许非规范 token：分支名/提交号等）
	Version string
	// EscapedVersion GOPROXY 编码后的版本（用于上游 URL 与磁盘文件名）
	EscapedVersion string
	Kind           Kind
}

// ErrNotProtocol 表示路径不是 GOPROXY 协议形态（9092 上此类路径返回 text/plain 404）。
var ErrNotProtocol = errors.New("非 GOPROXY 协议路径")

// infoVersionTokenRe .info 端点允许的非规范版本 token：分支名、提交号等。
// 禁止路径分隔符、`..`、空白与控制字符，防止磁盘路径穿越。
var infoVersionTokenRe = regexp.MustCompile(`^[A-Za-z0-9._+~!-]+$`)

// Parse 解析 GOPROXY 协议请求路径（如 /github.com/x/@v/list）。
// 返回值组合：
//   - req != nil, err == nil：协议请求，解析成功；
//   - err == ErrNotProtocol：非协议路径（模块元数据/搜索等业务路径）；
//   - 其他 err：协议路径但参数非法，调用方应返回 400。
func Parse(rawPath string) (*Request, error) {
	p := strings.TrimPrefix(rawPath, "/")
	if p == "" {
		return nil, ErrNotProtocol
	}
	// p 已由路由层清理过（gin 会规范化 .. 段），这里再做一次兜底
	if cleaned := path.Clean("/" + p); cleaned != "/"+p {
		return nil, fmt.Errorf("路径包含非法段")
	}

	req := &Request{}
	switch {
	case strings.HasSuffix(p, "/@latest"):
		req.Kind = KindLatest
		if err := req.setModule(p[:len(p)-len("/@latest")]); err != nil {
			return nil, err
		}
		return req, nil
	default:
		idx := strings.LastIndex(p, "/@v/")
		if idx < 0 {
			// 无 @v 也无 @latest：非协议路径（如 /github.com/x）
			if strings.Contains(p, "@v/") || strings.HasSuffix(p, "/@v") {
				return nil, fmt.Errorf("非法的 GOPROXY 路径")
			}
			return nil, ErrNotProtocol
		}
		mod, rest := p[:idx], p[idx+len("/@v/"):]
		if err := req.setModule(mod); err != nil {
			return nil, err
		}
		return parseVersionFile(req, rest)
	}
}

// setModule 校验并记录模块路径（先按 GOPROXY 编码解码，再用 module.CheckPath 校验）。
func (r *Request) setModule(escaped string) error {
	if escaped == "" {
		return fmt.Errorf("模块路径为空")
	}
	if len(escaped) > 512 {
		return fmt.Errorf("模块路径过长")
	}
	decoded, err := module.UnescapePath(escaped)
	if err != nil {
		return fmt.Errorf("模块路径编码非法: %w", err)
	}
	if err := module.CheckPath(decoded); err != nil {
		return fmt.Errorf("模块路径非法: %w", err)
	}
	r.Module = decoded
	// 统一使用重新编码后的形式访问上游与磁盘，保证与解码结果严格互逆
	escaped, err2 := module.EscapePath(decoded)
	if err2 != nil {
		return fmt.Errorf("模块路径编码失败: %w", err2)
	}
	r.EscapedModule = escaped
	return nil
}

// parseVersionFile 解析 @v/ 之后的文件名（list / <ver>.info|.mod|.zip|.ziphash）。
func parseVersionFile(r *Request, rest string) (*Request, error) {
	if rest == "list" {
		r.Kind = KindList
		return r, nil
	}
	for _, k := range []struct {
		suffix string
		kind   Kind
	}{{".ziphash", KindZiphash}, {".zip", KindZip}, {".mod", KindMod}, {".info", KindInfo}} {
		if strings.HasSuffix(rest, k.suffix) {
			escapedVer := strings.TrimSuffix(rest, k.suffix)
			if escapedVer == "" {
				return nil, fmt.Errorf("版本号为空")
			}
			ver, err := module.UnescapeVersion(escapedVer)
			if err != nil {
				return nil, fmt.Errorf("版本编码非法: %w", err)
			}
			if err := validateVersionToken(ver); err != nil {
				return nil, err
			}
			escapedVer2, err := module.EscapeVersion(ver)
			if err != nil {
				return nil, fmt.Errorf("版本编码失败: %w", err)
			}
			r.Version = ver
			r.EscapedVersion = escapedVer2
			r.Kind = k.kind
			// .mod/.zip/.ziphash 只接受规范版本（伪版本本身是规范版本，可以缓存）
			if k.kind != KindInfo && !isCanonicalVersion(ver) {
				return nil, fmt.Errorf("版本 %q 不是规范版本", ver)
			}
			return r, nil
		}
	}
	return nil, fmt.Errorf("未知的 GOPROXY 资源类型")
}

// validateVersionToken 校验版本 token 的基础安全性（.info 允许非规范 token）。
func validateVersionToken(ver string) error {
	if ver == "" || len(ver) > 128 {
		return fmt.Errorf("版本号长度非法")
	}
	if !infoVersionTokenRe.MatchString(ver) {
		return fmt.Errorf("版本号包含非法字符")
	}
	if strings.Contains(ver, "..") {
		return fmt.Errorf("版本号包含路径穿越片段")
	}
	for _, r := range ver {
		if unicode.IsControl(r) {
			return fmt.Errorf("版本号包含控制字符")
		}
	}
	return nil
}

// IsCanonical 报告请求版本是否为规范 semver 版本。
func (r *Request) IsCanonical() bool {
	return r.Version != "" && isCanonicalVersion(r.Version)
}

// isCanonicalVersion 判断版本是否为规范 semver 形式
// （替代新版 x/mod 已移除的 module.CheckVersion 的版本校验部分）。
func isCanonicalVersion(v string) bool {
	return semver.IsValid(v) && v == module.CanonicalVersion(v)
}

// versionFile 拼接 @v 目录下的产物文件名（如 v1.10.0.zip）。
func (r *Request) versionFile(ext string) string {
	return r.EscapedVersion + ext
}

// escapeVersion 供包内使用的版本编码。
func escapeVersion(v string) string {
	escaped, err := module.EscapeVersion(v)
	if err != nil {
		return v // 已通过校验的版本不会失败
	}
	return escaped
}

// escapePath 供包内使用的模块路径编码。
func escapePath(m string) string {
	escaped, err := module.EscapePath(m)
	if err != nil {
		return m // 已通过校验的路径不会失败
	}
	return escaped
}

// EscapePath GOPROXY 大小写编码（导出供清理/预热等包复用）。
func EscapePath(m string) string { return escapePath(m) }

// EscapeVersion GOPROXY 版本编码（导出供清理/预热等包复用）。
func EscapeVersion(v string) string { return escapeVersion(v) }

// ValidateModuleInput 校验外部输入（管理 API/预热）的模块路径是否合法。
func ValidateModuleInput(m string) error {
	if m == "" || len(m) > 512 {
		return fmt.Errorf("模块路径长度非法")
	}
	// 管理接口传入的是规范路径；同时容忍 GOPROXY 编码形式（先解码再校验）
	decoded, err := module.UnescapePath(m)
	if err == nil {
		m = decoded
	}
	return module.CheckPath(m)
}

