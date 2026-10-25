<script setup>
// 系统配置：上游/出站代理按生态分区，修改密码写单库 platform_settings，
// 存储清理按生态执行（各套清理器规则不同、各自独立）。修改立即生效。
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import { api } from '../lib/api';
import { notify, notifyError } from '../lib/toast';
import Icon from '../components/Icon.vue';

const eco = ref('go'); // go | npm | py | mvn
const ecoSettings = ref({ go: {}, npm: {}, py: {}, mvn: {} });
const oldPassword = ref('');
const newPassword = ref('');
const confirmPassword = ref('');
// 数据库与端口（只读卡片：来自服务器配置文件，修改需编辑文件并重启）
const sysInfo = ref(null);

const proxyStatusMap = ref({ go: null, npm: null, py: null, mvn: null });
// 保存中状态：按操作名（如 goUpstream/password）懒创建的键，随用随设，无需手写全集
const saving = reactive({});
const cleanupState = ref({ go: null, npm: null, py: null, mvn: null });
// 每个生态独立的轮询计时器：同时点两个生态的清理时互不覆盖
const cleanupTimers = { go: null, npm: null, py: null, mvn: null };

const ECO_LABEL = { go: 'Go', npm: 'npm', py: 'Python', mvn: 'Java/Maven' };
// npm 生态无回退链语义；go/py/mvn 均有备用上游
const hasFallback = (ecoSide) => ecoSide !== 'npm';
const ecoQuery = (ecoSide) => (ecoSide === 'go' ? '' : `?eco=${ecoSide}`);

const settings = computed(() => ecoSettings.value[eco.value] || {});
const proxyStatus = computed(() => proxyStatusMap.value[eco.value] || null);

async function load() {
  try {
    const s = await api.get('/api/admin/settings');
    ecoSettings.value = {
      go: s.go || {},
      npm: s.npm || {},
      py: s.py || {},
      mvn: s.mvn || {},
    };
  } catch (e) {
    notifyError(e.message);
  }
}

async function loadSysInfo() {
  try {
    sysInfo.value = await api.get('/api/admin/system-info');
  } catch {
    /* 只读信息拉取失败不打扰页面（卡片保持隐藏） */
  }
}

onMounted(() => {
  load();
  loadSysInfo();
});

const DRIVER_LABEL = { sqlite: 'SQLite', postgresql: 'PostgreSQL' };

function saveUpstream(ecoSide) {
  const s = ecoSettings.value[ecoSide] || {};
  saving[ecoSide + 'Upstream'] = true;
  const body = hasFallback(ecoSide)
    ? { upstreamUrl: (s.upstreamUrl || '').trim(), fallbackUrl: (s.fallbackUrl || '').trim() }
    : { upstreamUrl: (s.upstreamUrl || '').trim() };
  api
    .post(`/api/admin/settings/upstream${ecoQuery(ecoSide)}`, body)
    .then(() => notify(`${ECO_LABEL[ecoSide]} 上游配置已保存，立即生效`))
    .catch((e) => notifyError(e.message))
    .finally(() => (saving[ecoSide + 'Upstream'] = false));
}

function saveProxy(ecoSide) {
  const s = ecoSettings.value[ecoSide] || {};
  saving[ecoSide + 'Proxy'] = true;
  api
    .post(`/api/admin/settings/proxy${ecoQuery(ecoSide)}`, { outboundProxy: (s.outboundProxy || '').trim() })
    .then(() => notify(`${ECO_LABEL[ecoSide]} 出站代理配置已保存，立即生效`))
    .catch((e) => notifyError(e.message))
    .finally(() => (saving[ecoSide + 'Proxy'] = false));
}

function testProxy(ecoSide) {
  const s = ecoSettings.value[ecoSide] || {};
  saving[ecoSide + 'ProxyTest'] = true;
  api
    .post(`/api/admin/settings/test-proxy${ecoQuery(ecoSide)}`, { outboundProxy: (s.outboundProxy || '').trim() })
    .then((res) => {
      proxyStatusMap.value = { ...proxyStatusMap.value, [ecoSide]: { ok: true, text: `已连接 (延迟 ${res.latencyMs}ms)` } };
    })
    .catch((e) => {
      proxyStatusMap.value = { ...proxyStatusMap.value, [ecoSide]: { ok: false, text: e.message } };
    })
    .finally(() => (saving[ecoSide + 'ProxyTest'] = false));
}

