<script setup>
// 公开首页：四生态接入卡片（GO / NPM / PYTHON / JAVA）与各生态缓存概览。
// 统计永远分生态展示，不做跨生态合计（D6）。
// 接入命令的地址由服务端注入（app.goProxy / app.npmRegistry / app.pyIndex / app.mavenRepo），前端不硬编码端口。
import { computed, onMounted, ref } from 'vue';
import { api } from '../lib/api';
import { fmtBytes, fmtNum } from '../lib/format';
import { app } from '../lib/store';
import Icon from '../components/Icon.vue';
import CmdBox from '../components/CmdBox.vue';
import { notifyError } from '../lib/toast';
import { LOGO_URL } from '../lib/assets';

const stats = ref(null);
const eco = ref('go'); // 接入配置生态 tab：go | npm | py | mvn（D6 不做独立生态页）
const goClient = ref('go113');
const npmClient = ref('npm');
const mvnClient = ref('maven');

const goProxy = computed(() => app.goProxy || '');
const npmRegistry = computed(() => app.npmRegistry || '');
const pyIndex = computed(() => app.pyIndex || '');
const mavenRepo = computed(() => app.mavenRepo || '');
// trusted-host 取 pyIndex 的 host:port（HTTP 内网源需要）
const pyHost = computed(() => {
  try {
    return new URL(pyIndex.value).host;
  } catch {
    return window.location.host;
  }
});

// Go 接入文案按推荐终端 / macOS-Linux / Windows 分组。
const goClients = computed(() => [
  {
    id: 'go113',
    label: 'Go 1.13+（推荐）',
    cmd: `go env -w GO111MODULE=on
go env -w GOPROXY=${goProxy.value},direct`,
    hint: '配置一次后当前账号下的所有项目都会使用本镜像；末尾的 ,direct 表示本镜像没有的模块回退直连拉取。',
  },
  {
    id: 'sh',
    label: 'macOS or Linux',
    cmd: `export GO111MODULE=on
export GOPROXY=${goProxy.value}`,
    extra: [
      { text: '或写入配置文件（永久生效）：', mono: false },
      { text: `echo "export GO111MODULE=on" >> ~/.profile`, mono: true },
      { text: `echo "export GOPROXY=${goProxy.value}" >> ~/.profile`, mono: true },
      { text: 'source ~/.profile', mono: true },
    ],
    hint: '仅对当前终端会话生效时直接执行上面两条 export 命令。',
  },
  {
    id: 'win',
    label: 'Windows',
    cmd: `$env:GO111MODULE = "on"
$env:GOPROXY = "${goProxy.value}"`,
    extra: [
      { text: '或通过图形界面永久设置：开始菜单搜索 “env” → “编辑系统环境变量” → “环境变量…” → 在上半部分“<你的用户名> 的用户变量”中点“新建…”：', mono: false },
      { text: '变量名 GO111MODULE，变量值 on', mono: true },
      { text: `再次“新建…”：变量名 GOPROXY，变量值 ${goProxy.value}`, mono: true },
    ],
    hint: 'PowerShell 方式仅对当前会话生效；图形界面方式对所有新终端生效。',
  },
]);

// npm 接入：各包管理器 registry 配置。
const npmClients = computed(() => [
  { id: 'npm', label: 'npm', cmd: `npm config set registry ${npmRegistry.value}` },
  { id: 'pnpm', label: 'pnpm', cmd: `pnpm config set registry ${npmRegistry.value}` },
  { id: 'yarn', label: 'yarn', cmd: `yarn config set registry ${npmRegistry.value}` },
  { id: 'bun', label: 'bun', cmd: `bun install --registry ${npmRegistry.value}` },
]);

