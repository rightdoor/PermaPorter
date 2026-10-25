// 路由守卫回归测试：初始化模式（setupMode）下全部路由（含控制台根路径与公开页）
// 必须导向部署向导，且向导页自身不得再被"密码初始化"分流劫持——两条守卫顺序不当
// 会互踢形成 wizard↔setup 无限重定向，vue-router 中止导航，表现为管理端入口点击无响应。
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../lib/api', () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn() },
}));

import { router } from './index';
import { app } from '../lib/store';

describe('路由守卫：初始化模式分流', () => {
  beforeEach(() => {
    app.ready = true;
    app.setupMode = true;
    app.initialized = false;
    app.authed = false;
  });

  it('setupMode 下控制台根路径与登录页都落在向导，不发生无限重定向', async () => {
    await router.push('/');
    expect(router.currentRoute.value.name).toBe('wizard');

    await router.push('/admin/login');
    expect(router.currentRoute.value.name).toBe('wizard');

    await router.push('/admin/wizard');
    expect(router.currentRoute.value.name).toBe('wizard');
  });

  it('非 setupMode、已配置库但未设密码时，管理端导向密码初始化页', async () => {
    app.setupMode = false;
    await router.push('/admin/dashboard');
    expect(router.currentRoute.value.name).toBe('setup');
  });
});
