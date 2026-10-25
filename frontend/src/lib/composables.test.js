// composables 单元测试：分页列表三态与请求取消、详情资源 404/错误、抽屉焦点与滚动锁定。
import { describe, it, expect, vi } from 'vitest';
import { defineComponent, h, nextTick } from 'vue';
import { mount } from '@vue/test-utils';
import { usePagedList, useDetailResource, useDrawer } from './composables';

// withSetup 在组件实例内执行组合式函数（onMounted/onUnmounted 需要实例）。
function withSetup(setup) {
  let result;
  const Comp = defineComponent({
    setup() {
      result = setup();
      return () => h('div');
    },
  });
  const wrapper = mount(Comp, { attachTo: document.body });
  return { wrapper, result };
}

// delayedResolve 构造可控的 fetchPage 替身。
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('usePagedList', () => {
  it('成功加载填充列表字段', async () => {
    const { wrapper, result } = withSetup(() =>
      usePagedList({ fetchPage: async () => ({ items: [{ n: 1 }], total: 1, totalPages: 2 }) }),
    );
    await result.load();
    expect(result.items.value).toEqual([{ n: 1 }]);
    expect(result.total.value).toBe(1);
    expect(result.totalPages.value).toBe(2);
    expect(result.error.value).toBe('');
    wrapper.unmount();
  });

  it('错误保留旧数据并记录原因，重试成功后清错', async () => {
    let fail = true;
    const { wrapper, result } = withSetup(() =>
      usePagedList({
        fetchPage: async () => {
          if (fail) {
            const e = new Error('网络请求失败');
            e.status = 0;
            throw e;
          }
          return { items: [{ n: 2 }], total: 1, totalPages: 1 };
        },
      }),
    );
    await result.load();
    expect(result.error.value).toBe('网络请求失败');
    expect(result.items.value).toBe(null); // 首次失败无旧数据

    fail = false;
    await result.retry();
    expect(result.error.value).toBe('');
    expect(result.items.value).toEqual([{ n: 2 }]);
    wrapper.unmount();
  });

  it('新请求发出时通过 AbortController 取消旧请求', async () => {
    const first = deferred();
    const second = deferred();
    const signals = [];
    let call = 0;
    const { wrapper, result } = withSetup(() =>
      usePagedList({
        fetchPage: (signal) => {
          signals.push(signal);
          call++;
          return call === 1 ? first.promise : second.promise;
        },
      }),
    );
    const p1 = result.load();
    const p2 = result.load();
    expect(signals[0].aborted).toBe(true); // 第二次加载取消了第一次
    expect(signals[1].aborted).toBe(false);
    first.resolve({ items: ['stale'], total: 0, totalPages: 1 });
    second.resolve({ items: ['fresh'], total: 1, totalPages: 1 });
    await Promise.all([p1, p2]);
    expect(result.items.value).toEqual(['fresh']); // 旧响应不覆盖新结果
    wrapper.unmount();
    expect(signals[1].aborted).toBe(true); // 卸载取消在途请求
  });
});

describe('useDetailResource', () => {
  it('404 进入 notFound 态', async () => {
    const { wrapper, result } = withSetup(() =>
      useDetailResource({
        fetch: async () => {
          const e = new Error('not found');
          e.status = 404;
          throw e;
        },
      }),
    );
    await result.load();
    expect(result.notFound.value).toBe(true);
    expect(result.error.value).toBe('');
    wrapper.unmount();
  });

  it('非 404 错误进入 error 态并触发回调', async () => {
    const onError = vi.fn();
    const { wrapper, result } = withSetup(() =>
      useDetailResource({
        fetch: async () => {
          const e = new Error('上游不可用');
          e.status = 502;
          throw e;
        },
        onError,
      }),
    );
    await result.load();
    expect(result.notFound.value).toBe(false);
    expect(result.error.value).toBe('上游不可用');
    expect(onError).toHaveBeenCalledOnce();
    wrapper.unmount();
  });
});

describe('useDrawer', () => {
  const DrawerHost = defineComponent({
    props: { onClose: { type: Function, required: true } },
    setup(props) {
      const { layerRef } = useDrawer({ onClose: () => props.onClose() });
      return () =>
        h('div', { ref: layerRef, role: 'dialog', tabindex: '-1' }, [
          h('button', { type: 'button' }, '第一个'),
          h('button', { type: 'button' }, '最后一个'),
        ]);
    },
  });

  it('打开时锁定滚动并聚焦首个元素，关闭后恢复', async () => {
    const trigger = document.createElement('button');
    document.body.appendChild(trigger);
    trigger.focus();

    const onClose = vi.fn();
    const wrapper = mount(DrawerHost, { props: { onClose }, attachTo: document.body });
    await nextTick();

    expect(document.body.classList.contains('no-scroll')).toBe(true);
    expect(document.activeElement.textContent).toBe('第一个');

    wrapper.unmount();
    await nextTick();
    expect(document.body.classList.contains('no-scroll')).toBe(false);
    expect(document.activeElement).toBe(trigger); // 焦点归还触发元素
    trigger.remove();
  });

  it('Esc 触发关闭，Tab 在抽屉内循环', async () => {
    const onClose = vi.fn();
    const wrapper = mount(DrawerHost, { props: { onClose }, attachTo: document.body });
    await nextTick();

    const esc = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true });
    document.dispatchEvent(esc);
    expect(onClose).toHaveBeenCalledOnce();

    const buttons = document.querySelectorAll('button');
    const last = buttons[buttons.length - 1];
    last.focus();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
    await nextTick();
    expect(document.activeElement.textContent).toBe('第一个'); // 末尾 Tab 回到开头

    wrapper.unmount();
  });
});
