// 极简 fetch 封装：JSON 进出、错误统一抛 ApiError、401 统一回调（跳登录页）。
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

let onUnauthorized = null;
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn;
}

// 默认超时：避免网络异常时请求永久悬挂、页面一直转圈
const DEFAULT_TIMEOUT = 15000;

async function request(method, url, body, opts = {}) {
  const ctrl = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    ctrl.abort();
  }, opts.timeout ?? DEFAULT_TIMEOUT);
  // 调用方的 signal（如组件卸载、切包）也用于取消
  const external = opts.signal;
  if (external) {
    if (external.aborted) ctrl.abort();
    else external.addEventListener('abort', () => ctrl.abort(), { once: true });
  }

  let res;
  try {
    res = await fetch(url, {
      method,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: ctrl.signal,
    });
  } catch {
    if (timedOut) throw new ApiError(0, '请求超时，请稍后重试');
    if (external?.aborted) throw new ApiError(0, '请求已取消');
    throw new ApiError(0, '网络请求失败，请确认服务是否可达');
  } finally {
    clearTimeout(timer);
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    /* 空响应体 */
  }
  if (!res.ok) {
    if (res.status === 401 && onUnauthorized) onUnauthorized();
    throw new ApiError(res.status, data?.error || `请求失败 (${res.status})`);
  }
  return data;
}

// qs 将参数对象拼为查询串（跳过 undefined/null/空串）
export function qs(params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v === undefined || v === null || v === '') continue;
    q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : '';
}

// latestOnly 生成一个「只认最后一次调用」的守卫：
// 快速切包/翻页时，慢的旧响应不会覆盖新结果。
export function latestOnly() {
  let seq = 0;
  return {
    // begin 返回本次调用的序号；isStale 判断是否已被后续调用取代
    begin: () => ++seq,
    isStale: (id) => id !== seq,
  };
}

export const api = {
  get: (url, opts) => request('GET', url, undefined, opts),
  post: (url, body, opts) => request('POST', url, body ?? {}, opts),
  del: (url, opts) => request('DELETE', url, undefined, opts),
};