const savingTest = (ecoSide) => saving[ecoSide + 'ProxyTest'];

async function savePassword() {
  saving.password = true;
  try {
    await api.post('/api/admin/settings/password', {
      oldPassword: oldPassword.value,
      newPassword: newPassword.value,
      confirm: confirmPassword.value,
    });
    notify('密码已更新（所有生态共用，存储于单库）');
    oldPassword.value = '';
    newPassword.value = '';
    confirmPassword.value = '';
  } catch (e) {
    notifyError(e.message);
  } finally {
    saving.password = false;
  }
}

// 清理：按生态分别执行（各套清理器规则不同，不能互相套用）。
// 必须先 POST 触发后端任务再轮询状态；409 表示已有清理在执行，直接跟进其进度。
async function runCleanup(ecoSide) {
  saving[ecoSide + 'Cleanup'] = true;
  cleanupState.value = { ...cleanupState.value, [ecoSide]: null };
  clearTimeout(cleanupTimers[ecoSide]);
  try {
    const st = await api.post(`/api/admin/settings/cleanup${ecoQuery(ecoSide)}`);
    cleanupState.value = { ...cleanupState.value, [ecoSide]: st };
  } catch (e) {
    if (e.status !== 409) {
      saving[ecoSide + 'Cleanup'] = false;
      notifyError(e.message);
      return;
    }
    notify(`${ECO_LABEL[ecoSide]} 清理任务正在执行，跟进其进度`);
  }
  scheduleCleanupPoll(ecoSide);
}

function scheduleCleanupPoll(ecoSide) {
  clearTimeout(cleanupTimers[ecoSide]);
  cleanupTimers[ecoSide] = setTimeout(() => pollCleanup(ecoSide), 1500);
}

function pollCleanup(ecoSide) {
  api
    .get(`/api/admin/settings/cleanup${ecoQuery(ecoSide)}`)
    .then((st) => {
      cleanupState.value = { ...cleanupState.value, [ecoSide]: st };
      if (st.running) {
        scheduleCleanupPoll(ecoSide);
        return;
      }
      saving[ecoSide + 'Cleanup'] = false;
      if (st.err) notifyError(st.err);
      else if (st.message) notify(st.message);
    })
    .catch((e) => {
      saving[ecoSide + 'Cleanup'] = false;
      notifyError(e.message);
    });
}

onUnmounted(() => {
  for (const t of Object.values(cleanupTimers)) clearTimeout(t);
});
</script>

