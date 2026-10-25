#!/usr/bin/env node
/**
 * 生成内联图标模块（替代原模板 sprite，纳入前端 pnpm 工具链）。
 *
 * 用法：pnpm icons
 * - SVG 源保存于 src/icons/<name>.svg（随仓库提交，运行时零外部调用）；
 *   缺失时依次尝试镜像下载（仅开发期需要网络）；
 * - 生成 src/icons.js（图标名 -> SVG 内部标记），由 Icon.vue 内联渲染。
 * MDI 图标许可：Pictogrammers Free License（https://pictogrammers.com/docs/general/license/）。
 */
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const SRC = resolve(here, '../icons');
const OUT = resolve(here, '../icons.js');

// 页面用到的全部图标（MDI 名称）
const ICONS = [
  'view-dashboard', 'package-variant-closed', 'fire', 'format-list-bulleted',
  'cog', 'open-in-new', 'magnify', 'weather-sunny', 'weather-night',
  'close', 'chevron-left', 'chevron-right', 'arrow-right', 'arrow-up',
  'source-commit', 'database', 'clock-outline', 'bullseye-arrow',
  'harddisk', 'cloud-download-outline', 'web', 'source-repository',
  'download', 'refresh', 'check-circle', 'close-circle', 'alert',
  'content-copy', 'console-line', 'hexagon-multiple',
  'home', 'login', 'logout',
];

const MIRRORS = [
  'https://raw.githubusercontent.com/Templarian/MaterialDesign-SVG/master/svg/{name}.svg',
  'https://cdn.jsdelivr.net/npm/@mdi/svg@7.4.47/svg/{name}.svg',
  'https://unpkg.com/@mdi/svg@7.4.47/svg/{name}.svg',
];

async function load(name) {
  try {
    return readFileSync(join(SRC, `${name}.svg`), 'utf8');
  } catch {
    /* 缓存缺失，走镜像 */
  }
  let lastErr;
  for (const tpl of MIRRORS) {
    const url = tpl.replace('{name}', name);
    try {
      const res = await fetch(url);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.text();
      writeFileSync(join(SRC, `${name}.svg`), data);
      return data;
    } catch (err) {
      lastErr = err;
      console.error(`  mirror failed: ${url} (${err.message})`);
    }
  }
  throw new Error(`下载失败 ${name}: ${lastErr}`);
}

function innerBody(svg) {
  return svg
    .replace(/<title>.*?<\/title>/gs, '')
    .replace(/<!--.*?-->/gs, '')
    .match(/<svg[^>]*>([\s\S]*)<\/svg>/)?.[1]
    ?.trim() ?? svg.trim();
}

const entries = [];
for (const name of ICONS) {
  const body = innerBody(await load(name));
  entries.push(`  ${JSON.stringify(name)}: ${JSON.stringify(body)},`);
  console.log(`ok ${name} (${body.length}B)`);
}

const mod =
  `// 由 scripts/build-icons.mjs 自动生成（pnpm icons），勿手改。
// MDI 图标许可：Pictogrammers Free License（https://pictogrammers.com/docs/general/license/）。
export const ICONS = {
${entries.join('\n')}
};
`;
writeFileSync(OUT, mod);
console.log(`icons -> ${OUT} (${mod.length}B)`);
