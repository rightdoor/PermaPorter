// 全局轻提示（替代旧 htmx 通知片段）：成功自动消失，错误停留更久。
import { reactive } from 'vue';

export const toasts = reactive([]);

let seq = 0;

export function notify(message, type = 'success') {
  const id = ++seq;
  toasts.push({ id, message, type });
  setTimeout(() => dismiss(id), type === 'error' ? 6000 : 4200);
}

export function notifyError(message) {
  notify(message, 'error');
}

export function dismiss(id) {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}
