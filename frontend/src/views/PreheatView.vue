<script setup>
// 依赖预热：按生态分 tab（四套输入格式）——go: go.mod/go.sum/module@version；
// npm: package.json/包名清单；py: requirements.txt/项目名清单；
// mvn: pom.xml/Gradle 坐标/每行 groupId:artifactId[:version][:classifier]。进行中每 2s 轮询进度。
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue';
import { api } from '../lib/api';
import { fmtBytes, fmtStamp } from '../lib/format';
import { notify, notifyError } from '../lib/toast';
import EmptyState from '../components/EmptyState.vue';
import AutoTextarea from '../components/AutoTextarea.vue';

const GO_PLACEHOLDER = `粘贴完整 go.mod / go.sum，或每行一个模块：

module e2e.local/test

require (
\tgithub.com/gin-gonic/gin v1.10.0
\tgopkg.in/yaml.v3 v3.0.1 // indirect
)

go.sum 同样支持：
github.com/gin-gonic/gin v1.10.0 h1:abc=

也可以是：
github.com/gin-gonic/gin
github.com/gin-gonic/gin@v1.10.0`;

const NPM_PLACEHOLDER = `粘贴整个 package.json，或每行一个包名：

{
  "dependencies": {
    "vue": "^3.5.0",
    "axios": "^1.7.0"
  }
}

也可以是：
vue
axios
@babel/core`;

const PY_PLACEHOLDER = `粘贴整个 requirements.txt，或每行一个项目名：

requests==2.31.0
flask>=3.0,<4.0
numpy

注释与 pip 选项行会自动忽略：
# production
--index-url https://example.com/simple`;

const MVN_PLACEHOLDER = `粘贴 pom.xml、build.gradle，或每行一个坐标：

<dependency>
  <groupId>com.google.guava</groupId>
  <artifactId>guava</artifactId>
  <version>33.0.0-jre</version>
</dependency>

Gradle 同样支持：
implementation "com.squareup.okhttp3:okhttp:4.12.0"

也可以是：
com.google.guava:guava
junit:junit:4.13.2
org.example:demo:1.0.0:sources`;

const eco = ref('go'); // go | npm | py | mvn
const goDeps = ref('');
const goWithZip = ref(true);
const npmDeps = ref('');
const npmWithTarball = ref(true);
const pyDeps = ref('');
const pyWithFiles = ref(true);
const mvnDeps = ref('');
const mvnWithFiles = ref(true);
// 各生态各自的最近任务：必须用 reactive 对象承载（写入 jobs[eco] 才能触发响应式更新），
// 不能用 computed 组装临时对象再赋值（写入临时对象不生效，进度区会永远为空）
const jobs = reactive({ go: null, npm: null, py: null, mvn: null });
const submitting = ref(false);
let pollTimer = null;

const job = computed(() => jobs[eco.value]);
const running = computed(() => !!job.value && !job.value.finished);

function ecoQuery() {
  return eco.value === 'go' ? '' : `?eco=${eco.value}`;
}

async function load() {
  // 捕获请求发起时的生态：await 期间切换 eco 后，旧生态的响应不得写入新生态的槽位
  const e = eco.value;
  try {
    const data = await api.get(`/api/admin/preheat${e === 'go' ? '' : `?eco=${e}`}`);
    if (e !== eco.value) return;
    jobs[e] = data.job;
    schedulePoll();
  } catch {
    /* 页面加载失败不打断 */
  }
}

// 只在任务进行中且页面可见时轮询：切到后台标签页时暂停，回来立即刷一次
function schedulePoll() {
  clearTimeout(pollTimer);
  if (!running.value || document.visibilityState === 'hidden') return;
  pollTimer = setTimeout(async () => {
    const e = eco.value;
    try {
      const data = await api.get(`/api/admin/preheat${e === 'go' ? '' : `?eco=${e}`}`);
      if (e === eco.value) jobs[e] = data.job;
    } catch {
      /* 轮询失败下轮重试 */
    }
    schedulePoll();
  }, 2000);
}

function onVisibility() {
  if (document.visibilityState === 'visible') schedulePoll();
}

