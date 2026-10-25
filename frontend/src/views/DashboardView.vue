<script setup>
// 仪表盘：四生态卡片（GO / NPM / PYTHON / JAVA），各生态内为原单服务视图结构。
// 统计永远分生态展示，不做跨生态合计（D6）。
import { computed, onMounted, ref } from 'vue';
import { api } from '../lib/api';
import { fmtBytes, fmtTimeLong } from '../lib/format';
import Icon from '../components/Icon.vue';
import CountUp from '../components/CountUp.vue';
import EmptyState from '../components/EmptyState.vue';
import TrendChart from '../components/TrendChart.vue';
import { notifyError } from '../lib/toast';

const data = ref(null);

// 生态段默认结构：接口失败时也保持字段齐全，模板取属性不会抛错白屏
function emptyEco() {
  return {
    moduleCount: 0,
    packageCount: 0,
    projectCount: 0,
    artifactCount: 0,
    versionCount: 0,
    fileCount: 0,
    totalSize: 0,
    hitRate: 0,
    recent: [],
    trend: [],
    disk: { free: 0, total: 0, warn: false, inflight: 0 },
  };
}

function pickEco(seg) {
  const s = seg || {};
  return {
    ...emptyEco(),
    ...s,
    moduleCount: s.moduleCount ?? 0,
    packageCount: s.packageCount ?? 0,
    projectCount: s.projectCount ?? 0,
    artifactCount: s.artifactCount ?? 0,
    disk: { ...emptyEco().disk, ...(s.disk || {}) },
  };
}

const go = computed(() => pickEco(data.value?.go));
const npm = computed(() => pickEco(data.value?.npm));
const py = computed(() => pickEco(data.value?.py));
const mvn = computed(() => pickEco(data.value?.mvn));

onMounted(async () => {
  try {
    data.value = await api.get('/api/admin/dashboard');
  } catch (e) {
    data.value = { go: null, npm: null, py: null, mvn: null };
    if (e?.status !== 401) notifyError('仪表盘加载失败');
  }
});
</script>

