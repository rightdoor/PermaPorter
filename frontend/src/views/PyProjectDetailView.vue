<script setup>
// 公开 Py 项目详情：发行文件清单与缓存状态为主（Simple 协议无 README/依赖元数据）。
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { api, qs } from '../lib/api';
import { useDetailResource } from '../lib/composables';
import { fmtBytes, fmtTimeShort } from '../lib/format';
import { app } from '../lib/store';
import { notify, notifyError } from '../lib/toast';
import Icon from '../components/Icon.vue';
import CmdBox from '../components/CmdBox.vue';
import EmptyState from '../components/EmptyState.vue';

const route = useRoute();
// 详情资源内核：竞态守卫 + 旧请求取消 + loading/notFound/error 三态（含重试）
const { loading, notFound, error, detail, load, retry } = useDetailResource({
  fetch: (signal) => api.get(`/api/pyproject/${projectName.value}${qs({ v: route.query.v })}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const refreshing = ref(false);

const projectName = computed(() => String(route.params.name || ''));
const project = computed(() => detail.value?.project || null);
const files = computed(() => detail.value?.files || []);
const pyIndex = computed(() => app.pyIndex || '');

const cachedCount = computed(() => detail.value?.cachedCount ?? 0);
const cachedSize = computed(() => detail.value?.cachedSize ?? 0);

async function refreshMeta() {
  refreshing.value = true;
  try {
    await api.post(`/api/admin/py-refresh${qs({ project: projectName.value })}`);
    notify(`已强制刷新 ${projectName.value} 的锚点页`);
    await load();
  } catch (e) {
    notifyError(`刷新失败: ${e.message}`);
  } finally {
    refreshing.value = false;
  }
}

watch(() => projectName.value, load);
onMounted(load);
</script>

<template>
  <!-- 项目不存在：接入引导 -->
  <div v-if="notFound" class="ui-container ui-page">
    <div class="ui-card" style="max-width: 760px; margin-inline: auto">
      <div class="ui-card-body" style="padding: 40px; text-align: center">
        <p class="ui-mono ui-muted" style="margin: 0; font-size: 64px; font-weight: 700; letter-spacing: -0.04em; line-height: 1">404</p>
        <h1 class="ui-page-title" style="margin-top: 14px">项目不存在</h1>
        <p class="ui-page-sub">
          「<span class="ui-mono">{{ projectName }}</span>」尚未被缓存，或者名字写错了。
        </p>
        <p class="ui-hint">在客户端把 pip 源指向本服务并安装它，锚点页与发行文件会自动出现在这里。</p>
        <CmdBox v-if="pyIndex" class="ui-mt-16" :cmd="`pip config set global.index-url ${pyIndex}`" />
        <div class="ui-row" style="justify-content: center; margin-top: 18px">
          <RouterLink :to="{ name: 'pythonsimple' }" class="ui-btn ui-btn-ghost">返回 PYTHON</RouterLink>
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
        <p class="ui-empty-title">正在加载 {{ projectName }}…</p>
      </div>
    </div>
  </div>

  <div v-else class="ui-container ui-page">
    <RouterLink :to="{ name: 'pythonsimple' }" class="ui-back">
      <Icon name="chevron-left" />
      返回 PYTHON
    </RouterLink>

    <header class="ui-page-head ui-detail-head">
      <div>
        <div class="ui-row" style="flex-wrap: wrap">
          <h1 class="ui-page-title ui-mono">{{ projectName }}</h1>
          <span v-if="project?.latestVersion" class="ui-chip ui-chip-line ui-mono">{{ project.latestVersion }}</span>
        </div>
        <p v-if="project?.summary" class="ui-page-sub">{{ project.summary }}</p>
        <p v-else class="ui-page-sub">PyPI Simple 项目 · 发行文件不可变，落盘后永久缓存</p>
      </div>
      <div class="ui-page-actions">
        <button v-if="detail?.showRefresh" class="ui-btn ui-btn-outline" type="button" :disabled="refreshing" @click="refreshMeta">
          <Icon name="refresh" />
          {{ refreshing ? '刷新中…' : '刷新锚点页' }}
        </button>
      </div>
    </header>

    <dl class="ui-facts" style="border-bottom: 0; padding-bottom: 0">
      <div>
        <dt>发行文件</dt>
        <dd>{{ project?.fileCount ?? files.length }}</dd>
      </div>
      <div>
        <dt>已缓存</dt>
        <dd>{{ cachedCount }}<small style="font-size: 13px; color: var(--c-ink-3)"> / {{ project?.fileCount ?? files.length }}</small></dd>
      </div>
      <div>
        <dt>缓存体积</dt>
        <dd>{{ fmtBytes(cachedSize) }}</dd>
      </div>
    </dl>

    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">发行文件</h2>
          <p class="ui-card-sub">wheel / sdist 清单（锚点页口径）· 已缓存的文件可直接下载</p>
        </div>
      </div>
      <div v-if="files.length" class="ui-table-wrap">
        <table class="ui-table ui-table-wide">
          <thead>
            <tr>
              <th>文件</th>
              <th>版本</th>
              <th>体积</th>
              <th>Python</th>
              <th>最近缓存</th>
              <th style="text-align: right">下载</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in files" :key="f.filename">
              <td>
                <div class="ui-cell-main">
                  <span class="ui-chip" :class="f.cached ? 'ui-chip-ok' : 'ui-chip-line'">{{ f.cached ? '已缓存' : '未缓存' }}</span>
                  <span class="ui-cell-title ui-mono" :title="f.sha256 ? `sha256: ${f.sha256}` : ''">{{ f.filename }}</span>
                </div>
              </td>
              <td class="num ui-mono">{{ f.version || '-' }}</td>
              <td class="num">{{ f.fileSizeBytes > 0 ? fmtBytes(f.fileSizeBytes) : '—' }}</td>
              <td class="num ui-mono ui-muted">{{ f.requiresPython || '-' }}</td>
              <td class="num ui-muted">{{ f.cachedAt ? fmtTimeShort(f.cachedAt) : '-' }}</td>
              <td>
                <div class="ui-table-actions">
                  <a v-if="f.downloadURL" class="ui-btn ui-btn-ghost ui-btn-sm" :href="f.downloadURL" title="从本镜像下载">
                    <Icon name="download" /> 下载
                  </a>
                  <span v-else class="ui-muted" style="font-size: 12px">首次请求时回源</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <EmptyState
        v-else
        flat
        ico="hexagon-multiple"
        title="该项目还没有发行文件记录"
        sub="锚点页可能刚刷新失败，稍后强制刷新重试。"
      />
    </section>
  </div>
</template>
