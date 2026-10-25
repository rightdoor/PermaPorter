<script setup>
// 后台包列表：搜索（300ms 防抖）、表头排序（服务端白名单）、分页、版本管理抽屉。
import { onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { api, qs } from '../lib/api';
import { usePagedList } from '../lib/composables';
import { notifyError } from '../lib/toast';
import { fmtBytes, fmtTimeShort } from '../lib/format';
import Icon from '../components/Icon.vue';
import Pager from '../components/Pager.vue';
import EmptyState from '../components/EmptyState.vue';
import VersionsModal from '../components/VersionsModal.vue';

const state = reactive({ q: '', sort: '', dir: '', page: 1 });
// 分页列表内核：竞态守卫 + 旧请求取消 + 错误/空态分离（错误保留重试入口）
const { loading, items: rows, error, total, totalPages, load, retry } = usePagedList({
  fetchPage: (signal) => api.get(`/api/admin/packages${qs({
    q: state.q, page: state.page, sort: state.sort, dir: state.dir,
  })}`, { signal }),
  onError: (e) => notifyError(e.message),
});

const modalPkg = ref('');
let modalKey = 0;

function sortBy(key) {
  if (state.sort === key && state.dir === 'desc') {
    state.dir = 'asc';
  } else {
    state.sort = key;
    state.dir = 'desc';
  }
  state.page = 1;
}

function arrow(key) {
  if (state.sort !== key) return '';
  return state.dir === 'asc' ? '▲' : '▼';
}

function goPage(n) {
  state.page = Math.min(Math.max(1, n), totalPages.value);
}

let debounceTimer = null;
watch(() => state.q, () => {
  clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    // page 变化会触发 watch 的 load：page 已是 1 时才需要显式加载，避免双请求
    if (state.page !== 1) state.page = 1;
    else load();
  }, 300);
});
watch(() => [state.sort, state.dir, state.page], load);

onMounted(load);
onUnmounted(() => clearTimeout(debounceTimer));

function openModal(pkgName) {
  modalPkg.value = pkgName;
  modalKey++;
}
function onModalChanged(pkgRemoved) {
  modalPkg.value = '';
  if (pkgRemoved) load();
}
</script>

<template>
  <div>
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">包管理</h1>
        <p class="ui-page-sub">共 {{ total }} 个已缓存包 · 管理版本与缓存状态</p>
      </div>
    </header>

    <div class="ui-card">
      <div class="ui-card-head">
        <div class="ui-search" style="max-width: 360px">
          <Icon name="magnify" />
          <input v-model="state.q" class="ui-input" type="search" placeholder="搜索包名或描述" aria-label="搜索包">
        </div>
        <span class="ui-muted" style="font-size: 12px">{{ total }} 个</span>
      </div>

      <div v-if="error">
        <EmptyState
          flat
          ico="alert"
          title="加载失败"
          :sub="error"
        >
          <template #actions>
            <button class="ui-btn ui-btn-primary ui-btn-sm" type="button" @click="retry">重试</button>
          </template>
        </EmptyState>
      </div>

      <div v-else-if="rows && rows.length" class="ui-table-wrap" :class="{ 'load-dim': loading }">
        <table class="ui-table ui-table-wide">
          <thead>
            <tr>
              <th>
                <button class="ui-th-sort" :class="{ 'is-sorted': state.sort === 'name' }" type="button" title="按包名排序" @click="sortBy('name')">
                  包<span class="sort-arrow">{{ arrow('name') }}</span>
                </button>
              </th>
              <th>
                <button class="ui-th-sort" :class="{ 'is-sorted': state.sort === 'versions' }" type="button" title="按版本数排序" @click="sortBy('versions')">
                  版本数<span class="sort-arrow">{{ arrow('versions') }}</span>
                </button>
              </th>
              <th>最新版本</th>
              <th>
                <button class="ui-th-sort" :class="{ 'is-sorted': state.sort === 'size' }" type="button" title="按缓存体积排序" @click="sortBy('size')">
                  体积<span class="sort-arrow">{{ arrow('size') }}</span>
                </button>
              </th>
              <th>
                <button class="ui-th-sort" :class="{ 'is-sorted': state.sort === 'time' }" type="button" title="按最近缓存时间排序" @click="sortBy('time')">
                  最近缓存<span class="sort-arrow">{{ arrow('time') }}</span>
                </button>
              </th>
              <th style="text-align: right">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in rows" :key="p.pkgName">
              <td>
                <div class="ui-cell-main">
                  <span class="ui-ico"><Icon name="package-variant-closed" /></span>
                  <RouterLink :to="{ name: 'package', params: { pkg: p.pkgName } }" class="ui-cell-title ui-mono">
                    {{ p.pkgName }}
                  </RouterLink>
                </div>
              </td>
              <td class="num">{{ p.versionCount }}</td>
              <td class="num">
                <span v-if="p.latestVersion" class="ui-chip ui-chip-line ui-mono">{{ p.latestVersion }}</span>
                <span v-else class="ui-muted">-</span>
              </td>
              <td class="num">{{ p.totalSize > 0 ? fmtBytes(p.totalSize) : '—' }}</td>
              <td class="num ui-muted">{{ fmtTimeShort(p.lastCached) }}</td>
              <td>
                <div class="ui-table-actions">
                  <RouterLink class="ui-btn ui-btn-ghost ui-btn-sm" :to="{ name: 'package', params: { pkg: p.pkgName } }" title="前往公开页面">
                    <Icon name="open-in-new" /> 公开页
                  </RouterLink>
                  <button class="ui-btn ui-btn-ghost ui-btn-sm" type="button" title="管理已缓存版本" @click="openModal(p.pkgName)">
                    <Icon name="cog" /> 操作
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <EmptyState
        v-else-if="rows"
        flat
        ico="package-variant-closed"
        :title="state.q ? `没有匹配「${state.q}」的已缓存包` : '还没有已缓存的包'"
        :sub="state.q ? '换个关键字试试，或在客户端直接安装该包。' : '在客户端把 registry 指向本服务并安装包，或用依赖预热批量拉取。'"
      >
        <template #actions>
          <RouterLink class="ui-btn ui-btn-primary ui-btn-sm" :to="{ name: 'preheat' }">
            <Icon name="fire" /> 去依赖预热
          </RouterLink>
        </template>
      </EmptyState>

      <div v-else class="ui-empty">
        <p class="ui-empty-title">加载中…</p>
      </div>

      <div v-if="rows && rows.length" class="ui-table-foot">
        <span>第 {{ state.page }} / {{ totalPages }} 页 · 共 {{ total }} 个包</span>
        <Pager :page="state.page" :total-pages="totalPages" @go="goPage" />
      </div>
    </div>

    <VersionsModal
      kind="npm"
      v-if="modalPkg"
      :key="modalKey"
      :name="modalPkg"
      @close="modalPkg = ''"
      @changed="onModalChanged"
    />
  </div>
</template>
