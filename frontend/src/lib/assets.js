// 运行时静态资源地址：这些文件在 frontend/public/ 内，生产构建由 Vite 原样拷贝到
// internal/core/webui/assets/，由 Go 的 /static 路由提供（不参与打包，路径保持固定）。
// 开发期由 Vite dev server 直接从 public/ 提供：后端没启动时首页 logo 也不会裂，
// 否则 /static 走代理会因连不上后端而返回 500。
export const LOGO_URL = import.meta.env.DEV ? '/logo.png' : '/static/assets/logo.png';
