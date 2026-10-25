// PreheatView 组件测试（P0-3 回归）：提交预热后任务进度实时渲染；
// 切换生态互不干扰；完成后停止轮询。
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import PreheatView from './PreheatView.vue';
import { api } from '../lib/api';

vi.mock('../lib/api', () => ({
  api: { get: vi.fn(), post: vi.fn() },
}));

const goJob = (done, total) => ({
  id: 7,
  withZip: true,
  createdAt: '2026-01-01T00:00:00Z',
  finished: done >= total,
  done,
  failed: 0,
  total,
  items: Array.from({ length: total }, (_, i) => ({
    name: `example.com/m${i}`,
    state: i < done ? 'done' : 'running',
    version: 'v1.0.0',
    size: 1024,
    err: '',
  })),
});

function mountView() {
  return mount(PreheatView, { attachTo: document.body });
}

describe('PreheatView', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.get.mockResolvedValue({ job: null });
  });

  it('提交任务后渲染进度与明细', async () => {
    api.post.mockResolvedValue({ total: 2, job: goJob(1, 2) });
    const wrapper = mountView();
    await flushPromises();

    await wrapper.find('form').trigger('submit');
    await flushPromises();

    const text = wrapper.text();
    expect(text).toContain('1 / 2'); // 进度计数
    expect(text).toContain('example.com/m0'); // 明细
    expect(text).toContain('任务 #7'); // 任务标识
    wrapper.unmount();
  });

  it('无任务时显示空态（提交前不误渲染）', async () => {
    const wrapper = mountView();
    await flushPromises();
    expect(wrapper.text()).toContain('还没有预热任务');
    wrapper.unmount();
  });

  it('切换生态展示各自最近任务，互不干扰', async () => {
    api.get.mockImplementation(async (url) =>
      url.includes('eco=npm') ? { job: { ...goJob(1, 1), id: 9, withTarball: true, items: [{ name: 'vue', state: 'done', version: '3.5.0', size: 1, err: '' }] } } : { job: goJob(2, 2) },
    );
    const wrapper = mountView();
    await flushPromises();

    expect(wrapper.text()).toContain('任务 #7'); // go 任务
    const tabs = wrapper.findAll('[role="tab"]');
    await tabs[1].trigger('click'); // 切到 NPM
    await flushPromises();
    expect(wrapper.text()).toContain('任务 #9'); // npm 任务
    expect(wrapper.text()).toContain('vue');
    wrapper.unmount();
  });
});
