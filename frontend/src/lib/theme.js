// 日/夜主题切换：data-theme 挂在 <html> 上，由 Go 输出的外壳脚本预置避免闪烁。
export function currentTheme() {
  return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
}

export function toggleTheme() {
  const next = currentTheme() === 'dark' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  try {
    localStorage.setItem('permaporter-theme', next);
  } catch {
    /* 隐私模式等场景忽略 */
  }
}
