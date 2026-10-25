<script setup>
// PYTHON：真实缓存项目表格 + 页内直搜 + 服务端分页。
// 搜索在本页工具栏完成（服务端 q 过滤），独立搜索页已下线。
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api, qs } from '../lib/api';
import { usePagedList } from '../lib/composables';
import { notifyError } from '../lib/toast';
import { fmtBytes, fmtNum, fmtTimeShort } from '../lib/format';
import { app } from '../lib/store';
import Icon from '../components/Icon.vue';
import Pager from '../components/Pager.vue';
import EmptyState from '../components/EmptyState.vue';
import CmdBox from '../components/CmdBox.vue';

const route = useRoute();
const router = useRouter();

// 分页列表内核：竞态守卫 + 旧请求取消 + 错误/空态分离（错误保留重试入口）
const { loading, items, error, total, page, totalPages, load, retry } = usePagedList({
  fetchPage: (signal) => api.get(`/api/pyprojects${qs({ q: query.value, page: route.query.page || 1 })}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const query = computed(() => String(route.query.q || ''));
const q = ref(query.value);
// PyPI Simple 基地址由服务端注入（前端不硬编码端口），缺失时按主机名回退
const pyIndex = computed(() => app.pyIndex || '');

watch(
  () => route.query.q,
  (value) => {
    const next = String(value || '');
    if (next !== q.value) q.value = next;
  },
);

function submit() {
  const keyword = q.value.trim();
  router.replace({ name: 'pythonsimple', query: keyword ? { q: keyword } : {} });
}

function clear() {
  q.value = '';
  submit();
}

function goPage(n) {
  router.push({ query: { ...route.query, page: n > 1 ? n : undefined } });
}

watch(() => [query.value, route.query.page], load, { immediate: false });
onMounted(load);
</script>

<template>
  <div class="ui-container ui-page">
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">PYTHON</h1>
        <p class="ui-page-sub">
          {{ query ? `“${query}” 的缓存结果 · ${fmtNum(total)} 个项目` : 'PyPI Simple 生态 · 按最近缓存排序 · 点击项目查看发行文件' }}
        </p>
      </div>
    </header>

    <form class="ui-toolbar" role="search" @submit.prevent="submit">
      <div class="ui-search" style="max-width: 460px">
        <Icon name="magnify" />
        <input
          v-model="q"
          class="ui-input"
          type="search"
          placeholder="搜索项目名或摘要"
          aria-label="搜索项目"
        >
      </div>
      <button class="ui-btn ui-btn-primary" type="submit">搜索</button>
      <button v-if="q.trim()" class="ui-btn ui-btn-ghost" type="button" @click="clear">清除</button>
      <span class="ui-toolbar-spacer"></span>
      <span class="ui-muted" style="font-size: 12px">共 {{ fmtNum(total) }} 个</span>
    </form>

    <div v-if="error" class="ui-card">
      <EmptyState ico="alert" title="加载失败" :sub="error">
        <template #actions>
          <button class="ui-btn ui-btn-primary ui-btn-sm" type="button" @click="retry">重试</button>
        </template>
      </EmptyState>
    </div>

    <div v-else-if="items && items.length" class="ui-card" :class="{ 'load-dim': loading }">
      <div class="ui-table-wrap">
        <table class="ui-table ui-table-wide">
          <thead>
            <tr>
              <th>项目</th>
              <th>最新版本</th>
              <th>已缓存文件</th>
              <th>体积</th>
              <th>最近缓存</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in items" :key="p.projectName">
              <td>
                <div class="ui-cell-main">
                  <span class="ui-ico"><Icon name="hexagon-multiple" /></span>
                  <RouterLink :to="{ name: 'pyproject', params: { name: p.projectName } }" class="ui-cell-title ui-mono">
                    {{ p.projectName }}
                  </RouterLink>
                </div>
              </td>
              <td class="num ui-mono">{{ p.latestVersion || '-' }}</td>
              <td class="num">{{ p.cachedFileCount }}<span class="ui-muted"> / {{ p.fileCount }}</span></td>
              <td class="num">{{ p.totalSize > 0 ? fmtBytes(p.totalSize) : '—' }}</td>
              <td class="num ui-muted">{{ p.lastCached ? fmtTimeShort(p.lastCached) : '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="ui-table-foot">
        <span>第 {{ page }} / {{ totalPages }} 页 · 共 {{ fmtNum(total) }} 个项目</span>
        <Pager :page="page" :total-pages="totalPages" @go="goPage" />
      </div>
    </div>

    <div v-else-if="items" class="ui-card">
      <EmptyState
        ico="hexagon-multiple"
        :title="query ? `没有找到与「${query}」匹配的缓存项目` : '还没有缓存任何项目'"
        :sub="query ? '试试更短的关键词，或在客户端安装后自动出现在这里。' : '将 pip 源指向本服务并安装第一个项目，镜像会自动开始工作。'"
      >
        <template v-if="query" #actions>
          <button class="ui-btn ui-btn-outline ui-btn-sm" type="button" @click="clear">清空搜索</button>
        </template>
        <CmdBox v-if="!query && pyIndex" class="ui-mt-16" :cmd="`pip config set global.index-url ${pyIndex}`" />
      </EmptyState>
    </div>

    <div v-else class="ui-card">
      <div class="ui-empty">
        <p class="ui-empty-title">加载中…</p>
      </div>
    </div>
  </div>
</template>
