// 列表/详情/抽屉三类页面的通用交互逻辑（与生态语义无关）。
// - 竞态守卫（latestOnly）+ AbortController：新请求发出即取消旧请求，
//   快速翻页/切包时旧响应既不覆盖展示、也不再占用连接与后端查询；
// - 列表区分 loading / empty / error 三态：错误保留旧数据并提供重试，
//   空态仅用于成功返回 0 条；
// - 抽屉统一 Esc 关闭、焦点陷阱、滚动锁定与焦点归还。
import { onMounted, onUnmounted, ref } from 'vue';
import { latestOnly } from './api';

// focusableSelector 抽屉内可聚焦元素（用于焦点陷阱与初始焦点）。
const focusableSelector = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// usePagedList 服务端分页列表内核：公开列表页（路由查询驱动）与后台列表页
// （本地状态驱动）共用；分页参数的组装与翻页交互留在调用方。
// fetchPage(signal) 返回 { items, total, page?, totalPages }。
// onError 可选：错误额外通知（如 notifyError）。
export function usePagedList({ fetchPage, onError }) {
  const loading = ref(false);
  const items = ref(null); // null = 尚未完成首次加载
  const error = ref('');
  const total = ref(0);
  const totalPages = ref(1);
  const page = ref(1); // 服务端回传的当前页（搜索命中后可能小于请求页）
  const guard = latestOnly();
  let controller = null;

  async function load() {
    const id = guard.begin();
    controller?.abort();
    controller = new AbortController();
    loading.value = true;
    error.value = '';
    try {
      const data = await fetchPage(controller.signal);
      if (guard.isStale(id)) return; // 已被后续请求取代，丢弃慢的旧响应
      items.value = data.items || [];
      total.value = data.total;
      totalPages.value = data.totalPages || 1;
      page.value = data.page || 1;
    } catch (e) {
      if (guard.isStale(id)) return;
      error.value = e.message; // 保留旧 items：错误态覆盖空态展示
      // 401 由全局处理器统一跳登录页，不再叠加页面级错误提示
      if (onError && e.status !== 401) onError(e);
    } finally {
      if (!guard.isStale(id)) loading.value = false;
    }
  }

  onUnmounted(() => controller?.abort());
  return { loading, items, error, page, total, totalPages, load, retry: load };
}

// useDetailResource 详情页资源内核：loading / notFound / error 三态 + 重试。
// fetch(signal) 返回详情数据；404（notFoundStatus 可配）走未找到引导页。
export function useDetailResource({ fetch, notFoundStatus = 404, onError }) {
  const loading = ref(true);
  const notFound = ref(false);
  const detail = ref(null);
  const error = ref('');
  const guard = latestOnly();
  let controller = null;

  async function load() {
    const id = guard.begin();
    controller?.abort();
    controller = new AbortController();
    loading.value = true;
    notFound.value = false;
    error.value = '';
    detail.value = null;
    try {
      const data = await fetch(controller.signal);
      if (guard.isStale(id)) return; // 已切到别的资源，丢弃慢响应
      detail.value = data;
    } catch (e) {
      if (guard.isStale(id)) return;
      if (e.status === notFoundStatus) notFound.value = true;
      else {
        error.value = e.message;
        // 401 由全局处理器统一跳登录页，不再叠加页面级错误提示
        if (onError && e.status !== 401) onError(e);
      }
    } finally {
      if (!guard.isStale(id)) loading.value = false;
    }
  }

  onUnmounted(() => controller?.abort());
  return { loading, notFound, detail, error, load, retry: load };
}

// useDrawer 抽屉交互：Esc 关闭、焦点陷阱（Tab 循环）、body 滚动锁定、
// 关闭后焦点归还触发元素。返回 layerRef 绑定到 role=dialog 的根元素。
export function useDrawer({ onClose }) {
  const layerRef = ref(null);
  let restoreScroll = false;
  let lastFocused = null;

  function focusables() {
    return layerRef.value ? Array.from(layerRef.value.querySelectorAll(focusableSelector)) : [];
  }

  function onKeydown(e) {
    if (e.key === 'Escape') {
      onClose();
      return;
    }
    if (e.key !== 'Tab') return;
    const list = focusables();
    if (!list.length) return;
    const first = list[0];
    const last = list[list.length - 1];
    const active = document.activeElement;
    if (!layerRef.value.contains(active)) {
      e.preventDefault();
      first.focus();
    } else if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  onMounted(() => {
    lastFocused = document.activeElement;
    document.addEventListener('keydown', onKeydown);
    if (!document.body.classList.contains('no-scroll')) {
      document.body.classList.add('no-scroll');
      restoreScroll = true;
    }
    // 初始焦点落在抽屉内第一个可聚焦元素
    const list = focusables();
    if (list.length) list[0].focus();
    else layerRef.value?.focus();
  });
  onUnmounted(() => {
    document.removeEventListener('keydown', onKeydown);
    if (restoreScroll) document.body.classList.remove('no-scroll');
    // 焦点归还打开抽屉的触发元素
    if (lastFocused && document.contains(lastFocused)) lastFocused.focus();
  });
  return { layerRef };
}