async function submit() {
  if (submitting.value) return;
  submitting.value = true;
  const body =
    eco.value === 'mvn'
      ? { deps: mvnDeps.value, withFiles: mvnWithFiles.value }
      : eco.value === 'py'
        ? { deps: pyDeps.value, withFiles: pyWithFiles.value }
        : eco.value === 'npm'
          ? { deps: npmDeps.value, withTarball: npmWithTarball.value }
          : { deps: goDeps.value, withZip: goWithZip.value };
  const unit = eco.value === 'mvn' ? '构件' : eco.value === 'py' ? '项目' : eco.value === 'npm' ? '包' : '模块';
  try {
    const res = await api.post(`/api/admin/preheat${ecoQuery()}`, body);
    notify(`预热任务已开始，共 ${res.total} 个${unit}`);
    jobs[eco.value] = res.job;
    schedulePoll();
  } catch (e) {
    notifyError(e.message);
  } finally {
    submitting.value = false;
  }
}

// 切生态时拉取该生态的最近任务
watch(eco, load);

onMounted(load);
onMounted(() => document.addEventListener('visibilitychange', onVisibility));
onUnmounted(() => {
  clearTimeout(pollTimer);
  document.removeEventListener('visibilitychange', onVisibility);
});

const stateClass = { done: 'ui-chip-ok', cached: 'ui-chip-ok', fail: 'ui-chip-warn', running: 'ui-chip-line' };
const stateText = { done: '已拉取', cached: '原本就有', fail: '失败', running: '拉取中' };
const withMeta = { go: '含 zip', npm: '含 tarball', py: '含发行文件', mvn: '含构件文件' };
</script>

