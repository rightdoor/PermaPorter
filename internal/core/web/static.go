package web

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// 静态文件内容与 ETag 在启动时一次性载入内存。
// embed.FS 的文件修改时间为零值，Gin StaticFS 因此不会返回任何缓存头，
// 导致浏览器每次刷新都全量重新下载，局域网访问会出现明显的字体闪烁。
var (
	staticFiles = map[string][]byte{}
	staticETags = map[string]string{}
)

var contentTypeByExt = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
}

func initStaticAssets(fsys embed.FS) {
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// .vite/ 是构建工具的模块清单（webui 包解析内嵌 FS 使用），
		// 不对外服务，避免暴露前端源码模块结构
		if d.IsDir() && d.Name() == ".vite" {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		staticFiles[p] = data
		staticETags[p] = fmt.Sprintf(`"%x"`, sum[:8])
		return nil
	}); err != nil {
		panic(fmt.Sprintf("加载内嵌静态资源失败: %v", err))
	}
}

// staticHandler 按内容 ETag 提供内嵌静态资源（frontend/ 的 Vite 构建产物）。
// 带内容 hash 文件名的 JS/CSS（assets/assets/）走一年期 immutable 长缓存；
// 其余（manifest/logo 等非 hash 文件名）保持 max-age=0 must-revalidate 以便更新即时生效。
func staticHandler(c *gin.Context) {
	name := path.Clean(strings.TrimPrefix(c.Request.URL.Path, "/static/"))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		c.Status(http.StatusNotFound)
		return
	}
	data, ok := staticFiles[name]
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	etag := staticETags[name]
	switch {
	// 内容 hash 的构建产物（assets/*.js|css）走一年期 immutable 长缓存；
	// logo 等非 hash 文件保持 max-age=0 must-revalidate 以便更新即时生效。
	case strings.HasPrefix(name, "assets/") && isHashedAsset(name):
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	default:
		c.Header("Cache-Control", "public, max-age=0, must-revalidate")
	}
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	ct := contentTypeByExt[strings.ToLower(path.Ext(name))]
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	c.Data(http.StatusOK, ct, data)
}

// hashedAssetRe 带内容 hash 的构建产物文件名（Vite 实际产物命名：name-<8位base62>.js|css，
// 如 main-CToQXxdf.css、vendor-BQVu_NzZ.js——连字符分隔且 hash 段可含 _ 与非十六进制字符）。
var hashedAssetRe = regexp.MustCompile(`-[A-Za-z0-9_-]{8}\.(?:js|css|mjs)$`)

// isHashedAsset 判断是否为带内容 hash 的构建产物（main-XXXXXXXX.css / vendor-XXXXXXXX.js 等）。
func isHashedAsset(name string) bool {
	return hashedAssetRe.MatchString(path.Base(name))
}
