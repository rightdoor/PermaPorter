// 展示格式化：KB 进制、千分位与本地时区时间。

export function fmtBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let f = n;
  let i = 0;
  while (f >= 1024 && i < units.length - 1) {
    f /= 1024;
    i++;
  }
  if (i === 0) return `${n} B`;
  return `${f.toFixed(1)} ${units[i]}`;
}

export function fmtNum(n) {
  return Number(n || 0).toLocaleString('en-US');
}

// README 摘要为服务端生成的纯文本，这里兜底再清洗一次 HTML 片段。
export function plainText(s, max = 0) {
  let text = String(s || '')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;|&apos;/gi, "'")
    .replace(/&nbsp;/gi, ' ')
    .replace(/&amp;/gi, '&')
    .replace(/<[^>]*>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
  if (max > 0 && text.length > max) text = `${text.slice(0, max)}…`;
  return text;
}

// 库内时间串均为 UTC；go 上游时间为 RFC3339。解析为本地时间后格式化。
function parseTime(s) {
  if (!s) return null;
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(s)) {
    return new Date(`${s.replace(' ', 'T')}Z`);
  }
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
}

const p2 = (n) => String(n).padStart(2, '0');

// "01-02 15:04"
export function fmtTimeShort(s) {
  const d = parseTime(s);
  if (!d) return s || '';
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

// "2006-01-02 15:04:05"
export function fmtTimeLong(s) {
  const d = parseTime(s);
  if (!d) return s || '';
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

// "15:04:05"（日志页）
export function fmtClock(s) {
  const d = parseTime(s);
  if (!d) return s || '';
  return `${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

// "01-02 15:04:05"（预热任务开始时间，RFC3339 输入）
export function fmtStamp(s) {
  const d = parseTime(s);
  if (!d) return s || '';
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}
