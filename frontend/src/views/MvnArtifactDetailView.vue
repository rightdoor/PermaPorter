<script setup>
// 公开 Maven 构件详情：文件清单与缓存状态为主（仓库协议无包描述元数据）。
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { api, qs } from '../lib/api';
import { useDetailResource } from '../lib/composables';
import { fmtBytes, fmtNum, fmtTimeShort } from '../lib/format';
import { app } from '../lib/store';
import { notify, notifyError } from '../lib/toast';
import Icon from '../components/Icon.vue';
import CmdBox from '../components/CmdBox.vue';
import EmptyState from '../components/EmptyState.vue';

const route = useRoute();
// 详情资源内核：竞态守卫 + 旧请求取消 + loading/notFound/error 三态（含重试）
const { loading, notFound, error, detail, load, retry } = useDetailResource({
  fetch: (signal) => api.get(`/api/mvnartifact/${coords.value}`, { signal }),
  onError: (e) => notifyError(e.message),
});
const refreshing = ref(false);

// 坐标形如 groupId:artifactId（路径段内含冒号，路由已按通配捕获）
const coords = computed(() => String(route.params.coords || ''));
const artifact = computed(() => detail.value?.artifact || null);
const files = computed(() => detail.value?.files || []);
const mavenRepo = computed(() => app.mavenRepo || '');

const cachedCount = computed(() => detail.value?.cachedCount ?? 0);
const cachedSize = computed(() => detail.value?.cachedSize ?? 0);

async function refreshMeta() {
  refreshing.value = true;
  try {
    await api.post(`/api/admin/mvn-refresh${qs({ coords: coords.value })}`);
    notify(`已强制刷新 ${coords.value} 的元数据`);
    await load();
  } catch (e) {
    notifyError(`刷新失败: ${e.message}`);
  } finally {
    refreshing.value = false;
  }
}

watch(() => coords.value, load);
onMounted(load);
</script>

<template>
  <!-- 构件不存在：接入引导 -->
  <div v-if="notFound" class="ui-container ui-page">
    <div class="ui-card" style="max-width: 760px; margin-inline: auto">
      <div class="ui-card-body" style="padding: 40px; text-align: center">
        <p class="ui-mono ui-muted" style="margin: 0; font-size: 64px; font-weight: 700; letter-spacing: -0.04em; line-height: 1">404</p>
        <h1 class="ui-page-title" style="margin-top: 14px">构件不存在</h1>
        <p class="ui-page-sub">
          「<span class="ui-mono">{{ coords }}</span>」尚未被缓存，或者坐标写错了。
        </p>
        <p class="ui-hint">在客户端把 Maven/Gradle 仓库指向本服务并依赖它，元数据与构件文件会自动出现在这里。</p>
        <CmdBox v-if="mavenRepo" class="ui-mt-16" :cmd="`mvn dependency:get -DremoteRepositories=mirror::default::${mavenRepo}`" />
        <div class="ui-row" style="justify-content: center; margin-top: 18px">
          <RouterLink :to="{ name: 'maven' }" class="ui-btn ui-btn-ghost">返回 JAVA</RouterLink>
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
        <p class="ui-empty-title">正在加载 {{ coords }}…</p>
      </div>
    </div>
  </div>

  <div v-else class="ui-container ui-page">
    <RouterLink :to="{ name: 'maven' }" class="ui-back">
      <Icon name="chevron-left" />
      返回 JAVA
    </RouterLink>

    <header class="ui-page-head ui-detail-head">
      <div>
        <div class="ui-row" style="flex-wrap: wrap">
          <h1 class="ui-page-title ui-mono">{{ coords }}</h1>
          <span v-if="artifact?.latestVersion" class="ui-chip ui-chip-line ui-mono">{{ artifact.latestVersion }}</span>
        </div>
        <p class="ui-page-sub">
          Maven 构件 · 元数据（maven-metadata.xml）按 TTL 刷新，构件文件不可变、落盘后永久缓存
        </p>
      </div>
      <div class="ui-page-actions">
        <button v-if="detail?.showRefresh" class="ui-btn ui-btn-outline" type="button" :disabled="refreshing" @click="refreshMeta">
          <Icon name="refresh" />
          {{ refreshing ? '刷新中…' : '刷新元数据' }}
        </button>
      </div>
    </header>

    <dl class="ui-facts" style="border-bottom: 0; padding-bottom: 0">
      <div>
        <dt>构件文件</dt>
        <dd>{{ artifact?.fileCount ?? files.length }}</dd>
      </div>
      <div>
        <dt>已缓存</dt>
        <dd>{{ cachedCount }}<small style="font-size: 13px; color: var(--c-ink-3)"> / {{ artifact?.fileCount ?? files.length }}</small></dd>
      </div>
      <div>
        <dt>缓存体积</dt>
        <dd>{{ fmtBytes(cachedSize) }}</dd>
      </div>
      <div>
        <dt>上游版本数</dt>
        <dd>{{ artifact?.versionCount > 0 ? fmtNum(artifact.versionCount) : '-' }}</dd>
      </div>
    </dl>

    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">构件文件</h2>
          <p class="ui-card-sub">jar / pom / sources 等文件清单 · 已缓存的文件可直接下载</p>
        </div>
      </div>
      <div v-if="files.length" class="ui-table-wrap">
        <table class="ui-table ui-table-wide">
          <thead>
            <tr>
              <th>文件</th>
              <th>版本</th>
              <th>体积</th>
              <th>最近缓存</th>
              <th style="text-align: right">下载</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in files" :key="f.repoPath">
              <td>
                <div class="ui-cell-main">
                  <span class="ui-chip" :class="f.cached ? 'ui-chip-ok' : 'ui-chip-line'">{{ f.cached ? '已缓存' : '未缓存' }}</span>
                  <span class="ui-cell-title ui-mono" :title="f.sha1 ? `sha1: ${f.sha1}` : ''">{{ f.filename }}</span>
                </div>
              </td>
              <td class="num ui-mono">{{ f.version || '-' }}</td>
              <td class="num">{{ f.fileSizeBytes > 0 ? fmtBytes(f.fileSizeBytes) : '—' }}</td>
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
        ico="database"
        title="该构件还没有文件记录"
        sub="固定版本构建只请求构件文件；依赖它之后记录会出现在这里。"
      />
    </section>
  </div>
</template>