<template>
  <div>
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">仪表盘</h1>
        <p class="ui-page-sub">四生态缓存运行概览与最近活动（各生态独立统计）</p>
      </div>
    </header>

    <!-- GO 卡片 -->
    <section class="ui-card">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">GO</h2>
          <p class="ui-card-sub">GOPROXY 生态 · 模块、版本与磁盘</p>
        </div>
        <RouterLink :to="{ name: 'admin-modules' }" class="ui-card-link">全部模块 →</RouterLink>
      </div>
      <div class="ui-card-body">
        <div class="ui-stat-grid ui-stat-grid-3">
          <div class="ui-stat">
            <div class="ui-stat-label">模块总数</div>
            <div class="ui-stat-value"><CountUp :value="go.moduleCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">版本总数</div>
            <div class="ui-stat-value"><CountUp :value="go.versionCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存命中率</div>
            <div class="ui-stat-value">{{ (go.hitRate ?? 0).toFixed(1) }}<small>%</small></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存总大小</div>
            <div class="ui-stat-value">{{ fmtBytes(go.totalSize) }}</div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">磁盘剩余{{ go.disk.err ? '（不可用）' : '' }}</div>
            <div v-if="go.disk.err" class="ui-stat-value ui-muted">—</div>
            <div v-else class="ui-stat-value">
              {{ fmtBytes(go.disk.free) }}<small>/ {{ fmtBytes(go.disk.total) }}</small>
            </div>
            <p v-if="!go.disk.err && go.disk.warn" class="ui-hint" style="margin: 6px 0 0; color: var(--c-ink)">
              <Icon name="alert" /> 剩余空间不足 5GB，请及时清理或扩容
            </p>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">正在回源</div>
            <div class="ui-stat-value"><CountUp :value="go.disk.inflight" /><small>个</small></div>
          </div>
        </div>
        <div class="ui-card-head" style="margin-top: 16px">
          <div>
            <h3 class="ui-card-title" style="font-size: 14px">每日请求趋势</h3>
            <p class="ui-card-sub">最近 14 天 zip 请求量</p>
          </div>
        </div>
        <TrendChart :rows="go.trend || []" field="zip" unit="zip" />
      </div>
    </section>

    <!-- NPM 卡片 -->
    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">NPM</h2>
          <p class="ui-card-sub">npm Registry 生态 · 包、版本与磁盘</p>
        </div>
        <RouterLink :to="{ name: 'admin-packages' }" class="ui-card-link">全部包 →</RouterLink>
      </div>
      <div class="ui-card-body">
        <div class="ui-stat-grid ui-stat-grid-3">
          <div class="ui-stat">
            <div class="ui-stat-label">包总数</div>
            <div class="ui-stat-value"><CountUp :value="npm.packageCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">版本总数</div>
            <div class="ui-stat-value"><CountUp :value="npm.versionCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存命中率</div>
            <div class="ui-stat-value">{{ (npm.hitRate ?? 0).toFixed(1) }}<small>%</small></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存总大小</div>
            <div class="ui-stat-value">{{ fmtBytes(npm.totalSize) }}</div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">正在回源</div>
            <div class="ui-stat-value"><CountUp :value="npm.disk.inflight" /><small>个</small></div>
          </div>
        </div>
        <div class="ui-card-head" style="margin-top: 16px">
          <div>
            <h3 class="ui-card-title" style="font-size: 14px">每日请求趋势</h3>
            <p class="ui-card-sub">最近 14 天 tarball 请求量</p>
          </div>
        </div>
        <TrendChart :rows="npm.trend || []" field="tarball" unit="tarball" />
      </div>
    </section>

    <!-- PYTHON 卡片 -->
    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">PYTHON</h2>
          <p class="ui-card-sub">PyPI Simple 生态 · 项目、文件与磁盘</p>
        </div>
        <RouterLink :to="{ name: 'admin-pyprojects' }" class="ui-card-link">全部项目 →</RouterLink>
      </div>
      <div class="ui-card-body">
        <div class="ui-stat-grid ui-stat-grid-3">
          <div class="ui-stat">
            <div class="ui-stat-label">项目总数</div>
            <div class="ui-stat-value"><CountUp :value="py.projectCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">已缓存文件</div>
            <div class="ui-stat-value"><CountUp :value="py.fileCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存命中率</div>
            <div class="ui-stat-value">{{ (py.hitRate ?? 0).toFixed(1) }}<small>%</small></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存总大小</div>
            <div class="ui-stat-value">{{ fmtBytes(py.totalSize) }}</div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">正在回源</div>
            <div class="ui-stat-value"><CountUp :value="py.disk.inflight" /><small>个</small></div>
          </div>
        </div>
        <div class="ui-card-head" style="margin-top: 16px">
          <div>
            <h3 class="ui-card-title" style="font-size: 14px">每日请求趋势</h3>
            <p class="ui-card-sub">最近 14 天发行文件请求量</p>
          </div>
        </div>
        <TrendChart :rows="py.trend || []" field="file" unit="file" />
      </div>
    </section>

    <!-- JAVA 卡片 -->
    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">JAVA</h2>
          <p class="ui-card-sub">Maven 仓库生态 · 构件、文件与磁盘</p>
        </div>
        <RouterLink :to="{ name: 'admin-mvnartifacts' }" class="ui-card-link">全部构件 →</RouterLink>
      </div>
      <div class="ui-card-body">
        <div class="ui-stat-grid ui-stat-grid-3">
          <div class="ui-stat">
            <div class="ui-stat-label">构件总数</div>
            <div class="ui-stat-value"><CountUp :value="mvn.artifactCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">已缓存文件</div>
            <div class="ui-stat-value"><CountUp :value="mvn.fileCount" /></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存命中率</div>
            <div class="ui-stat-value">{{ (mvn.hitRate ?? 0).toFixed(1) }}<small>%</small></div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">缓存总大小</div>
            <div class="ui-stat-value">{{ fmtBytes(mvn.totalSize) }}</div>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">磁盘剩余{{ mvn.disk.err ? '（不可用）' : '' }}</div>
            <div v-if="mvn.disk.err" class="ui-stat-value ui-muted">—</div>
            <div v-else class="ui-stat-value">
              {{ fmtBytes(mvn.disk.free) }}<small>/ {{ fmtBytes(mvn.disk.total) }}</small>
            </div>
            <p v-if="!mvn.disk.err && mvn.disk.warn" class="ui-hint" style="margin: 6px 0 0; color: var(--c-ink)">
              <Icon name="alert" /> 剩余空间不足 5GB，请及时清理或扩容
            </p>
          </div>
          <div class="ui-stat">
            <div class="ui-stat-label">正在回源</div>
            <div class="ui-stat-value"><CountUp :value="mvn.disk.inflight" /><small>个</small></div>
          </div>
        </div>
        <div class="ui-card-head" style="margin-top: 16px">
          <div>
            <h3 class="ui-card-title" style="font-size: 14px">每日请求趋势</h3>
            <p class="ui-card-sub">最近 14 天构件文件请求量</p>
          </div>
        </div>
        <TrendChart :rows="mvn.trend || []" field="file" unit="file" />
      </div>
    </section>

    <!-- 最近缓存活动（四生态并置） -->
    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">最近缓存</h2>
          <p class="ui-card-sub">各生态最近同步到本地的产物</p>
        </div>
      </div>
      <EmptyState
        v-if="!go.recent?.length && !npm.recent?.length && !py.recent?.length && !mvn.recent?.length"
        flat
        ico="cloud-download-outline"
        title="暂无缓存活动"
        sub="客户端通过本镜像拉取产物后，这里会显示最近记录"
      />
      <template v-else>
        <div v-if="go.recent?.length" class="ui-table-wrap">
          <table class="ui-table ui-table-wide">
            <thead>
              <tr><th>生态</th><th>模块</th><th>版本</th><th>体积</th><th>缓存时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in go.recent" :key="'go-' + r.modulePath + r.version">
                <td><span class="ui-chip">Go</span></td>
                <td>
                  <RouterLink :to="{ name: 'module', params: { path: r.modulePath } }" class="ui-cell-title ui-mono">
                    {{ r.modulePath }}
                  </RouterLink>
                </td>
                <td class="num ui-mono ui-muted">{{ r.version }}</td>
                <td class="num">{{ fmtBytes(r.zipSize) }}</td>
                <td class="num ui-muted">{{ fmtTimeLong(r.cachedAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="npm.recent?.length" class="ui-table-wrap">
          <table class="ui-table ui-table-wide">
            <thead>
              <tr><th>生态</th><th>包</th><th>版本</th><th>体积</th><th>缓存时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in npm.recent" :key="'npm-' + r.pkgName + r.version">
                <td><span class="ui-chip ui-chip-warn">npm</span></td>
                <td>
                  <RouterLink :to="{ name: 'package', params: { pkg: r.pkgName } }" class="ui-cell-title ui-mono">
                    {{ r.pkgName }}
                  </RouterLink>
                </td>
                <td class="num ui-mono ui-muted">{{ r.version }}</td>
                <td class="num">{{ fmtBytes(r.fileSizeBytes) }}</td>
                <td class="num ui-muted">{{ fmtTimeLong(r.cachedAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="py.recent?.length" class="ui-table-wrap">
          <table class="ui-table ui-table-wide">
            <thead>
              <tr><th>生态</th><th>项目</th><th>文件</th><th>体积</th><th>缓存时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in py.recent" :key="'py-' + r.projectName + r.filename">
                <td><span class="ui-chip">py</span></td>
                <td>
                  <RouterLink :to="{ name: 'pyproject', params: { name: r.projectName } }" class="ui-cell-title ui-mono">
                    {{ r.projectName }}
                  </RouterLink>
                </td>
                <td class="ui-mono ui-muted">{{ r.filename }}</td>
                <td class="num">{{ fmtBytes(r.fileSizeBytes) }}</td>
                <td class="num ui-muted">{{ fmtTimeLong(r.cachedAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="mvn.recent?.length" class="ui-table-wrap">
          <table class="ui-table ui-table-wide">
            <thead>
              <tr><th>生态</th><th>构件</th><th>文件</th><th>体积</th><th>缓存时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in mvn.recent" :key="'mvn-' + r.name + r.filename">
                <td><span class="ui-chip ui-chip-warn">java</span></td>
                <td>
                  <RouterLink :to="{ name: 'mvnartifact', params: { coords: r.name } }" class="ui-cell-title ui-mono">
                    {{ r.name }}
                  </RouterLink>
                </td>
                <td class="ui-mono ui-muted">{{ r.filename }}</td>
                <td class="num">{{ fmtBytes(r.fileSizeBytes) }}</td>
                <td class="num ui-muted">{{ fmtTimeLong(r.cachedAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>
  </div>
</template>