<template>
  <div>
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">依赖预热</h1>
        <p class="ui-page-sub">
          {{
            eco === 'go'
              ? '粘贴 go.mod / go.sum 或 module@version 清单批量预拉 Go 模块。'
              : eco === 'npm'
                ? '粘贴 package.json 或包名清单批量预拉 npm 包。'
                : eco === 'py'
                  ? '粘贴 requirements.txt 或项目名清单批量预拉 Python 项目。'
                  : '粘贴 pom.xml、Gradle 坐标或 Maven 坐标清单批量预拉 Java 构件。'
          }}
        </p>
      </div>
    </header>

    <div class="ui-tabs" role="tablist" aria-label="选择生态" style="margin-bottom: 14px">
      <button class="ui-tab" :class="{ 'is-active': eco === 'go' }" type="button" role="tab" :aria-selected="eco === 'go'" @click="eco = 'go'">GO</button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'npm' }" type="button" role="tab" :aria-selected="eco === 'npm'" @click="eco = 'npm'">NPM</button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'py' }" type="button" role="tab" :aria-selected="eco === 'py'" @click="eco = 'py'">PYTHON</button>
      <button class="ui-tab" :class="{ 'is-active': eco === 'mvn' }" type="button" role="tab" :aria-selected="eco === 'mvn'" @click="eco = 'mvn'">JAVA</button>
    </div>

    <section class="ui-card">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">预热清单</h2>
          <p class="ui-card-sub">{{ eco === 'go' ? '每行一个模块，支持 go.mod / go.sum 与 module@version 格式' : eco === 'npm' ? '每行一个包名，支持整个 package.json' : eco === 'py' ? '每行一个项目名，支持 requirements.txt（含版本说明符与注释）' : '每行一个构件坐标，支持 pom.xml 依赖块、Gradle 脚本与 groupId:artifactId[:version][:classifier]' }}</p>
        </div>
      </div>
      <div class="ui-card-body">
        <form class="ui-stack" @submit.prevent="submit">
          <label class="ui-field">
            <span class="ui-label">{{ eco === 'go' ? '模块清单' : eco === 'npm' ? '包清单' : eco === 'py' ? '项目清单' : '构件清单' }}</span>
            <AutoTextarea v-if="eco === 'go'" v-model="goDeps" :placeholder="GO_PLACEHOLDER" spellcheck="false" />
            <AutoTextarea v-else-if="eco === 'npm'" v-model="npmDeps" :placeholder="NPM_PLACEHOLDER" spellcheck="false" />
            <AutoTextarea v-else-if="eco === 'py'" v-model="pyDeps" :placeholder="PY_PLACEHOLDER" spellcheck="false" />
            <AutoTextarea v-else v-model="mvnDeps" :placeholder="MVN_PLACEHOLDER" spellcheck="false" />
            <span v-if="eco === 'go'" class="ui-hint">未指定版本时默认拉取 latest，并缓存其 .info 与 .mod。</span>
            <span v-else-if="eco === 'npm'" class="ui-hint">仅取包名（@version 后缀会去掉），按 dist-tags.latest 拉取并缓存 tarball。</span>
            <span v-else-if="eco === 'py'" class="ui-hint">仅取项目名（版本说明符会去掉），缓存锚点页；勾选发行文件时同时下载 latest 版本的全部文件。</span>
            <span v-else class="ui-hint">未指定版本时取 release/latest；勾选构件文件时下载该版本的 .pom 与主 .jar（指定 classifier 时另加该文件）。</span>
          </label>

          <div class="ui-row-between" style="flex-wrap: wrap">
            <label v-if="eco === 'go'" class="ui-row" style="cursor: pointer; font-size: 12.5px">
              <input v-model="goWithZip" type="checkbox">
              同时下载各模块 latest 版本的 .zip（不勾选则仅缓存 .info / .mod 元数据）
            </label>
            <label v-else-if="eco === 'npm'" class="ui-row" style="cursor: pointer; font-size: 12.5px">
              <input v-model="npmWithTarball" type="checkbox">
              同时下载各包 latest 版本的 tarball（不勾选则仅缓存 packument 元数据）
            </label>
            <label v-else-if="eco === 'py'" class="ui-row" style="cursor: pointer; font-size: 12.5px">
              <input v-model="pyWithFiles" type="checkbox">
              同时下载各项目 latest 版本的发行文件（不勾选则仅缓存锚点页元数据）
            </label>
            <label v-else class="ui-row" style="cursor: pointer; font-size: 12.5px">
              <input v-model="mvnWithFiles" type="checkbox">
              同时下载各构件的 .pom 与 .jar（不勾选则仅缓存 maven-metadata.xml 元数据）
            </label>
            <button class="ui-btn ui-btn-primary" type="submit" :disabled="submitting">
              {{ submitting ? '提交中…' : '开始预热' }}
            </button>
          </div>
          <p class="ui-hint">单次最多 500 个条目（超出自动截断），任务在后台排队执行。</p>
        </form>
      </div>
    </section>

    <section class="ui-card ui-mt-16">
      <div class="ui-card-head">
        <div>
          <h2 class="ui-card-title">最近一次任务</h2>
          <p class="ui-card-sub">{{ job ? `任务 #${job.id} · 开始于 ${fmtStamp(job.createdAt)}` : '提交清单后在这里查看进度' }}</p>
        </div>
        <span v-if="job" class="ui-chip" :class="job.finished ? 'ui-chip-ok' : 'ui-chip-line'">
          {{ job.finished ? '已完成' : '进行中' }}
        </span>
      </div>

      <EmptyState v-if="!job" flat ico="fire" title="还没有预热任务" sub="提交上面的清单后，进度将显示在这里" />

      <template v-else>
        <div class="ui-card-body" style="padding-bottom: 0">
          <div class="ui-progress">
            <span :style="{ width: (job.total > 0 ? (job.done * 100) / job.total : 0) + '%' }"></span>
          </div>
          <div class="ui-row" style="flex-wrap: wrap; margin-top: 10px; font-size: 12.5px">
            <span class="ui-mono">{{ job.done }} / {{ job.total }}</span>
            <span v-if="job.failed" class="ui-chip ui-chip-warn">失败 {{ job.failed }}</span>
            <span class="ui-chip ui-chip-line">
              {{ eco === 'go' ? (job.withZip ? withMeta.go : '仅元数据') : eco === 'npm' ? (job.withTarball ? withMeta.npm : '仅元数据') : eco === 'py' ? (job.withFiles ? withMeta.py : '仅元数据') : (job.withFiles ? withMeta.mvn : '仅元数据') }}
            </span>
          </div>
        </div>

        <div class="ui-table-wrap ui-mt-16">
          <table class="ui-table ui-table-wide">
            <thead>
              <tr>
                <th>{{ eco === 'go' ? '模块' : eco === 'npm' ? '包' : eco === 'py' ? '项目' : '构件' }}</th>
                <th>状态</th>
                <th>版本</th>
                <th>体积</th>
                <th>备注</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="it in job.items" :key="it.name">
                <td class="ui-mono">{{ it.name }}</td>
                <td>
                  <span class="ui-chip" :class="stateClass[it.state] || 'ui-chip-line'">{{ stateText[it.state] || '排队中' }}</span>
                </td>
                <td class="num ui-mono">{{ it.version || '-' }}</td>
                <td class="num ui-mono">{{ it.size > 0 ? fmtBytes(it.size) : '-' }}</td>
                <td class="ui-muted" style="font-size: 12px">{{ it.err }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>
  </div>
</template>
