// WizardView 回归测试：初始化完成后向导不允许再次进入——
// 状态过期的旧标签页（他处完成初始化或服务重启）打开向导时，
// 状态接口 409/404 触发自愈：刷新服务端状态并回登录页，不滞留表单。
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';

const replaceSpy = vi.fn();

vi.mock('vue-router', async (importOriginal) => ({
  ...(await importOriginal()),
  useRouter: () => ({ replace: replaceSpy }),
}));

vi.mock('../lib/api', () => {
  class ApiError extends Error {
    constructor(status, message) {
      super(message);
      this.status = status;
    }
  }
  return { ApiError, api: { get: vi.fn(), post: vi.fn(), del: vi.fn() } };
});

import WizardView from './WizardView.vue';
import { ApiError, api } from '../lib/api';
import { app } from '../lib/store';

function mountView() {
  return mount(WizardView, {
    global: { components: { RouterLink: { template: '<a><slot /></a>' } } },
  });
}

describe('WizardView：完成后禁止再次进入', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    app.ready = true;
    app.setupMode = true; // 模拟初始化完成前打开的旧标签页
    app.initialized = false;
    app.authed = false;
  });

  it('状态接口 409（他处已完成初始化）时自愈回登录页', async () => {
    api.get.mockImplementation(async (url) => {
      if (url === '/api/setup/status') throw new ApiError(409, '初始化已完成，请刷新页面登录');
      return { initialized: true, authed: false, setupMode: false }; // /api/state
    });
    const wrapper = mountView();
    await flushPromises();

    expect(replaceSpy).toHaveBeenCalledWith({ name: 'login' });
    expect(app.setupMode).toBe(false);
    expect(app.initialized).toBe(true);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false); // 不显示错误也不滞留
    wrapper.unmount();
  });

  it('初始化模式正常加载时渲染向导表单，不触发跳转', async () => {
    api.get.mockResolvedValue({
      configFile: '/var/lib/permaporter/config/permaporter.toml',
      initTokenRequired: false,
      ports: { ui: 9090, npm: 9091, go: 9092, py: 9093, mvn: 9094 },
      upstreams: {
        go: 'https://proxy.golang.org',
        goFallback: '',
        npm: 'https://registry.npmjs.org',
        py: 'https://pypi.org/simple/',
        mvn: 'https://repo.maven.apache.org/maven2',
      },
    });
    const wrapper = mountView();
    await flushPromises();

    expect(wrapper.text()).toContain('初始化向导');
    expect(wrapper.text()).toContain('数据库类型');
    expect(replaceSpy).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