// Maven/Gradle 接入：Maven 走 settings.xml 镜像，Gradle 走仓库声明。
const mvnClients = computed(() => [
  {
    id: 'maven',
    label: 'Maven',
    cmd: `<!-- ~/.m2/settings.xml -->\n<settings>\n  <mirrors>\n    <mirror>\n      <id>PermaPorter</id>\n      <mirrorOf>*</mirrorOf>\n      <name>PermaPorter</name>\n      <url>${mavenRepo.value}</url>\n    </mirror>\n  </mirrors>\n</settings>`,
    hint: 'mirrorOf=* 会把全部仓库请求（含中央仓库）重定向到本镜像；首次请求回源，产物永久缓存。',
  },
  {
    id: 'gradle',
    label: 'Gradle',
    cmd: `// build.gradle\nrepositories {\n    maven { url "${mavenRepo.value}" }\n    mavenCentral()\n}`,
    hint: '把本镜像放在仓库列表首位即可优先命中缓存；Gradle 与 Maven 使用同一套仓库布局。',
  },
  {
    id: 'gradle-kts',
    label: 'Gradle Kotlin DSL',
    cmd: `// build.gradle.kts\nrepositories {\n    maven("${mavenRepo.value}")\n    mavenCentral()\n}`,
    hint: 'Kotlin DSL 使用 maven(url) 简写；插件解析也可在 settings.gradle(.kts) 的 pluginManagement 中声明。',
  },
]);

const activeGoClient = computed(() => goClients.value.find((item) => item.id === goClient.value) || goClients.value[0]);
const activeNpmClient = computed(() => npmClients.value.find((item) => item.id === npmClient.value) || npmClients.value[0]);
const activeMvnClient = computed(() => mvnClients.value.find((item) => item.id === mvnClient.value) || mvnClients.value[0]);

// 四生态各自的统计段（永不合并）：go 侧看模块，npm 侧看包，py 侧看项目，mvn 侧看构件
const goStats = computed(() => {
  const s = stats.value?.go || {};
  return {
    count: s.moduleCount ?? 0,
    verCount: s.verCount ?? 0,
    totalSize: s.totalSize ?? 0,
  };
});
const npmStats = computed(() => {
  const s = stats.value?.npm || {};
  return {
    count: s.pkgCount ?? 0,
    verCount: s.verCount ?? 0,
    totalSize: s.totalSize ?? 0,
  };
});
const pyStats = computed(() => {
  const s = stats.value?.py || {};
  return {
    count: s.projectCount ?? 0,
    fileCount: s.fileCount ?? 0,
    totalSize: s.totalSize ?? 0,
  };
});
const mvnStats = computed(() => {
  const s = stats.value?.mvn || {};
  return {
    count: s.artifactCount ?? 0,
    fileCount: s.fileCount ?? 0,
    totalSize: s.totalSize ?? 0,
  };
});

onMounted(async () => {
  // 初始化模式（数据库未配置）下路由守卫已将本页导向部署向导，本分支为兜底：
  // /api/summary 未注册（仅完整控制台存在），跳过请求，统计保持为 0
  if (app.setupMode) return;
  try {
    stats.value = await api.get('/api/summary');
  } catch (e) {
    stats.value = null;
    if (e?.status !== 401) notifyError('统计数据加载失败');
  }
});
</script>

