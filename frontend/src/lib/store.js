// 全局应用状态：初始化/登录状态由服务端外壳注入（window.__STATE__），
// 缺失时回退到 /api/state 拉取（例如前端独立部署或直接访问 dev server）。
// goProxy / npmRegistry / pyIndex / mavenRepo 为服务端注入的四生态协议端口地址（§8：前端不得硬编码端口）。
import { reactive } from 'vue';
import { api } from './api';

const injected = typeof window !== 'undefined' ? window.__STATE__ : null;

export const app = reactive({
  // 服务端已注入状态时无需等待 /api/state，首屏少一个 RTT
  ready: !!injected,
  initialized: !!injected?.initialized,
  authed: !!injected?.authed,
  // setupMode：数据库未配置的初始化模式（管理端导向 /admin/wizard 向导）
  setupMode: !!injected?.setupMode,
  version: injected?.version || '',
  goProxy: injected?.goProxy || '',
  npmRegistry: injected?.npmRegistry || '',
  pyIndex: injected?.pyIndex || '',
  mavenRepo: injected?.mavenRepo || '',
});

export async function loadState() {
  try {
    const s = await api.get('/api/state');
    app.initialized = !!s.initialized;
    app.authed = !!s.authed;
    app.setupMode = !!s.setupMode;
    app.version = s.version || '';
    app.goProxy = s.goProxy || '';
    app.npmRegistry = s.npmRegistry || '';
    app.pyIndex = s.pyIndex || '';
    app.mavenRepo = s.mavenRepo || '';
  } catch {
    /* 服务不可用时保持默认值，页面仍可渲染 */
  }
  app.ready = true;
}
