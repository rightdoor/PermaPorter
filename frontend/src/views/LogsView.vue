<script setup>
// 代理请求日志：按生态分 tab（各生态各自独立 Ring，行内带生态标记），
// 每个 Ring 最近 200 条，每 5s 自动刷新。
import { onMounted, onUnmounted, ref, watch } from 'vue';
import { api, latestOnly } from '../lib/api';
import { fmtClock } from '../lib/format';
import EmptyState from '../components/EmptyState.vue';

const eco = ref('go'); // go | npm | py | mvn
const entries = ref(null);
const loadError = ref(''); // 首次加载失败：给出错误态与重试，而不是无限"加载中"
let pollTimer = null;
let failures = 0;
const guard = latestOnly(); // 竞态守卫：快速切生态时丢弃慢的旧生态响应

const ECO_LABEL = { go: 'Go 模块', npm: 'npm 包', py: 'Py 项目', mvn: 'Maven 构件' };

// 页面不可见时暂停轮询；恢复可见立即刷一次。失败按指数退避，避免上游抖动时刷屏。
function schedule(delay) {
  clearTimeout(pollTimer);
  if (document.visibilityState === 'hidden') return;
  pollTimer = setTimeout(load, delay);
}

async function load() {
  const id = guard.begin();
  const e = eco.value; // 请求发起时的生态：await 期间切换 eco 后旧响应直接丢弃
  try {
    const data = await api.get(`/api/admin/logs${e === 'go' ? '' : `?eco=${e}`}`);
    if (guard.isStale(id) || e !== eco.value) return;
    entries.value = data.entries || [];
    loadError.value = '';
    failures = 0;
  } catch {
    if (guard.isStale(id) || e !== eco.value) return;
    failures = Math.min(failures + 1, 4);
    if (!entries.value) loadError.value = '日志加载失败，稍后自动重试';
  }
  if (!guard.isStale(id)) schedule(Math.min(5000 * 2 ** failures, 60000));
}

function onVisibility() {
  if (document.visibilityState === 'visible') load();
}

// 切生态立即重拉（三个 Ring 独立，kind 语义不同）
watch(eco, () => {
  entries.value = null;
  loadError.value = '';
  load();
});

onMounted(() => {
  load();
  document.addEventListener('visibilitychange', onVisibility);
});
onUnmounted(() => {
  clearTimeout(pollTimer);
  document.removeEventListener('visibilitychange', onVisibility);
});

// kind 语义各生态不同：go: meta/zip/sumdb/pass；npm: meta/tarball/pass/audit；
// py: index/file；mvn: meta/file。按当前生态取文案，未知 kind 原样展示。
const kindNames = {
  go: { meta: '元数据', zip: 'zip', sumdb: 'sumdb', pass: '透传' },
  npm: { meta: '元数据', tarball: 'tarball', pass: '透传', audit: 'audit' },
  py: { index: '锚点页', file: '发行文件' },
  mvn: { meta: '元数据', file: '构件文件' },
};

function kindName(kind) {
  return kindNames[eco.value]?.[kind] || kind;
}

function kindClass(kind) {
  if (kind === 'sumdb' || kind === 'audit') return 'ui-chip-line';
  if (kind === 'pass') return 'ui-chip-warn';
  return '';
}
</script>

<template>
  <div>
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">请求日志</h1>
        <p class="ui-page-sub">最近 200 条 {{ ECO_LABEL[eco] }}代理请求（内存保留，重启清空），每 5 秒自动刷新。</p>
      </div>
      <span class="ui-live"><span class="ui-dot ui-dot-ok"></span>实时刷新</span>
    </header>

    <div class="ui-tabs" role="tablist" aria-label="选择生态" style="margin-bottom: 14px">
      <button class="ui-tab" :class="{ 'is-active': eco === 'go' }" type="button" role="tab" :aria-selected="eco === 'go'" @click="eco = 'go'">
        GO
      </button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'npm' }" type="button" role="tab" :aria-selected="eco === 'npm'" @click="eco = 'npm'">
        NPM
      </button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'py' }" type="button" role="tab" :aria-selected="eco === 'py'" @click="eco = 'py'">
        PYTHON
      </button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'mvn' }" type="button" role="tab" :aria-selected="eco === 'mvn'" @click="eco = 'mvn'">
        JAVA
      </button>
    </div>

    <section class="ui-card">
      <div v-if="entries && entries.length" class="ui-table-wrap">
        <table class="ui-table ui-table-wide">
          <thead>
            <tr>
              <th>时间</th>
              <th>类型</th>
              <th>对象</th>
              <th>命中</th>
              <th>耗时</th>
              <th>来源</th>
              <th>备注</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(e, i) in entries" :key="i">
              <td class="num ui-mono ui-muted">{{ fmtClock(e.time) }}</td>
              <td><span class="ui-chip" :class="kindClass(e.kind)">{{ kindName(e.kind) }}</span></td>
              <td><span class="ui-mono">{{ e.pkg }}</span></td>
              <td>
                <span class="ui-chip" :class="e.hit ? 'ui-chip-ok' : 'ui-chip-line'">{{ e.hit ? 'HIT' : 'MISS' }}</span>
              </td>
              <td class="num ui-mono">{{ e.durMs }}ms</td>
              <td class="num ui-mono ui-muted">{{ e.ip }}</td>
              <td class="ui-muted" style="font-size: 12px">{{ e.err }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState
        v-else-if="entries"
        flat
        ico="format-list-bulleted"
        title="暂无请求记录"
        :sub="`客户端通过本镜像发起任何 ${ECO_LABEL[eco]}拉取请求后，日志将显示在这里。`"
      />
      <div v-else-if="loadError" class="ui-empty">
        <p class="ui-empty-title">{{ loadError }}</p>
        <p><button class="ui-btn ui-btn-primary ui-btn-sm" type="button" @click="load">立即重试</button></p>
      </div>
      <div v-else class="ui-empty">
        <p class="ui-empty-title">加载中…</p>
      </div>
    </section>
  </div>
</template>
