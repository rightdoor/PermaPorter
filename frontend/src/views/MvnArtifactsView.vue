<script setup>
// JAVA：Maven 构件缓存表格 + 页内直搜 + 服务端分页。
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
  fetchPage: (signal) => api.get(`/api/mvnartifacts${qs({ q: query.value, page: route.query.page || 1 })}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const query = computed(() => String(route.query.q || ''));
const q = ref(query.value);
// Maven 仓库基地址由服务端注入（前端不硬编码端口），缺失时按主机名回退
const mavenRepo = computed(() => app.mavenRepo || '');

watch(
  () => route.query.q,
  (value) => {
    const next = String(value || '');
    if (next !== q.value) q.value = next;
  },
);

function submit() {
  const keyword = q.value.trim();
  router.replace({ name: 'maven', query: keyword ? { q: keyword } : {} });
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
        <h1 class="ui-page-title">JAVA</h1>
        <p class="ui-page-sub">
          {{ query ? `“${query}” 的缓存结果 · ${fmtNum(total)} 个构件` : 'Maven 仓库生态（Maven / Gradle 同协议）· 按最近缓存排序 · 点击构件查看文件' }}
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
          placeholder="搜索 groupId / artifactId"
          aria-label="搜索构件"
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
              <th>构件</th>
              <th>最新版本</th>
              <th>已缓存文件</th>
              <th>体积</th>
              <th>最近缓存</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="a in items" :key="a.name">
              <td>
                <div class="ui-cell-main">
                  <span class="ui-ico"><Icon name="database" /></span>
                  <RouterLink :to="{ name: 'mvnartifact', params: { coords: a.name } }" class="ui-cell-title ui-mono">
                    {{ a.name }}
                  </RouterLink>
                </div>
              </td>
              <td class="num ui-mono">{{ a.latestVersion || '-' }}</td>
              <td class="num">{{ a.cachedFileCount }}<span class="ui-muted"> / {{ a.fileCount }}</span></td>
              <td class="num">{{ a.totalSize > 0 ? fmtBytes(a.totalSize) : '—' }}</td>
              <td class="num ui-muted">{{ a.lastCached ? fmtTimeShort(a.lastCached) : '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="ui-table-foot">
        <span>第 {{ page }} / {{ totalPages }} 页 · 共 {{ fmtNum(total) }} 个构件</span>
        <Pager :page="page" :total-pages="totalPages" @go="goPage" />
      </div>
    </div>

    <div v-else-if="items" class="ui-card">
      <EmptyState
        ico="database"
        :title="query ? `没有找到与「${query}」匹配的缓存构件` : '还没有缓存任何构件'"
        :sub="query ? '试试更短的关键词，或在客户端构建后自动出现在这里。' : '将 Maven/Gradle 仓库指向本服务并构建一次，镜像会自动开始工作。'"
      >
        <template v-if="query" #actions>
          <button class="ui-btn ui-btn-outline ui-btn-sm" type="button" @click="clear">清空搜索</button>
        </template>
        <CmdBox v-if="!query && mavenRepo" class="ui-mt-16" :cmd="`mvn dependency:get -DremoteRepositories=mirror::default::${mavenRepo}`" />
      </EmptyState>
    </div>

    <div v-else class="ui-card">
      <div class="ui-empty">
        <p class="ui-empty-title">加载中…</p>
      </div>
    </div>
  </div>
</template>
