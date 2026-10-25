import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import tailwindcss from '@tailwindcss/vite';

// 后端控制台地址（UI_PORT，默认 9090）：pnpm dev 前先启动后端（make run 或 go run ./cmd/permaporter）。
// 固定写 127.0.0.1 而非 localhost：Go 只监听 IPv4（0.0.0.0），而 Node 解析 localhost 可能先取 ::1。
// 下载类请求不走此代理——详情页的 tarball/zip 链接由服务端注入 9091/9092 绝对地址。
const BACKEND = `http://127.0.0.1:${process.env.UI_PORT || 9090}`;

// 代理连不上后端（后端未启动、端口不符）时返回可读的 502。
// http-proxy 默认把 ECONNREFUSED 变成裸 500，页面上只看到一片「500 Internal Server Error」，
// 分不清是接口自身报错还是后端根本没起。
function proxyErrorHandler(proxy) {
  proxy.on('error', (err, _req, res) => {
    if (!res || typeof res.writeHead !== 'function' || res.headersSent) return;
    res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(
      JSON.stringify({
        error: `后端不可达（${BACKEND}）：请先启动后端（make run 或 go run ./cmd/permaporter）。原始错误：${err.code || err.message}`,
      }),
    );
  });
}

// 构建产物输出到 ../internal/core/webui/assets（Go embed 嵌入二进制），manifest 供 Go 生成
// SPA 外壳 index.html（internal/core/webui/manifest.go），页面路由由前端 vue-router 接管。
// 入口直接指向 JS 模块（HTML 外壳由 Go 服务端输出，无需 Vite 输出 HTML）。
//
// 前后端分离开发：pnpm dev 启动 Vite dev server（默认 5173），
// Go 后端单独运行（go run ./cmd/permaporter，监听 9090），前后端仅通过接口往来：
//   /api/**          → JSON API（含会话 Cookie，经代理同源无 CORS 问题）
//   /static/**       → logo 等静态资源（由 Go 内嵌产物提供）
//   /healthz         → 健康检查
// 构建入口仍是 src/main.js，frontend/index.html 仅作为 dev server 入口。
// tarball/zip 下载链接由服务端注入 9091/9092 绝对地址，dev 不经此代理。
export default defineConfig(({ command }) => ({
  // 生产构建由 Go 外壳在 /static/assets/ 下提供资源；动态 import 的预加载依赖
  // 也必须使用同一前缀，否则浏览器会把相对路径解析到站点根 /assets/ 而 404。
  // 开发服务器仍从根路径启动，保持原有代理与热更新地址不变。
  base: command === 'build' ? '/static/assets/' : '/',
  plugins: [vue(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: BACKEND, configure: proxyErrorHandler },
      '/static': { target: BACKEND, configure: proxyErrorHandler },
      '/healthz': { target: BACKEND, configure: proxyErrorHandler },
      // tarball/zip 等产物地址由服务端注入各协议端口绝对地址（9091-9094），dev 不经此代理
    },
  },
  build: {
    outDir: '../internal/core/webui/assets',
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      input: { main: 'src/main.js' },
      output: {
        // 框架单独成 chunk：业务代码变更时用户不必重新下载 Vue 运行时
        manualChunks: { vendor: ['vue', 'vue-router'] },
      },
    },
  },
}));
