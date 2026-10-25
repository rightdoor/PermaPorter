#!/usr/bin/env node
/**
 * 生成内联图标模块（数据源：Iconify MDI 集）。
 *
 * 用法：pnpm icons
 * - 从本地 node_modules 的 @iconify-json/mdi 提取图标（离线生成，零网络请求）；
 * - 生成 src/icons.js（图标名 -> SVG 内部标记），由 Icon.vue 内联渲染。
 * MDI 图标许可：Pictogrammers Free License（https://pictogrammers.com/docs/general/license/）。
 */
import { writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(here, '../icons.js');

// 页面用到的全部图标（MDI 名称）
const ICONS = [
  'view-dashboard', 'package-variant-closed', 'fire', 'format-list-bulleted',
  'cog', 'open-in-new', 'magnify', 'weather-sunny', 'weather-night',
  'close', 'chevron-left', 'chevron-right', 'arrow-right', 'arrow-up',
  'arrow-down', 'source-commit', 'database', 'clock-outline',
  'bullseye-arrow', 'harddisk', 'cloud-download-outline', 'web',
  'source-repository', 'download', 'refresh', 'check-circle', 'close-circle',
  'alert', 'content-copy', 'console-line', 'hexagon-multiple',
  'home', 'login', 'logout',
];

const require = createRequire(import.meta.url);
const mdi = require('@iconify-json/mdi/icons.json');

function iconBody(name) {
  const icon = mdi.icons[name];
  if (icon) return icon.body;
  const parent = mdi.aliases?.[name]?.parent;
  if (parent && mdi.icons[parent]) return mdi.icons[parent].body;
  throw new Error(`图标不存在于 @iconify-json/mdi：${name}`);
}

const entries = ICONS.map((name) => {
  const body = iconBody(name);
  const icon = mdi.icons[name];
  const w = icon?.width ?? mdi.width ?? 24;
  const h = icon?.height ?? mdi.height ?? 24;
  if (w !== 24 || h !== 24) {
    throw new Error(`图标 ${name} 尺寸为 ${w}x${h}，Icon.vue 的 viewBox 需同步调整`);
  }
  return `  ${JSON.stringify(name)}: ${JSON.stringify(body)},`;
});

const mod =
  `// 由 scripts/build-icons.mjs 自动生成（pnpm icons），勿手改。
// 数据源：Iconify MDI 集（@iconify-json/mdi，本地离线生成，运行时零外部调用）。
// MDI 图标许可：Pictogrammers Free License（https://pictogrammers.com/docs/general/license/）。
export const ICONS = {
${entries.join('\n')}
};`;
writeFileSync(OUT, mod, 'utf8');
console.log(`icons -> ${OUT} (${ICONS.length} icons from @iconify-json/mdi)`);