<template>
  <div>
    <header class="ui-page-head">
      <div>
        <h1 class="ui-page-title">系统配置</h1>
        <p class="ui-page-sub">上游与代理按生态隔离，修改后立即生效，无需重启服务。</p>
      </div>
    </header>

    <div class="ui-stack" style="max-width: 900px">
      <!-- 数据库与端口：只读（来自服务器配置文件，优先级 文件 > 环境变量 > 默认值） -->
      <section v-if="sysInfo" class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">数据库与端口</h2>
            <p class="ui-card-sub">启动必需项，只读展示——如需修改请编辑服务器配置文件并重启进程</p>
          </div>
        </div>
        <div class="ui-card-body">
          <div class="ui-cols-2">
            <div>
              <p class="ui-label">数据库</p>
              <p class="ui-mono">{{ DRIVER_LABEL[sysInfo.driver] || sysInfo.driver }}</p>
            </div>
            <div>
              <p class="ui-label">配置文件</p>
              <p class="ui-mono" style="overflow-wrap: anywhere">{{ sysInfo.configFile || '—' }}</p>
            </div>
          </div>
          <p class="ui-label ui-mt-8">五端口</p>
          <p class="ui-mono">
            控制台 :{{ sysInfo.ports?.ui }} · npm :{{ sysInfo.ports?.npm }} · GOPROXY :{{ sysInfo.ports?.go }} · PyPI :{{ sysInfo.ports?.py }} · Maven :{{ sysInfo.ports?.mvn }}
          </p>
          <div class="ui-alert" role="note">
            <Icon name="database" />
            <span>{{ sysInfo.hint }}</span>
          </div>
        </div>
      </section>

      <div class="ui-tabs" role="tablist" aria-label="选择生态">
        <button class="ui-tab" :class="{ 'is-active': eco === 'go' }" type="button" role="tab" :aria-selected="eco === 'go'" @click="eco = 'go'">GO</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'npm' }" type="button" role="tab" :aria-selected="eco === 'npm'" @click="eco = 'npm'">NPM</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'py' }" type="button" role="tab" :aria-selected="eco === 'py'" @click="eco = 'py'">PYTHON</button>
        <button class="ui-tab" :class="{ 'is-active': eco === 'mvn' }" type="button" role="tab" :aria-selected="eco === 'mvn'" @click="eco = 'mvn'">JAVA</button>
      </div>

      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">{{ ECO_LABEL[eco] }} 上游</h2>
            <p class="ui-card-sub">缓存未命中时回源的{{ eco === 'go' ? '模块代理' : eco === 'npm' ? 'registry' : eco === 'py' ? 'PyPI Simple' : 'Maven 仓库' }}地址（各生态互相独立）</p>
          </div>
        </div>
        <div class="ui-card-body">
          <form class="ui-stack" @submit.prevent="saveUpstream(eco)">
            <div class="ui-cols-2">
              <label class="ui-field">
                <span class="ui-label">主上游</span>
                <input v-model="settings.upstreamUrl" class="ui-input ui-mono" type="url" required>
                <span class="ui-hint">
                  {{ eco === 'py' ? 'PyPI Simple 根地址（以 /simple/ 结尾），未命中的锚点页与发行文件从这里拉取。' : eco === 'mvn' ? 'Maven 仓库根地址（如 https://repo.maven.apache.org/maven2），元数据与构件从这里拉取。' : '所有未命中的元数据与产物从这里拉取。' }}
                </span>
              </label>
              <label v-if="hasFallback(eco)" class="ui-field">
                <span class="ui-label">备用上游</span>
                <input v-model="settings.fallbackUrl" class="ui-input ui-mono" type="url" :placeholder="eco === 'go' ? 'https://proxy.golang.org' : eco === 'py' ? 'https://pypi.org/simple/' : 'https://repo1.maven.org/maven2'">
                <span class="ui-hint">主源网络错误 / 5xx / 403 时自动切换；404 不切换。{{ eco === 'npm' ? 'npm 生态无回退链。' : '' }}</span>
              </label>
            </div>
            <div class="ui-row-between">
              <span class="ui-hint">元数据按 TTL 刷新；产物一旦落盘永久缓存。</span>
              <button class="ui-btn ui-btn-primary" type="submit" :disabled="saving[eco + 'Upstream']">
                {{ saving[eco + 'Upstream'] ? '保存中…' : '保存上游配置' }}
              </button>
            </div>
          </form>
        </div>
      </section>

      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">出站代理</h2>
            <p class="ui-card-sub">{{ ECO_LABEL[eco] }} 生态访问上游时使用的 HTTP 代理（各生态互相独立）</p>
          </div>
        </div>
        <div class="ui-card-body">
          <form class="ui-stack" @submit.prevent="saveProxy(eco)">
            <label class="ui-field">
              <span class="ui-label">代理地址</span>
              <input
                v-model="settings.outboundProxy"
                class="ui-input ui-mono"
                placeholder="http://127.0.0.1:20171"
                spellcheck="false"
              >
              <span class="ui-hint">支持 http://、https://、socks5:// 协议（可含 user:pass@ 认证），留空表示直连。</span>
            </label>

            <div v-if="proxyStatus" class="ui-alert" :class="{ 'ui-alert-warn': !proxyStatus.ok }" role="status">
              <Icon :name="proxyStatus.ok ? 'check-circle' : 'close-circle'" />
              <span>{{ proxyStatus.text }}</span>
            </div>

            <div class="ui-row-between">
              <button class="ui-btn ui-btn-outline" type="button" :disabled="savingTest(eco)" @click="testProxy(eco)">
                <Icon name="web" />
                {{ savingTest(eco) ? '测试中…' : '测试连通性' }}
              </button>
              <button class="ui-btn ui-btn-primary" type="submit" :disabled="saving[eco + 'Proxy']">
                {{ saving[eco + 'Proxy'] ? '保存中…' : '保存代理' }}
              </button>
            </div>
          </form>
        </div>
      </section>

      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">管理员密码</h2>
            <p class="ui-card-sub">用于登录后台，所有生态共用（存储于单库 platform_settings）</p>
          </div>
        </div>
        <div class="ui-card-body">
          <form class="ui-stack" @submit.prevent="savePassword">
            <label class="ui-field">
              <span class="ui-label">当前密码</span>
              <input
                v-model="oldPassword"
                class="ui-input"
                type="password"
                placeholder="输入当前密码以确认身份"
                required
                autocomplete="current-password"
              >
            </label>
            <div class="ui-cols-2">
              <label class="ui-field">
                <span class="ui-label">新密码</span>
                <input
                  v-model="newPassword"
                  class="ui-input"
                  type="password"
                  placeholder="至少 6 位"
                  required
                  minlength="6"
                  autocomplete="new-password"
                >
              </label>
              <label class="ui-field">
                <span class="ui-label">确认新密码</span>
                <input
                  v-model="confirmPassword"
                  class="ui-input"
                  type="password"
                  placeholder="再次输入新密码"
                  required
                  autocomplete="new-password"
                >
              </label>
            </div>
            <div class="ui-row-between">
              <span class="ui-hint">修改后当前会话保持有效，其他会话需重新登录。</span>
              <button class="ui-btn ui-btn-primary" type="submit" :disabled="saving.password">
                {{ saving.password ? '更新中…' : '更新密码' }}
              </button>
            </div>
          </form>
        </div>
      </section>

      <section class="ui-card">
        <div class="ui-card-head">
          <div>
            <h2 class="ui-card-title">存储清理</h2>
            <p class="ui-card-sub">{{ ECO_LABEL[eco] }} 生态存储树，各套清理规则独立执行</p>
          </div>
        </div>
        <div class="ui-card-body">
          <div class="ui-row-between" style="flex-wrap: wrap">
            <span v-if="eco === 'go'" class="ui-hint">
              清理动作仅限：复位「有记录但文件丢失」的 zip 状态、删除无 DB 记录的孤立文件与超过 1
              小时的临时文件、剪枝空目录。<b>@v/list、latest.json 与仅元数据模块是合法缓存，永远不会被清理</b>。系统每 12 小时自动执行一次。
            </span>
            <span v-else-if="eco === 'npm'" class="ui-hint">
              清理动作仅限：删除文件缺失的版本记录、无记录的孤立 tarball、无包记录的 meta JSON、
              <b>仅元数据包</b>（30 分钟内活跃的除外）与空目录。系统每 12 小时自动执行一次。
            </span>
            <span v-else-if="eco === 'py'" class="ui-hint">
              清理动作仅限：复位「有记录但文件丢失」的缓存状态、删除无记录的孤立发行文件与下载临时残留。
              <b>项目记录与仅元数据项目（锚点页缓存）是合法缓存，永远不会被清理</b>。系统每 12 小时自动执行一次。
            </span>
            <span v-else class="ui-hint">
              清理动作仅限：复位「有记录但文件丢失」的缓存状态、删除无记录的孤立构件文件与下载临时残留、剪枝空目录。
              <b>构件记录、元数据缓存与仅元数据构件是合法缓存，永远不会被清理</b>。系统每 12 小时自动执行一次。
            </span>
            <button class="ui-btn ui-btn-outline" type="button" :disabled="saving[eco + 'Cleanup']" @click="runCleanup(eco)">
              {{ saving[eco + 'Cleanup'] ? '清理中…' : '立即清理' }}
            </button>
          </div>
          <p v-if="saving[eco + 'Cleanup']" class="ui-hint ui-mt-8">清理在后台执行，完成后会提示结果（大存储可能需要几十秒）。</p>
        </div>
      </section>
    </div>
  </div>
</template>