<template>
  <div class="ui-container ui-page ui-doc">
    <header class="ui-doc-head">
      <h1 class="ui-doc-title"><img class="ui-doc-logo" :src="LOGO_URL" alt="" />PermaPorter</h1>
      <p class="ui-doc-lead">内网四生态镜像：GOPROXY（Go 模块）、npm Registry（npm 包）、PyPI Simple（Python 包）与 Maven 仓库（Java 构件，Gradle 同协议）。一次回源，产物永久缓存，上游不可达也能装。</p>
    </header>

    <!-- 分生态统计：各占一行，永不跨生态合并 -->
    <div class="ui-facts-groups">
      <section class="ui-facts-group">
        <span class="ui-chip">GO</span>
        <dl class="ui-facts ui-facts-3">
          <div>
            <dt>已收录模块</dt>
            <dd>{{ fmtNum(goStats.count) }}</dd>
          </div>
          <div>
            <dt>可用版本</dt>
            <dd>{{ fmtNum(goStats.verCount) }}</dd>
          </div>
          <div>
            <dt>本地缓存</dt>
            <dd>{{ fmtBytes(goStats.totalSize) }}</dd>
          </div>
        </dl>
      </section>

      <section class="ui-facts-group">
        <span class="ui-chip ui-chip-warn">NPM</span>
        <dl class="ui-facts ui-facts-3">
          <div>
            <dt>已缓存包</dt>
            <dd>{{ fmtNum(npmStats.count) }}</dd>
          </div>
          <div>
            <dt>可用版本</dt>
            <dd>{{ fmtNum(npmStats.verCount) }}</dd>
          </div>
          <div>
            <dt>本地缓存</dt>
            <dd>{{ fmtBytes(npmStats.totalSize) }}</dd>
          </div>
        </dl>
      </section>

      <section class="ui-facts-group">
        <span class="ui-chip">PYTHON</span>
        <dl class="ui-facts ui-facts-3">
          <div>
            <dt>已收录项目</dt>
            <dd>{{ fmtNum(pyStats.count) }}</dd>
          </div>
          <div>
            <dt>可用文件</dt>
            <dd>{{ fmtNum(pyStats.fileCount) }}</dd>
          </div>
          <div>
            <dt>本地缓存</dt>
            <dd>{{ fmtBytes(pyStats.totalSize) }}</dd>
          </div>
        </dl>
      </section>

      <section class="ui-facts-group">
        <span class="ui-chip ui-chip-warn">JAVA</span>
        <dl class="ui-facts ui-facts-3">
          <div>
            <dt>已收录构件</dt>
            <dd>{{ fmtNum(mvnStats.count) }}</dd>
          </div>
          <div>
            <dt>可用文件</dt>
            <dd>{{ fmtNum(mvnStats.fileCount) }}</dd>
          </div>
          <div>
            <dt>本地缓存</dt>
            <dd>{{ fmtBytes(mvnStats.totalSize) }}</dd>
          </div>
        </dl>
      </section>
    </div>

    <!-- 接入地址一律由服务端注入（§8：前端不得硬编码端口）；地址未就绪时先不渲染命令 -->
    <section v-if="goProxy && npmRegistry && pyIndex && mavenRepo" class="ui-doc-section">
      <h2>接入配置</h2>
      <div class="ui-tabs" role="tablist" aria-label="选择生态">
        <button class="ui-tab" :class="{ 'is-active': eco === 'go' }" type="button" role="tab" :aria-selected="eco === 'go'" @click="eco = 'go'">Go 模块（GOPROXY）</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'npm' }" type="button" role="tab" :aria-selected="eco === 'npm'" @click="eco = 'npm'">npm 包（Registry）</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'py' }" type="button" role="tab" :aria-selected="eco === 'py'" @click="eco = 'py'">Python 包（PyPI）</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'mvn' }" type="button" role="tab" :aria-selected="eco === 'mvn'" @click="eco = 'mvn'">Java 构件（Maven / Gradle）</button>
      </div>

      <template v-if="eco === 'go'">
        <div class="ui-tabs" role="tablist" aria-label="选择配置方式" style="margin-top: 10px">
          <button
            v-for="item in goClients"
            :key="item.id"
            class="ui-tab"
            :class="{ 'is-active': goClient === item.id }"
            type="button"
            role="tab"
            :aria-selected="goClient === item.id"
            @click="goClient = item.id"
          >
            {{ item.label }}
          </button>
        </div>
        <CmdBox class="ui-mt-8" :cmd="activeGoClient.cmd" />
        <div v-if="activeGoClient.extra?.length" class="ui-stack" style="gap: 4px; margin-top: 14px">
          <p v-for="(line, i) in activeGoClient.extra" :key="i" class="ui-hint" style="margin: 0">
            <span v-if="line.mono" class="ui-mono">{{ line.text }}</span>
            <template v-else>{{ line.text }}</template>
          </p>
        </div>
        <p class="ui-hint ui-mt-8">{{ activeGoClient.hint }} 可用 <span class="ui-mono">go env GOPROXY</span> 验证。</p>
      </template>

      <template v-else-if="eco === 'npm'">
        <div class="ui-tabs" role="tablist" aria-label="选择包管理器" style="margin-top: 10px">
          <button
            v-for="item in npmClients"
            :key="item.id"
            class="ui-tab"
            :class="{ 'is-active': npmClient === item.id }"
            type="button"
            role="tab"
            :aria-selected="npmClient === item.id"
            @click="npmClient = item.id"
          >
            {{ item.label }}
          </button>
        </div>
        <CmdBox class="ui-mt-8" :cmd="activeNpmClient.cmd" />
        <p class="ui-hint ui-mt-8">
          配置一次后，当前账号下的项目都会使用内网 registry。可用
          <span class="ui-mono">npm view react version</span> 验证。
        </p>
      </template>

      <template v-else-if="eco === 'py'">
        <CmdBox class="ui-mt-8" :cmd="`pip config set global.index-url ${pyIndex}`" />
        <p class="ui-hint ui-mt-8">
          HTTP 源还需信任主机：<span class="ui-mono">pip config set global.trusted-host {{ pyHost }}</span>。可用 <span class="ui-mono">pip download --no-deps -d /tmp requests</span> 验证（首次回源后永久缓存，上游不可达也能装）。
        </p>
      </template>

      <template v-else>
        <div class="ui-tabs" role="tablist" aria-label="选择构建工具" style="margin-top: 10px">
          <button
            v-for="item in mvnClients"
            :key="item.id"
            class="ui-tab"
            :class="{ 'is-active': mvnClient === item.id }"
            type="button"
            role="tab"
            :aria-selected="mvnClient === item.id"
            @click="mvnClient = item.id"
          >
            {{ item.label }}
          </button>
        </div>
        <CmdBox class="ui-mt-8" :cmd="activeMvnClient.cmd" />
        <p class="ui-hint ui-mt-8">
          {{ activeMvnClient.hint }} 可用 <span class="ui-mono">mvn dependency:get -Dartifact=junit:junit:4.13.2</span> 验证。
        </p>
      </template>
    </section>

    <section class="ui-doc-section">
      <h2>开始使用</h2>
      <div class="ui-doc-links">
        <RouterLink :to="{ name: 'goproxy' }" class="ui-doc-link">
          <strong>GO</strong>
          <span>浏览已缓存的 Go 模块：版本、go.mod 依赖、体积与 README，支持页面内搜索。</span>
          <Icon name="arrow-right" />
        </RouterLink>
        <RouterLink :to="{ name: 'npmregistry' }" class="ui-doc-link">
          <strong>NPM</strong>
          <span>浏览已缓存的 npm 包：版本、依赖、体积与 README，支持页面内搜索。</span>
          <Icon name="arrow-right" />
        </RouterLink>
        <RouterLink :to="{ name: 'pythonsimple' }" class="ui-doc-link">
          <strong>PYTHON</strong>
          <span>浏览已缓存的 Python 项目：发行文件、体积与摘要，支持页面内搜索。</span>
          <Icon name="arrow-right" />
        </RouterLink>
        <RouterLink :to="{ name: 'maven' }" class="ui-doc-link">
          <strong>JAVA</strong>
          <span>浏览已缓存的 Maven 构件：版本、jar/pom 文件与体积，支持页面内搜索。</span>
          <Icon name="arrow-right" />
        </RouterLink>
      </div>
    </section>
  </div>
</template>
