<script setup>
// 初始化向导（/admin/wizard）：数据库未配置（配置文件 [database].driver 为空）时的
// 首次部署入口。分步收集：数据库（sqlite / postgresql + 测试连接）、五端口、
// 四生态初始上游、管理员密码；完成后后端原地启动全部端口（无需重启进程）。
// 与 /admin/setup 密码初始化页的关系：setupMode 下管理端统一导向本向导；
// 数据库已配置而仅缺密码时仍走 /admin/setup（两入口互斥，不会打架）。
import { computed, onMounted, reactive, ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../lib/api';
import { app, loadState } from '../lib/store';
import Icon from '../components/Icon.vue';
import { LOGO_URL } from '../lib/assets';

const router = useRouter();

const tokenRequired = ref(false);
const token = ref('');
const driver = ref('sqlite');
const sqliteDsn = ref(''); // 留空 = 默认 data/permaporter.db
const pg = reactive({ host: '', port: 5432, user: '', password: '', database: '', sslmode: 'prefer' });
const ports = reactive({ ui: 9090, npm: 9091, go: 9092, py: 9093, mvn: 9094 });
const upstreams = reactive({ go: '', npm: '', py: '', mvn: '' });
const password = ref('');
const confirm = ref('');

const configFile = ref('');
const err = ref('');
const testing = ref(false);
const testResult = ref(null); // { ok, text }
const submitting = ref(false);
const done = ref(false);
const donePorts = ref(null);

const STEP_META = {
  token: '访问令牌',
  db: '数据库',
  ports: '端口',
  upstream: '上游源',
  password: '管理员密码',
};
const stepIds = computed(() => (tokenRequired.value ? ['token', 'db', 'ports', 'upstream', 'password'] : ['db', 'ports', 'upstream', 'password']));
const stepIndex = ref(0);
const currentStep = computed(() => stepIds.value[stepIndex.value]);

const PORT_FIELDS = [
  ['ui', '控制台', '本页面所在端口（SPA + 管理 API）'],
  ['npm', 'npm Registry', 'packument / tarball / audit 透传'],
  ['go', 'GOPROXY', '@v/list 与版本产物'],
  ['py', 'PyPI Simple', '/simple/ 锚点页与发行文件'],
  ['mvn', 'Maven 仓库', 'maven-metadata.xml 与构件文件'],
];
const UPSTREAM_FIELDS = [
  ['go', 'Go 主上游', 'https://proxy.golang.org'],
  ['npm', 'npm 上游', 'https://registry.npmjs.org'],
  ['py', 'PyPI 上游', 'https://pypi.org/simple/'],
  ['mvn', 'Maven 上游', 'https://repo.maven.apache.org/maven2'],
];

// 向导已完成的旧标签页自愈：状态过期（他处已完成初始化或服务已重启）时不再滞留表单，
// 刷新服务端状态后回登录页——初始化完成后向导不允许再次进入（端点侧 409/404 拒绝）。
async function leaveStale() {
  await loadState();
  if (!app.setupMode) router.replace({ name: 'login' });
}

async function load() {
  try {
    const s = await api.get('/api/setup/status');
    configFile.value = s.configFile || '';
    tokenRequired.value = !!s.initTokenRequired;
    if (s.ports) Object.assign(ports, s.ports);
    if (s.upstreams) {
      // 仅取四生态主上游（goFallback 已从向导收集范围移除，备用上游在系统配置按生态设置）
      upstreams.go = s.upstreams.go || '';
      upstreams.npm = s.upstreams.npm || '';
      upstreams.py = s.upstreams.py || '';
      upstreams.mvn = s.upstreams.mvn || '';
    }
  } catch (e) {
    if (e?.status === 409 || e?.status === 404) {
      await leaveStale();
      if (!app.setupMode) return;
    }
    err.value = e.message;
  }
}
onMounted(load);

function validateStep() {
  const id = currentStep.value;
  if (id === 'token') {
    if (!token.value) return '请输入初始化令牌';
  } else if (id === 'db') {
    if (driver.value === 'postgresql') {
      if (!pg.host || !pg.user || !pg.password || !pg.database) return 'PostgreSQL 的主机 / 用户名 / 密码 / 库名均为必填';
      if (!(pg.port >= 1 && pg.port <= 65535)) return 'PostgreSQL 端口必须在 1-65535 范围内';
    }
  } else if (id === 'ports') {
    for (const [k] of PORT_FIELDS) {
      if (!(ports[k] >= 1 && ports[k] <= 65535)) return '端口必须在 1-65535 范围内';
    }
    if (new Set(Object.values(ports)).size !== 5) return '五个端口必须互不相同';
  } else if (id === 'upstream') {
    const required = [
      ['Go 主上游', upstreams.go],
      ['npm 上游', upstreams.npm],
      ['PyPI 上游', upstreams.py],
      ['Maven 上游', upstreams.mvn],
    ];
    for (const [label, v] of required) {
      if (!v) return `${label}不能为空`;
      if (!/^https?:\/\//.test(v)) return `${label}必须以 http:// 或 https:// 开头`;
    }
  } else if (id === 'password') {
    if (password.value.length < 6) return '密码长度至少为 6 位';
    if (password.value !== confirm.value) return '两次输入的密码不一致';
  }
  return '';
}

function next() {
  const e = validateStep();
  if (e) {
    err.value = e;
    return;
  }
  err.value = '';
  if (stepIndex.value < stepIds.value.length - 1) {
    stepIndex.value += 1;
    return;
  }
  submit();
}

function prev() {
  err.value = '';
  testResult.value = null;
  if (stepIndex.value > 0) stepIndex.value -= 1;
}

function dbPayload() {
  return driver.value === 'postgresql'
    ? { token: token.value, driver: 'postgresql', ...pg }
    : { token: token.value, driver: 'sqlite', dsn: sqliteDsn.value };
}

async function testDatabase() {
  testing.value = true;
  testResult.value = null;
  try {
    const r = await api.post('/api/setup/test-database', dbPayload());
    testResult.value = { ok: true, text: r.message || '连接成功' };
  } catch (e) {
    testResult.value = { ok: false, text: e.message };
  } finally {
    testing.value = false;
  }
}

async function submit() {
  submitting.value = true;
  err.value = '';
  try {
    const res = await api.post('/api/setup/initialize', {
      ...dbPayload(),
      ports: { ...ports },
      upstreams: { ...upstreams },
      adminPassword: password.value,
      confirm: confirm.value,
    });
    done.value = true;
    donePorts.value = res.ports || { ...ports };
    app.setupMode = false;
    app.initialized = true;
    loadState(); // 刷新服务端注入的协议端口地址
  } catch (e) {
    if (e?.status === 409 || e?.status === 404) {
      await leaveStale();
      if (!app.setupMode) return;
    }
    err.value = e.message;
  } finally {
    submitting.value = false;
  }
}

function goLogin() {
  router.push({ name: 'login' });
}
</script>

<template>
  <div class="ui ui-auth">
    <div class="ui-card ui-auth-card" style="max-width: 640px">
      <RouterLink to="/" class="ui-brand" aria-label="返回首页">
        <img class="ui-brand-mark" :src="LOGO_URL" alt="" />
        <span class="ui-brand-name">PermaPorter</span>
      </RouterLink>

      <!-- 完成页 -->
      <template v-if="done">
        <h1 class="ui-auth-title">初始化完成</h1>
        <p class="ui-auth-sub">全部端口已就绪，正在提供服务。配置已写入服务器配置文件，之后如需修改数据库或端口，请编辑配置文件并重启进程。</p>

        <div class="ui-alert" role="status">
          <Icon name="check-circle" />
          <div>
            <b>五个端口已全部就绪：</b>
            <div class="ui-mono ui-mt-4" style="font-size: 13px">
              <div>控制台&nbsp;&nbsp;&nbsp;:{{ donePorts.ui }}</div>
              <div>npm&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;:{{ donePorts.npm }}</div>
              <div>GOPROXY&nbsp;&nbsp;:{{ donePorts.go }}</div>
              <div>PyPI&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;:{{ donePorts.py }}</div>
              <div>Maven&nbsp;&nbsp;&nbsp;&nbsp;:{{ donePorts.mvn }}</div>
            </div>
          </div>
        </div>
        <div class="ui-alert ui-alert-warn" role="alert">
          <Icon name="close-circle" />
          <span>管理员密码已按本向导所填值设置，此后向导不再开放；日常改密请登录后在「系统配置」进行。如遇密码遗失，可按文档手工清空配置文件 [database] 段后重启重新进入向导——该操作会重置管理员密码。</span>
        </div>

        <button class="ui-btn ui-btn-primary ui-mt-12" type="button" @click="goLogin">前往登录</button>
      </template>

      <!-- 分步表单 -->
      <template v-else>
        <h1 class="ui-auth-title">初始化向导</h1>
        <p class="ui-auth-sub">首次部署：依次配置数据库、端口、上游源与管理员密码，完成后全部端口就地启动（无需重启）。</p>

        <!-- 步骤指示器 -->
        <ol class="ui-wizard-steps" aria-label="向导步骤">
          <li
            v-for="(id, i) in stepIds"
            :key="id"
            :class="{ 'is-active': i === stepIndex, 'is-done': i < stepIndex }"
          >
            <span class="ui-wizard-dot">{{ i < stepIndex ? '✓' : i + 1 }}</span>
            <span>{{ STEP_META[id] }}</span>
          </li>
        </ol>

        <div v-if="err" class="ui-alert ui-alert-warn" role="alert">
          <Icon name="close-circle" />
          <span>{{ err }}</span>
        </div>

        <form class="ui-stack" @submit.prevent="next">
          <!-- ① 访问令牌（INIT_TOKEN 设置时） -->
          <template v-if="currentStep === 'token'">
            <label class="ui-field">
              <span class="ui-label">初始化令牌</span>
              <input v-model="token" class="ui-input ui-mono" type="password" placeholder="输入部署时设置的 INIT_TOKEN" autocomplete="off">
              <span class="ui-hint">本部署设置了 INIT_TOKEN 环境变量，向导操作前需先验证令牌。</span>
            </label>
          </template>

          <!-- ② 数据库 -->
          <template v-else-if="currentStep === 'db'">
            <div class="ui-field">
              <span class="ui-label">数据库类型</span>
              <div class="ui-row" style="gap: 12px">
                <label class="ui-radio">
                  <input v-model="driver" type="radio" value="sqlite">
                  <span><b>SQLite</b>（默认）— 单文件库，零外部依赖</span>
                </label>
                <label class="ui-radio">
                  <input v-model="driver" type="radio" value="postgresql">
                  <span><b>PostgreSQL</b> — 外部数据库服务</span>
                </label>
              </div>
            </div>

            <template v-if="driver === 'sqlite'">
              <label class="ui-field">
                <span class="ui-label">数据库文件（可选）</span>
                <input v-model="sqliteDsn" class="ui-input ui-mono" type="text" placeholder="留空使用默认 data/permaporter.db" spellcheck="false">
                <span class="ui-hint">SQLite DSN（file: 路径）。默认形态即可满足绝大多数部署。</span>
              </label>
            </template>

            <template v-else>
              <div class="ui-cols-2">
                <label class="ui-field">
                  <span class="ui-label">主机</span>
                  <input v-model="pg.host" class="ui-input ui-mono" type="text" placeholder="postgres（compose 内）或 127.0.0.1" required spellcheck="false">
                </label>
                <label class="ui-field">
                  <span class="ui-label">端口</span>
                  <input v-model.number="pg.port" class="ui-input ui-mono" type="number" min="1" max="65535" required>
                </label>
              </div>
              <div class="ui-cols-2">
                <label class="ui-field">
                  <span class="ui-label">用户名</span>
                  <input v-model="pg.user" class="ui-input ui-mono" type="text" required autocomplete="off" spellcheck="false">
                </label>
                <label class="ui-field">
                  <span class="ui-label">密码</span>
                  <input v-model="pg.password" class="ui-input ui-mono" type="password" required autocomplete="new-password">
                </label>
              </div>
              <div class="ui-cols-2">
                <label class="ui-field">
                  <span class="ui-label">数据库名</span>
                  <input v-model="pg.database" class="ui-input ui-mono" type="text" required spellcheck="false">
                </label>
                <label class="ui-field">
                  <span class="ui-label">sslmode</span>
                  <select v-model="pg.sslmode" class="ui-input">
                    <option>prefer</option>
                    <option>disable</option>
                    <option>require</option>
                    <option>verify-ca</option>
                    <option>verify-full</option>
                  </select>
                </label>
              </div>
              <div class="ui-row-between">
                <span class="ui-hint">连接信息将以明文写入服务器配置文件（权限 0600）。</span>
                <button class="ui-btn ui-btn-outline" type="button" :disabled="testing" @click="testDatabase">
                  {{ testing ? '测试中…' : '测试连接' }}
                </button>
              </div>
              <div v-if="testResult" class="ui-alert" :class="{ 'ui-alert-warn': !testResult.ok }" role="status">
                <Icon :name="testResult.ok ? 'check-circle' : 'close-circle'" />
                <span>{{ testResult.text }}</span>
              </div>
            </template>
          </template>

          <!-- ③ 端口 -->
          <template v-else-if="currentStep === 'ports'">
            <label v-for="[key, label, sub] in PORT_FIELDS" :key="key" class="ui-field">
              <span class="ui-label">{{ label }}（{{ key }}）</span>
              <input v-model.number="ports[key]" class="ui-input ui-mono" type="number" min="1" max="65535" required>
              <span class="ui-hint">{{ sub }}。修改后需同步客户端配置、防火墙与端口映射。</span>
            </label>
            <span class="ui-hint">五个端口必须互不相同（容器部署时此处为容器内后端端口，保持默认即可）。</span>
          </template>

          <!-- ④ 上游源 -->
          <template v-else-if="currentStep === 'upstream'">
            <label v-for="[key, label, ph] in UPSTREAM_FIELDS" :key="key" class="ui-field">
              <span class="ui-label">{{ label }}</span>
              <input v-model="upstreams[key]" class="ui-input ui-mono" type="url" :placeholder="ph" required>
            </label>
            <span class="ui-hint">写入各生态初始上游（备用上游此后可在「系统配置」按生态设置；国内网络可替换为镜像源）。</span>
          </template>

          <!-- ⑤ 管理员密码 -->
          <template v-else-if="currentStep === 'password'">
            <label class="ui-field">
              <span class="ui-label">管理密码</span>
              <input v-model="password" class="ui-input" type="password" placeholder="至少 6 位" required minlength="6" autocomplete="new-password">
            </label>
            <label class="ui-field">
              <span class="ui-label">确认密码</span>
              <input v-model="confirm" class="ui-input" type="password" placeholder="再次输入" required autocomplete="new-password">
            </label>
            <div class="ui-alert ui-alert-warn" role="alert">
              <Icon name="close-circle" />
              <span>若目标数据库中已存在管理员密码，本步骤会将其<b>重置</b>为此次所填值。</span>
            </div>
          </template>

          <div class="ui-row-between ui-mt-8">
            <button class="ui-btn ui-btn-outline" type="button" :disabled="stepIndex === 0" @click="prev">上一步</button>
            <button class="ui-btn ui-btn-primary" type="submit" :disabled="submitting">
              {{ submitting ? '初始化中…' : stepIndex === stepIds.length - 1 ? '完成初始化' : '下一步' }}
            </button>
          </div>
        </form>

        <p v-if="configFile" class="ui-hint" style="text-align: center">配置文件：{{ configFile }}</p>
      </template>
    </div>
  </div>
</template>
