// lib/api 单元测试：qs 参数拼装、latestOnly 竞态守卫、错误与 401 回调。
import { describe, it, expect, vi, afterEach } from 'vitest';
import { api, qs, latestOnly, setUnauthorizedHandler } from './api';

describe('qs', () => {
  it('跳过空值并拼为查询串', () => {
    expect(qs({ q: 'vue', page: 2, empty: '', nil: null, undef: undefined })).toBe('?q=vue&page=2');
  });
  it('全空时返回空串', () => {
    expect(qs({})).toBe('');
    expect(qs(null)).toBe('');
  });
});

describe('latestOnly', () => {
  it('慢的旧响应被标记为过期', () => {
    const guard = latestOnly();
    const first = guard.begin();
    const second = guard.begin();
    expect(guard.isStale(first)).toBe(true);
    expect(guard.isStale(second)).toBe(false);
  });
});

describe('api', () => {
  const origFetch = global.fetch;
  afterEach(() => {
    global.fetch = origFetch;
    setUnauthorizedHandler(null);
    vi.restoreAllMocks();
  });

  function mockFetch(status, body) {
    global.fetch = vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: async () => body,
    });
  }

  it('GET 返回解析后的 JSON', async () => {
    mockFetch(200, { ok: true, items: [1, 2] });
    const data = await api.get('/api/demo');
    expect(data.items).toEqual([1, 2]);
    expect(global.fetch).toHaveBeenCalledWith('/api/demo', expect.objectContaining({ method: 'GET' }));
  });

  it('非 2xx 抛出携带状态码的 ApiError', async () => {
    mockFetch(502, { error: '上游不可用' });
    const err = await api.get('/api/demo').catch((e) => e);
    expect(err.status).toBe(502);
    expect(err.message).toBe('上游不可用');
  });

  it('404 无 error 字段时使用兜底文案', async () => {
    mockFetch(404, null);
    const err = await api.get('/api/demo').catch((e) => e);
    expect(err.status).toBe(404);
    expect(err.message).toBe('请求失败 (404)');
  });

  it('401 触发统一未授权回调', async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    mockFetch(401, { error: '未登录' });
    await api.get('/api/admin/settings').catch(() => {});
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it('网络失败抛出网络错误', async () => {
    global.fetch = vi.fn().mockRejectedValue(new TypeError('network down'));
    const err = await api.get('/api/demo').catch((e) => e);
    expect(err.status).toBe(0);
    expect(err.message).toContain('网络请求失败');
  });
});
