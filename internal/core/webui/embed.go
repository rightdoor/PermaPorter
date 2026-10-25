// Package webui 嵌入前端构建产物（frontend/ 的 Vite 输出）并生成 SPA 外壳。
// assets/ 不入库：由 make frontend 在构建前生成（见 Makefile 的 frontend-assets）。
package webui

import "embed"

//go:embed all:assets
var FS embed.FS
