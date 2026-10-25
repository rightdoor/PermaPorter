<script setup>
// 公开模块详情：go.mod 源码与 README 为主；版本与依赖收进抽屉，版本切换保留 ?v= 深链。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api, qs } from '../lib/api';
import { useDetailResource } from '../lib/composables';
import { app } from '../lib/store';
import { fmtBytes, fmtTimeLong } from '../lib/format';
import { notify, notifyError } from '../lib/toast';
import Icon from '../components/Icon.vue';
import CmdBox from '../components/CmdBox.vue';
import EmptyState from '../components/EmptyState.vue';

const route = useRoute();
const router = useRouter();
// 详情资源内核：竞态守卫 + 旧请求取消 + loading/notFound/error 三态（含重试）
const { loading, notFound, error, detail, load, retry } = useDetailResource({
  fetch: (signal) => api.get(`/api/module/${modulePath.value}${qs({ v: route.query.v })}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const drawer = ref(''); // '' | 'versions' | 'deps'
const refreshing = ref(false);
const host = computed(() => app.goProxy || '');

const modulePath = computed(() => String(route.params.path || ''));
const cur = computed(() => detail.value?.current || null);

// 安装命令两种形态：拉取依赖 / 仅下载到模块缓存
const cmds = ['get', 'download'];
const cmdMode = ref('get');
const ver = computed(() => cur.value?.version || detail.value?.module?.latestVersion || '');
const installCmd = computed(() => {
  const n = modulePath.value;
  if (cmdMode.value === 'download') {
    return ver.value ? `go mod download ${n}@${ver.value}` : `go mod download ${n}`;
  }
  return ver.value ? `go get ${n}@${ver.value}` : `go get ${n}`;
});

const versionCount = computed(() => detail.value?.versionTotal ?? detail.value?.versions?.length ?? 0);

const reqs = computed(() => cur.value?.requirements || []);
const indirectCount = computed(() => reqs.value.filter((r) => r.indirect).length);

// 服务端已按 semver 降序返回；把当前版本置顶。
const sortedVersions = computed(() => {
  const list = [...(detail.value?.versions || [])];
  const current = cur.value?.version;
  const index = current ? list.findIndex((v) => v.version === current) : -1;
  if (index > 0) list.unshift(list.splice(index, 1)[0]);
  return list;
});

const drawerTitle = computed(() => (drawer.value === 'versions' ? '切换版本' : '依赖 (require)'));
const drawerSub = computed(() => {
  if (drawer.value === 'versions') {
    const truncated = detail.value?.versionsTruncated ? ' · 列表仅展示已缓存与最近版本' : '';
    return `共 ${versionCount.value} 个版本 · 当前 ${cur.value?.version || '-'}${truncated}`;
  }
  return reqs.value.length ? `共 ${reqs.value.length} 个依赖（间接 ${indirectCount.value} 个）` : '当前版本没有依赖记录';
});

async function refreshMeta() {
  refreshing.value = true;
  try {
    await api.post(`/api/admin/module-refresh${qs({ module: modulePath.value })}`);
    notify(`已强制刷新 ${modulePath.value} 的元数据`);
    await load();
  } catch (e) {
    notifyError(`刷新失败: ${e.message}`);
  } finally {
    refreshing.value = false;
  }
}

function changeVersion(version) {
  drawer.value = '';
  if (!detail.value || version === cur.value?.version) return;
  router.replace({ query: { ...route.query, v: version } });
}

function onKeydown(event) {
  if (event.key === 'Escape') drawer.value = '';
}

watch(() => [modulePath.value, route.query.v], load);
onMounted(() => {
  load();
  window.addEventListener('keydown', onKeydown);
});
onUnmounted(() => window.removeEventListener('keydown', onKeydown));
</script>

<template>
  <!-- 模块不存在：安装引导 -->
  <div v-if="notFound" class="ui-container ui-page">
    <div class="ui-card" style="max-width: 760px; margin-inline: auto">
      <div class="ui-card-body" style="padding: 40px; text-align: center">
        <p class="ui-mono ui-muted" style="margin: 0; font-size: 64px; font-weight: 700; letter-spacing: -0.04em; line-height: 1">404</p>
        <h1 class="ui-page-title" style="margin-top: 14px">模块不存在</h1>
        <p class="ui-page-sub">
          「<span class="ui-mono">{{ modulePath }}</span>」尚未被缓存，或者路径写错了。
        </p>
        <p class="ui-hint" style="margin-top: 18px">本镜像按需缓存 —— 直接在客户端拉取它试试：</p>
        <CmdBox class="ui-mt-8" :cmd="`go mod download ${modulePath}`" />
        <p class="ui-hint ui-mt-8">拉取完成后刷新此页，即可查看 go.mod、版本与 README。</p>
        <div class="ui-row" style="justify-content: center; flex-wrap: wrap; margin-top: 24px">
          <RouterLink :to="{ name: 'home' }" class="ui-btn ui-btn-primary">返回首页</RouterLink>
          <RouterLink :to="{ name: 'goproxy' }" class="ui-btn ui-btn-ghost">浏览已缓存模块</RouterLink>
        </div>
      </div>
    </div>
  </div>

  <!-- 加载失败：原因 + 重试 -->
  <div v-else-if="error" class="ui-container ui-page">
    <div class="ui-card">
      <EmptyState ico="alert" title="加载失败" :sub="error">
        <template #actions>
          <button class="ui-btn ui-btn-primary ui-btn-sm" type="button" @click="retry">重试</button>
        </template>
      </EmptyState>
    </div>
  </div>

  <!-- 加载中 -->
  <div v-else-if="loading" class="ui-container ui-page">
    <div class="ui-card">
      <div class="ui-empty">
        <p class="ui-empty-title">正在加载 {{ modulePath }}…</p>
      </div>
    </div>
  </div>

  <div v-else-if="detail" class="ui-container ui-page">
    <RouterLink :to="{ name: 'goproxy' }" class="ui-back">
      <Icon name="chevron-left" />
      返回 GO
    </RouterLink>

    <header class="ui-page-head ui-detail-head">
      <div>
        <div class="ui-row" style="flex-wrap: wrap">
          <h1 class="ui-page-title ui-mono">{{ modulePath }}</h1>
          <span v-if="cur?.version" class="ui-chip ui-chip-brand ui-mono">{{ cur.version }}</span>
          <span class="ui-chip" :class="(cur?.zipSizeBytes || 0) > 0 ? 'ui-chip-ok' : 'ui-chip-line'">
            {{ (cur?.zipSizeBytes || 0) > 0 ? '已缓存' : '仅元数据' }}
          </span>
          <span v-if="cur?.pseudo" class="ui-chip ui-chip-line">pseudo</span>
          <span class="ui-chip">已缓存 {{ detail.cachedCount }} 个版本 · {{ fmtBytes(detail.cachedSize) }}</span>
        </div>
        <div class="ui-row ui-mt-8" style="flex-wrap: wrap">
          <span v-if="detail.module.latestVersion" class="ui-chip ui-chip-line ui-mono">latest {{ detail.module.latestVersion }}</span>
          <span v-if="cur?.goVersion" class="ui-chip ui-chip-line">go {{ cur.goVersion }}</span>
          <span class="ui-chip ui-chip-line">{{ versionCount }} 个版本</span>
        </div>
      </div>

      <div class="ui-page-actions">
        <button class="ui-btn ui-btn-outline" type="button" title="查看全部版本并切换" @click="drawer = 'versions'">
          <Icon name="format-list-bulleted" />
          版本 {{ versionCount }}
        </button>
        <button class="ui-btn ui-btn-outline" type="button" title="查看 require 依赖" @click="drawer = 'deps'">
          <Icon name="hexagon-multiple" />
          依赖 {{ reqs.length }}
        </button>
      </div>
    </header>

    <div class="ui-detail-grid">
      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">go.mod / README</h2>
            <p class="ui-card-sub">当前版本 {{ cur?.version || '-' }} 的模块定义与说明</p>
          </div>
          <span class="ui-muted ui-mono" style="font-size: 11.5px">{{ modulePath }}</span>
        </div>
        <div v-if="detail.readmeHTML" class="ui-card-body md-body ui-readme" v-html="detail.readmeHTML"></div>
        <pre v-else-if="detail.goMod" class="ui-card-body gomod-src">{{ detail.goMod }}</pre>
        <div v-else class="ui-card-body">
          <p class="ui-muted" style="margin: 0">
            缓存 zip 后将在此展示 README，当前仅缓存了元数据。
          </p>
        </div>
      </section>

      <aside class="ui-stack">
        <section class="ui-card">
          <div class="ui-card-head">
            <div>
              <h2 class="ui-card-title">安装</h2>
              <p class="ui-card-sub">通过本镜像拉取该模块</p>
            </div>
            <button
              v-if="detail.showRefresh"
              class="ui-btn ui-btn-ghost ui-btn-sm"
              type="button"
              :disabled="refreshing"
              @click="refreshMeta"
            >
              <Icon name="refresh" />
              {{ refreshing ? '刷新中…' : '刷新元数据' }}
            </button>
          </div>
          <div class="ui-card-body">
            <div class="ui-tabs" role="tablist" aria-label="选择安装方式">
              <button
                v-for="m in cmds"
                :key="m"
                class="ui-tab"
                :class="{ 'is-active': cmdMode === m }"
                type="button"
                role="tab"
                :aria-selected="cmdMode === m"
                @click="cmdMode = m"
              >
                {{ m === 'get' ? 'go get' : 'go mod download' }}
              </button>
            </div>
            <CmdBox class="ui-mt-8" :cmd="installCmd" />
            <p class="ui-hint ui-mt-8">
              GOPROXY 指向 <span class="ui-mono">{{ host || '本镜像' }}</span> · 指定当前版本 {{ ver || '-' }}
            </p>
          </div>
        </section>

        <section class="ui-card">
          <div class="ui-card-head">
            <div>
              <h2 class="ui-card-title">模块信息</h2>
              <p class="ui-card-sub">当前版本的缓存与元数据</p>
            </div>
          </div>
          <div class="ui-card-body">
            <dl class="ui-meta">
              <dt>当前版本</dt>
              <dd class="ui-mono">{{ cur?.version || '-' }}</dd>
              <dt>最新版本</dt>
              <dd class="ui-mono">{{ detail.module.latestVersion || '-' }}</dd>
              <dt>发布时间</dt>
              <dd>{{ cur?.publishedAt ? fmtTimeLong(cur.publishedAt) : '-' }}</dd>
              <dt>go 版本</dt>
              <dd class="ui-mono">{{ cur?.goVersion || '-' }}</dd>
              <dt>当前体积</dt>
              <dd>{{ (cur?.zipSizeBytes || 0) > 0 ? fmtBytes(cur.zipSizeBytes) : 'zip 未下载' }}</dd>
              <dt>已缓存版本</dt>
              <dd>
                <button class="ui-link" type="button" title="打开版本抽屉" @click="drawer = 'versions'">
                  {{ detail.cachedCount }} 个 · {{ fmtBytes(detail.cachedSize) }}
                </button>
              </dd>
              <template v-if="detail.module.metaCachedAt">
                <dt>元数据更新</dt>
                <dd>{{ fmtTimeLong(detail.module.metaCachedAt) }}</dd>
              </template>
            </dl>

            <hr class="ui-divider">

            <div class="ui-row" style="flex-wrap: wrap; gap: 14px">
              <a v-if="detail.pkgGoDevURL" :href="detail.pkgGoDevURL" class="ui-link" target="_blank" rel="noopener">
                <Icon name="web" /> pkg.go.dev
              </a>
              <a v-if="detail.zipURL" :href="detail.zipURL" class="ui-link" download>
                <Icon name="download" /> 下载 .zip
              </a>
              <a v-if="detail.modURL" :href="detail.modURL" class="ui-link" download>
                <Icon name="download" /> .mod
              </a>
              <a v-if="detail.infoURL" :href="detail.infoURL" class="ui-link" download>
                <Icon name="download" /> .info
              </a>
            </div>
          </div>
        </section>
      </aside>
    </div>

    <div v-if="drawer" class="ui-drawer-layer" @click.self="drawer = ''">
      <section class="ui-drawer" role="dialog" aria-modal="true" :aria-label="drawerTitle">
        <header class="ui-drawer-head">
          <div>
            <h2 class="ui-drawer-title">{{ drawerTitle }}</h2>
            <p class="ui-drawer-sub">{{ drawerSub }}</p>
          </div>
          <button class="ui-icon-btn" type="button" title="关闭（Esc）" aria-label="关闭" @click="drawer = ''">
            <Icon name="close" />
          </button>
        </header>

        <div class="ui-drawer-body">
          <template v-if="drawer === 'versions'">
            <button
              v-for="v in sortedVersions"
              :key="v.version"
              class="ui-version-row ui-version-btn"
              :class="{ 'is-current': v.version === cur?.version }"
              type="button"
              :title="v.version === cur?.version ? '当前版本' : `切换到 ${v.version}`"
              @click="changeVersion(v.version)"
            >
              <span class="ui-version-main">
                <span class="ui-version-line">
                  <span class="ui-version-tag">{{ v.version }}</span>
                  <span v-if="v.version === detail.module.latestVersion" class="ui-chip ui-chip-line">latest</span>
                  <span v-if="v.version === cur?.version" class="ui-chip ui-chip-brand">当前</span>
                  <span v-if="v.pseudo" class="ui-chip ui-chip-line">pseudo</span>
                </span>
                <span class="ui-version-meta num">
                  {{ v.publishedAt ? fmtTimeLong(v.publishedAt) : '-' }} · {{ v.zipSizeBytes > 0 ? fmtBytes(v.zipSizeBytes) : 'zip 未下载' }}
                </span>
              </span>
              <span class="ui-version-side">
                <span class="ui-chip" :class="v.cached ? 'ui-chip-ok' : 'ui-chip-line'">
                  {{ v.cached ? '已缓存' : '仅元数据' }}
                </span>
                <Icon v-if="v.version !== cur?.version" class="ui-version-arrow" name="chevron-right" />
              </span>
            </button>
            <EmptyState v-if="!sortedVersions.length" ico="hexagon-multiple" title="暂无版本记录" sub="等待元数据刷新后重试。" />
          </template>

          <template v-else>
            <div v-if="reqs.length" class="ui-dep-list">
              <div v-for="dep in reqs" :key="dep.path" class="ui-dep-row">
                <RouterLink :to="{ name: 'module', params: { path: dep.path } }" class="ui-mono ui-cell-title">
                  {{ dep.path }}
                </RouterLink>
                <span class="ui-row" style="gap: 6px; flex-wrap: nowrap">
                  <span v-if="dep.indirect" class="ui-chip ui-chip-line">indirect</span>
                  <span class="ui-chip ui-chip-line ui-mono">{{ dep.version }}</span>
                </span>
              </div>
            </div>
            <EmptyState
              v-else
              ico="check-circle"
              title="没有依赖记录"
              sub="这个版本可以独立安装，或其 go.mod 尚未解析出 require 项。"
            />
          </template>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
/* go.mod 源码块：与 README 渲染区分开，保持等宽与代码观感 */
.gomod-src {
  margin: 0;
  padding: 20px;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  line-height: 1.6;
  background: color-mix(in srgb, currentColor 6%, transparent);
  overflow-x: auto;
}
</style>
