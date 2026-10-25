<script setup>
// 已缓存版本管理抽屉：查看/删除版本；删空后通知父级刷新列表。
import { computed, onMounted, ref } from 'vue';
import { api, latestOnly, qs } from '../lib/api';
import { useDrawer } from '../lib/composables';
import { notifyError } from '../lib/toast';
import { fmtBytes, fmtTimeShort } from '../lib/format';
import Icon from './Icon.vue';
import ConfirmDialog from './ConfirmDialog.vue';

const props = defineProps({
  // kind: 'go'（模块版本，module-versions 接口）| 'npm'（包版本，package-versions 接口）
  kind: { type: String, default: 'go' },
  name: { type: String, required: true },
});

// go / npm 两类详情接口与参数映射（go: module=…；npm: pkg=…）——kind 仅这两种
const apiBase = props.kind === 'npm' ? '/api/admin/package-versions' : '/api/admin/module-versions';
const delBase = props.kind === 'npm' ? '/api/admin/package-version' : '/api/admin/module-version';
const nameParam = props.kind === 'npm' ? { pkg: props.name } : { module: props.name };
// 删除响应键按生态区分：go 返回 moduleRemoved，npm 返回 pkgRemoved
const removedKey = props.kind === 'npm' ? 'pkgRemoved' : 'moduleRemoved';
const entityName = props.kind === 'npm' ? '包' : '模块';
const emit = defineEmits(['close', 'changed']);

// 抽屉交互：Esc 关闭、焦点陷阱、滚动锁定、关闭后焦点归还
const { layerRef } = useDrawer({ onClose: () => emit('close') });

const loadGuard = latestOnly(); // 竞态守卫：抽屉关闭/重开后丢弃慢响应
const versions = ref(null); // null = 加载中
const error = ref('');
const pendingRemove = ref(null); // 待确认删除的版本对象

async function load() {
  const id = loadGuard.begin();
  try {
    const data = await api.get(`${apiBase}${qs(nameParam)}`);
    if (loadGuard.isStale(id)) return; // 抽屉已关闭或已重开：慢响应不回写
    versions.value = data.versions || [];
  } catch (e) {
    if (loadGuard.isStale(id)) return;
    error.value = e.message;
  }
}

function askRemove(v) {
  pendingRemove.value = v;
}

const removeTip = computed(() => {
  const v = pendingRemove.value;
  if (!v) return '';
  const last = versions.value.length === 1;
  if (props.kind === 'npm') {
    return last
      ? `确定删除版本 ${v.version}？删除后该包将被整体移除（含 packument 元数据）。`
      : `确定删除版本 ${v.version}？将同时删除磁盘上的 tarball 文件。`;
  }
  return last
    ? `确定删除版本 ${v.version}？删除后该模块将被整体移除（含 @v/list 元数据）。`
    : `确定删除版本 ${v.version}？将同时删除磁盘上的全部产物文件（.zip/.mod/.info）。`;
});

async function doRemove() {
  const v = pendingRemove.value;
  pendingRemove.value = null;
  if (!v) return;
  try {
    const res = await api.del(`${delBase}${qs({ ...nameParam, version: v.version })}`);
    if (res[removedKey]) {
      emit('changed', true);
    } else {
      versions.value = versions.value.filter((x) => x.version !== v.version);
    }
  } catch (e) {
    notifyError(`删除失败: ${e.message}`);
  }
}

onMounted(load);
</script>

<template>
  <Teleport to="body">
    <div ref="layerRef" class="ui-drawer-layer" role="dialog" aria-modal="true" aria-label="管理已缓存版本" tabindex="-1" @click.self="emit('close')">
      <section class="ui-drawer">
        <header class="ui-drawer-head">
          <div>
            <h2 class="ui-drawer-title">管理已缓存版本</h2>
            <p class="ui-drawer-sub ui-mono">{{ name }}</p>
          </div>
          <button class="ui-icon-btn" type="button" title="关闭（Esc）" aria-label="关闭" @click="emit('close')">
            <Icon name="close" />
          </button>
        </header>

        <div class="ui-drawer-body">
          <div v-if="error" class="ui-empty">
            <Icon name="alert" />
            <p class="ui-empty-title">加载失败</p>
            <p class="ui-empty-sub">{{ error }}</p>
          </div>
          <div v-else-if="versions === null" class="ui-empty">
            <p class="ui-empty-title">加载中…</p>
          </div>
          <template v-else-if="versions.length">
            <div v-for="v in versions" :key="v.version" class="ui-dep-row">
              <div class="ui-version-main">
                <div class="ui-version-line">
                  <span class="ui-version-tag">{{ v.version }}</span>
                  <span v-if="v.pseudo" class="ui-chip ui-chip-line">pseudo</span>
                  <span class="ui-chip" :class="v.zipCached ? 'ui-chip-ok' : 'ui-chip-line'">
                    {{ v.zipCached ? 'zip 已缓存' : '仅元数据' }}
                  </span>
                </div>
                <span class="ui-version-meta">
                  {{ v.zipCached ? fmtBytes(v.zipSizeBytes) : '无 zip' }} · {{ fmtTimeShort(v.cachedAt) }}
                </span>
              </div>
              <div class="ui-version-side">
                <RouterLink
                  class="ui-btn ui-btn-ghost ui-btn-sm"
                  :to="{ name: props.kind === 'npm' ? 'package' : 'module', params: props.kind === 'npm' ? { pkg: name } : { path: name }, query: { v: v.version } }"
                >
                  查看
                </RouterLink>
                <button class="ui-btn ui-btn-danger ui-btn-sm" type="button" @click="askRemove(v)">删除</button>
              </div>
            </div>
          </template>
          <div v-else class="ui-empty">
            <Icon name="hexagon-multiple" />
            <p class="ui-empty-title">该{{ entityName }}暂无已缓存版本</p>
          </div>
        </div>
      </section>
    </div>

    <ConfirmDialog
      :open="!!pendingRemove"
      title="删除确认"
      :message="removeTip"
      confirm-text="确认删除"
      danger
      @confirm="doRemove"
      @cancel="pendingRemove = null"
    />
  </Teleport>
</template>
