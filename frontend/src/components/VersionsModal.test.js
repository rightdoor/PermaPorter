// VersionsModal 组件测试（P1-1 回归）：删除响应键按生态区分——
// npm 读 pkgRemoved、go 读 moduleRemoved；删空后 emit changed(true)。
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { defineComponent, h } from 'vue';
import VersionsModal from './VersionsModal.vue';
import { api } from '../lib/api';

vi.mock('../lib/api', async () => {
  const actual = await vi.importActual('../lib/api');
  return { api: { get: vi.fn(), del: vi.fn() }, qs: actual.qs, latestOnly: actual.latestOnly };
});

const RouterLinkStub = defineComponent({
  name: 'RouterLink',
  setup: (_, { slots }) => () => h('a', {}, slots.default?.()),
});

function mountModal(kind, name) {
  return mount(VersionsModal, {
    props: { kind, name },
    global: { components: { RouterLink: RouterLinkStub } },
    attachTo: document.body,
  });
}

async function confirmRemove() {
  await flushPromises();
  const removeBtn = [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === '删除');
  expect(removeBtn, '应有删除按钮').toBeTruthy();
  removeBtn.click();
  await flushPromises();
  const confirmBtn = [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === '确认删除');
  expect(confirmBtn, '确认对话框应打开').toBeTruthy();
  confirmBtn.click();
  await flushPromises();
}

describe('VersionsModal 删除响应键映射', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('npm：后端返回 pkgRemoved=true 时 emit changed(true)', async () => {
    api.get.mockResolvedValue({ versions: [{ version: '1.0.0', zipCached: true, zipSizeBytes: 5, cachedAt: '2026-01-01 00:00:00' }] });
    api.del.mockResolvedValue({ pkgRemoved: true });

    const wrapper = mountModal('npm', 'demo');
    await confirmRemove();

    expect(api.del).toHaveBeenCalledWith(expect.stringContaining('/api/admin/package-version'));
    expect(wrapper.emitted('changed')).toEqual([[true]]);
    wrapper.unmount();
  });

  it('npm：后端只返回 moduleRemoved 时不误触发整包刷新', async () => {
    api.get.mockResolvedValue({ versions: [{ version: '1.0.0', zipCached: true, zipSizeBytes: 5, cachedAt: '2026-01-01 00:00:00' }] });
    api.del.mockResolvedValue({ moduleRemoved: true }); // 错误键：npm 侧不应识别

    const wrapper = mountModal('npm', 'demo');
    await confirmRemove();

    expect(wrapper.emitted('changed')).toBeUndefined();
    wrapper.unmount();
  });

  it('go：后端返回 moduleRemoved=true 时 emit changed(true)', async () => {
    api.get.mockResolvedValue({ versions: [{ version: 'v1.0.0', pseudo: false, zipCached: true, zipSizeBytes: 5, cachedAt: '2026-01-01 00:00:00' }] });
    api.del.mockResolvedValue({ moduleRemoved: true });

    const wrapper = mountModal('go', 'example.com/demo');
    await confirmRemove();

    expect(api.del).toHaveBeenCalledWith(expect.stringContaining('/api/admin/module-version'));
    expect(wrapper.emitted('changed')).toEqual([[true]]);
    wrapper.unmount();
  });

  it('删除失败时用 notifyError 提示且不 emit', async () => {
    api.get.mockResolvedValue({ versions: [{ version: '1.0.0', zipCached: true, zipSizeBytes: 5, cachedAt: '2026-01-01 00:00:00' }] });
    api.del.mockRejectedValue(new Error('网络请求失败'));

    const wrapper = mountModal('npm', 'demo');
    await confirmRemove();

    expect(wrapper.emitted('changed')).toBeUndefined();
    expect(wrapper.text()).not.toContain('undefined');
    wrapper.unmount();
  });
});
