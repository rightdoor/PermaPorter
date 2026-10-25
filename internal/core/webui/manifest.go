package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"path"
	"sync"
)

// Vite 构建产物 manifest（assets/.vite/manifest.json）：
// 将源模块名映射为带内容 hash 的输出文件，供 SPA 外壳引用实现长缓存。
type viteEntry struct {
	File    string   `json:"file"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
}

var (
	manifestOnce sync.Once
	manifest     map[string]viteEntry
	indexOnce    sync.Once
	indexTmpl    []byte
)

// State SPA 外壳注入的初始状态：路由守卫据此决定首屏跳转，
// 免去「先请求 /api/state 再发页面数据」的串行等待。
type State struct {
	Initialized bool `json:"initialized"`
	Authed      bool `json:"authed"`
	// SetupMode 未初始化模式（数据库未配置）：前端路由守卫据此把管理端导向初始化向导。
	SetupMode   bool   `json:"setupMode,omitempty"`
	Version     string `json:"version"`
	GoProxy     string `json:"goProxy,omitempty"`     // GOPROXY 协议端口基地址（服务端注入）
	NPMRegistry string `json:"npmRegistry,omitempty"` // npm Registry 协议端口基地址（服务端注入）
	PyIndex     string `json:"pyIndex,omitempty"`     // PyPI Simple 协议端口基地址（服务端注入）
	MavenRepo   string `json:"mavenRepo,omitempty"`   // Maven 仓库协议端口基地址（服务端注入）
}

// statePlaceholder 外壳模板中的状态占位符（每次请求替换为真实 JSON）。
var statePlaceholder = []byte("__STEAD_STATE__")

func loadManifest() {
	manifestOnce.Do(func() {
		manifest = map[string]viteEntry{}
		data, err := fs.ReadFile(FS, "assets/.vite/manifest.json")
		if err != nil {
			// 前端未构建时降级：外壳仍可输出，引用无 hash 的默认路径
			log.Printf("[webui] 读取 Vite manifest 失败（前端未构建？）: %v", err)
			return
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			log.Printf("[webui] 解析 Vite manifest 失败: %v", err)
		}
	})
}

// assetURL 解析 manifest 键对应的带 hash 资源 URL；
// manifest 缺失时回退到约定俗成的无 hash 路径。
func assetURL(key string) string {
	loadManifest()
	if e, ok := manifest[key]; ok && e.File != "" {
		return "/static/assets/" + e.File
	}
	return "/static/assets/" + path.Base(key)
}

// cssURL 主样式表 URL（由 main.js 引入，经 Vite 抽取为独立文件）。
func cssURL() string {
	loadManifest()
	if e, ok := manifest["src/main.js"]; ok && len(e.CSS) > 0 && e.CSS[0] != "" {
		return "/static/assets/" + e.CSS[0]
	}
	return "/static/assets/main.css"
}

// jsURL 主脚本 URL（Vue 应用与全部交互逻辑）。
func jsURL() string { return assetURL("src/main.js") }

// modulePreload 首屏需要的模块 preload 链接（vendor 等静态导入的 chunk）。
func modulePreload() string {
	loadManifest()
	e, ok := manifest["src/main.js"]
	if !ok {
		return ""
	}
	var b bytes.Buffer
	for _, imp := range e.Imports {
		if f, ok := manifest[imp]; ok && f.File != "" {
			fmt.Fprintf(&b, "  <link rel=\"modulepreload\" href=\"/static/assets/%s\">\n", f.File)
		}
	}
	return b.String()
}

// IndexHTML 返回注入了初始状态的 SPA 外壳页面。
// 模板只构建一次，每次请求仅做一次占位符替换（开销可忽略）。
func IndexHTML(s State) []byte {
	indexOnce.Do(func() {
		indexTmpl = []byte(fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>PermaPorter · 内网四生态镜像</title>
  <link rel="icon" type="image/png" href="/static/assets/logo.png">
  <link rel="stylesheet" href="%s">
%s  <script>try{var t=localStorage.getItem('permaporter-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t);}catch(e){}/* 主题预置，避免闪烁 */</script>
  <script>window.__STATE__=__STEAD_STATE__;</script>
</head>
<body>
  <div id="app"></div>
  <script type="module" crossorigin src="%s"></script>
</body>
</html>`, cssURL(), modulePreload(), jsURL()))
	})
	payload, err := json.Marshal(s)
	if err != nil {
		payload = []byte(`{"initialized":false,"authed":false,"version":""}`)
	}
	return bytes.Replace(indexTmpl, statePlaceholder, payload, 1)
}
