<script setup>
// 公开包详情：README 为主；版本与依赖收进抽屉，版本切换保留 ?v= 深链。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api, qs } from '../lib/api';
import { useDetailResource } from '../lib/composables';
import { fmtBytes, fmtTimeLong, plainText } from '../lib/format';
import { notify, notifyError } from '../lib/toast';
import { app } from '../lib/store';
import Icon from '../components/Icon.vue';
import CmdBox from '../components/CmdBox.vue';
import EmptyState from '../components/EmptyState.vue';

const route = useRoute();
const router = useRouter();
// 详情资源内核：竞态守卫 + 旧请求取消 + loading/notFound/error 三态（含重试）
const { loading, notFound, error, detail, load, retry } = useDetailResource({
  fetch: (signal) => api.get(`/api/package/${pkgName.value}${qs({ v: route.query.v })}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const drawer = ref(''); // '' | 'versions' | 'deps'
const refreshing = ref(false);

const pkgName = computed(() => String(route.params.pkg || ''));
const cur = computed(() => detail.value?.current || null);

const managers = ['npm', 'pnpm', 'yarn', 'bun'];
const pkgMgr = ref('npm');
const installVersion = computed(() => cur.value?.version || detail.value?.pkg?.latestVersion || '');
const installCmd = computed(() => {
  const n = pkgName.value;
  const v = installVersion.value ? `@${installVersion.value}` : '';
  switch (pkgMgr.value) {
    case 'pnpm': return `pnpm add ${n}${v}`;
    case 'yarn': return `yarn add ${n}${v}`;
    case 'bun': return `bun add ${n}${v}`;
    default: return `npm i ${n}${v}`;
  }
});

const versionCount = computed(() => detail.value?.versionTotal ?? detail.value?.versions?.length ?? 0);

const depGroups = computed(() => {
  const c = cur.value;
  if (!c) return [];
  return [
    { title: '运行时依赖', items: c.deps || [] },
    { title: '开发依赖', items: c.devDeps || [] },
    { title: '对等依赖', items: c.peerDeps || [] },
  ].filter((group) => group.items.length);
});

const depCount = computed(() => depGroups.value.reduce((sum, group) => sum + group.items.length, 0));

// 上游返回的版本顺序并不稳定；按发布时间倒序，并把当前版本置顶。
const sortedVersions = computed(() => {
  const list = [...(detail.value?.versions || [])];
  list.sort((a, b) => new Date(b.publishedAt || 0) - new Date(a.publishedAt || 0));
  const current = cur.value?.version;
  const index = current ? list.findIndex((v) => v.version === current) : -1;
  if (index > 0) list.unshift(list.splice(index, 1)[0]);
  return list;
});

const drawerTitle = computed(() => (drawer.value === 'versions' ? '切换版本' : '依赖'));
// Registry 基地址由服务端注入（前端不硬编码端口），缺失时按主机名回退；展示去掉末尾 /
const npmRegistryBase = computed(() => (app.npmRegistry || '').replace(/\/+$/, ''));
const drawerSub = computed(() => {
  if (drawer.value === 'versions') {
    const truncated = detail.value?.versionsTruncated ? ' · 列表仅展示已缓存与最近版本' : '';
    return `共 ${versionCount.value} 个版本 · 当前 v${cur.value?.version || '-'}${truncated}`;
  }
  return depCount.value ? `共 ${depCount.value} 个依赖` : '当前版本没有依赖记录';
});

async function refreshMeta() {
  refreshing.value = true;
  try {
    await api.post(`/api/admin/package-refresh${qs({ pkg: pkgName.value })}`);
    notify(`已强制刷新 ${pkgName.value} 的元数据`);
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

watch(() => [pkgName.value, route.query.v], load);
onMounted(() => {
  load();
  window.addEventListener('keydown', onKeydown);
});
onUnmounted(() => window.removeEventListener('keydown', onKeydown));
</script>

<template>
  <!-- 包不存在：安装引导 -->
  <div v-if="notFound" class="ui-container ui-page">
    <div class="ui-card" style="max-width: 760px; margin-inline: auto">
      <div class="ui-card-body" style="padding: 40px; text-align: center">
        <p class="ui-mono ui-muted" style="margin: 0; font-size: 64px; font-weight: 700; letter-spacing: -0.04em; line-height: 1">404</p>
        <h1 class="ui-page-title" style="margin-top: 14px">包不存在</h1>
        <p class="ui-page-sub">
          「<span class="ui-mono">{{ pkgName }}</span>」尚未被缓存，或者名字写错了。
        </p>
        <p class="ui-hint" style="margin-top: 18px">本镜像按需缓存 —— 直接在客户端安装它试试：</p>
        <div class="ui-tabs" style="justify-content: center" role="tablist" aria-label="包管理器">
          <button
            v-for="m in managers"
            :key="m"
            type="button"
            class="ui-tab"
            :class="{ 'is-active': pkgMgr === m }"
            role="tab"
            :aria-selected="pkgMgr === m"
            @click="pkgMgr = m"
          >
            {{ m }}
          </button>
        </div>
        <CmdBox class="ui-mt-8" :cmd="installCmd" />
        <p class="ui-hint ui-mt-8">安装完成后刷新此页，即可查看 README、版本与依赖。</p>
        <div class="ui-row" style="justify-content: center; flex-wrap: wrap; margin-top: 24px">
          <RouterLink :to="{ name: 'home' }" class="ui-btn ui-btn-primary">返回首页</RouterLink>
          <RouterLink :to="{ name: 'npmregistry' }" class="ui-btn ui-btn-ghost">浏览已缓存包</RouterLink>
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
        <p class="ui-empty-title">正在加载 {{ pkgName }}…</p>
      </div>
    </div>
  </div>

  <div v-else-if="detail" class="ui-container ui-page">
    <RouterLink :to="{ name: 'npmregistry' }" class="ui-back">
      <Icon name="chevron-left" />
      返回 NPM
    </RouterLink>

    <header class="ui-page-head ui-detail-head">
      <div>
        <div class="ui-row" style="flex-wrap: wrap">
          <h1 class="ui-page-title ui-mono">{{ pkgName }}</h1>
          <span v-if="cur?.version" class="ui-chip ui-chip-brand ui-mono">v{{ cur.version }}</span>
          <span class="ui-chip" :class="(cur?.fileSizeBytes || 0) > 0 ? 'ui-chip-ok' : 'ui-chip-line'">
            {{ (cur?.fileSizeBytes || 0) > 0 ? '已缓存' : '仅元数据' }}
          </span>
          <span class="ui-chip">已缓存 {{ detail.cachedCount }} 个版本 · {{ fmtBytes(detail.cachedSize) }}</span>
        </div>
        <p v-if="detail.pkg.description" class="ui-page-sub" style="max-width: 760px">{{ plainText(detail.pkg.description) }}</p>
        <div class="ui-row ui-mt-8" style="flex-wrap: wrap">
          <span v-if="detail.pkg.latestVersion" class="ui-chip ui-chip-line ui-mono">latest v{{ detail.pkg.latestVersion }}</span>
          <span v-if="detail.pkg.license" class="ui-chip ui-chip-line">{{ detail.pkg.license }}</span>
          <span class="ui-chip ui-chip-line">{{ versionCount }} 个版本</span>
        </div>
      </div>

      <div class="ui-page-actions">
        <button class="ui-btn ui-btn-outline" type="button" title="查看全部版本并切换" @click="drawer = 'versions'">
          <Icon name="format-list-bulleted" />
          版本 {{ versionCount }}
        </button>
        <button class="ui-btn ui-btn-outline" type="button" title="查看运行时依赖" @click="drawer = 'deps'">
          <Icon name="package-variant-closed" />
          依赖 {{ depCount }}
        </button>
      </div>
    </header>

    <div class="ui-detail-grid">
      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">README</h2>
            <p class="ui-card-sub">当前版本 v{{ cur?.version || '-' }} 的说明与示例</p>
          </div>
          <span class="ui-muted ui-mono" style="font-size: 11.5px">{{ pkgName }}</span>
        </div>
        <div v-if="detail.readmeHTML" class="ui-card-body md-body ui-readme" v-html="detail.readmeHTML"></div>
        <div v-else class="ui-card-body">
          <p class="ui-muted" style="margin: 0">该版本暂无 README。</p>
        </div>
      </section>

      <aside class="ui-stack">
        <section class="ui-card">
          <div class="ui-card-head">
            <div>
              <h2 class="ui-card-title">安装</h2>
              <p class="ui-card-sub">选择你使用的包管理器</p>
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
            <div class="ui-tabs" role="tablist" aria-label="选择包管理器">
              <button
                v-for="m in managers"
                :key="m"
                class="ui-tab"
                :class="{ 'is-active': pkgMgr === m }"
                type="button"
                role="tab"
                :aria-selected="pkgMgr === m"
                @click="pkgMgr = m"
              >
                {{ m }}
              </button>
            </div>
            <CmdBox class="ui-mt-8" :cmd="installCmd" />
            <p class="ui-hint ui-mt-8">通过本镜像 <span class="ui-mono">{{ npmRegistryBase || '本站' }}</span> 安装 · 指定当前版本 v{{ installVersion }}</p>
          </div>
        </section>

        <section class="ui-card">
          <div class="ui-card-head">
            <div>
              <h2 class="ui-card-title">包信息</h2>
              <p class="ui-card-sub">当前版本的缓存与元数据</p>
            </div>
          </div>
          <div class="ui-card-body">
            <dl class="ui-meta">
              <dt>当前版本</dt>
              <dd class="ui-mono">{{ cur?.version || '-' }}</dd>
              <dt>最新版本</dt>
              <dd class="ui-mono">{{ detail.pkg.latestVersion || '-' }}</dd>
              <dt>版本总数</dt>
              <dd class="ui-mono">{{ versionCount }}</dd>
              <dt>当前体积</dt>
              <dd>{{ (cur?.fileSizeBytes || 0) > 0 ? fmtBytes(cur.fileSizeBytes) : '未下载' }}</dd>
              <dt>已缓存版本</dt>
              <dd>
                <button class="ui-link" type="button" title="打开版本抽屉" @click="drawer = 'versions'">
                  {{ detail.cachedCount }} 个 · {{ fmtBytes(detail.cachedSize) }}
                </button>
              </dd>
              <template v-if="detail.pkg.license">
                <dt>许可证</dt>
                <dd>{{ detail.pkg.license }}</dd>
              </template>
              <template v-if="detail.pkg.metaCachedAt">
                <dt>元数据更新</dt>
                <dd>{{ fmtTimeLong(detail.pkg.metaCachedAt) }}</dd>
              </template>
            </dl>

            <hr class="ui-divider">

            <div class="ui-row" style="flex-wrap: wrap; gap: 14px">
              <a v-if="detail.pkg.homepageURL" :href="detail.pkg.homepageURL" class="ui-link" target="_blank" rel="noopener">
                <Icon name="web" /> 项目主页
              </a>
              <a v-if="detail.pkg.repositoryURL" :href="detail.pkg.repositoryURL" class="ui-link" target="_blank" rel="noopener">
                <Icon name="source-repository" /> 源码仓库
              </a>
              <a v-if="detail.downloadURL" :href="detail.downloadURL" class="ui-link" download>
                <Icon name="download" /> 下载 tarball
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
                  <span class="ui-version-tag">v{{ v.version }}</span>
                  <span v-if="v.version === detail.pkg.latestVersion" class="ui-chip ui-chip-line">latest</span>
                  <span v-if="v.version === cur?.version" class="ui-chip ui-chip-brand">当前</span>
                </span>
                <span class="ui-version-meta num">
                  {{ v.publishedAt ? fmtTimeLong(v.publishedAt) : '-' }} · {{ v.fileSizeBytes > 0 ? fmtBytes(v.fileSizeBytes) : '未下载' }}
                </span>
              </span>
              <span class="ui-version-side">
                <span class="ui-chip" :class="v.fileSizeBytes > 0 ? 'ui-chip-ok' : 'ui-chip-line'">
                  {{ v.fileSizeBytes > 0 ? '已缓存' : '未缓存' }}
                </span>
                <Icon v-if="v.version !== cur?.version" class="ui-version-arrow" name="chevron-right" />
              </span>
            </button>
            <EmptyState v-if="!sortedVersions.length" ico="package-variant-closed" title="暂无版本记录" sub="等待元数据刷新后重试。" />
          </template>

          <template v-else>
            <div v-if="depGroups.length">
              <div v-for="group in depGroups" :key="group.title">
                <h3 class="ui-drawer-sub" style="margin: 18px 0 4px; color: var(--c-ink); font-weight: 700">
                  {{ group.title }} <span class="ui-muted">({{ group.items.length }})</span>
                </h3>
                <div class="ui-dep-list">
                  <div v-for="dep in group.items" :key="dep.name" class="ui-dep-row">
                    <RouterLink :to="{ name: 'package', params: { pkg: dep.name } }" class="ui-mono ui-cell-title">
                      {{ dep.name }}
                    </RouterLink>
                    <span class="ui-chip ui-chip-line ui-mono">{{ dep.range }}</span>
                  </div>
                </div>
              </div>
            </div>
            <EmptyState
              v-else
              ico="check-circle"
              title="没有依赖记录"
              sub="这个版本可以独立安装，或元数据中尚未提供依赖信息。"
            />
          </template>
        </div>
      </section>
    </div>
  </div>
</template>
