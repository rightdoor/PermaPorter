<script setup>
// 已缓存构件文件管理抽屉：查看/删除文件；构件记录保留（仅元数据构件是合法缓存）。
// Maven 文件名不全局唯一，删除按仓库相对路径（repoPath）定位。
import { onMounted, ref } from 'vue';
import { api, latestOnly, qs } from '../lib/api';
import { useDrawer } from '../lib/composables';
import { notifyError } from '../lib/toast';
import { fmtBytes, fmtTimeShort } from '../lib/format';
import Icon from './Icon.vue';
import ConfirmDialog from './ConfirmDialog.vue';

const props = defineProps({
  coords: { type: String, required: true }, // Maven 坐标 groupId:artifactId
});

const emit = defineEmits(['close', 'changed']);

// 抽屉交互：Esc 关闭、焦点陷阱、滚动锁定、关闭后焦点归还
const { layerRef } = useDrawer({ onClose: () => emit('close') });

const loadGuard = latestOnly(); // 竞态守卫：抽屉关闭/重开后丢弃慢响应
const files = ref(null); // null = 加载中
const error = ref('');
const pendingRemove = ref(null); // 待确认删除的文件对象

async function load() {
  const id = loadGuard.begin();
  try {
    const data = await api.get(`/api/admin/mvnartifact-files${qs({ coords: props.coords })}`);
    if (loadGuard.isStale(id)) return; // 抽屉已关闭或已重开：慢响应不回写
    files.value = data.files || [];
  } catch (e) {
    if (loadGuard.isStale(id)) return;
    error.value = e.message;
  }
}

function askRemove(f) {
  pendingRemove.value = f;
}

async function doRemove() {
  const f = pendingRemove.value;
  pendingRemove.value = null;
  if (!f) return;
  try {
    await api.del(`/api/admin/mvn-file${qs({ path: f.repoPath })}`);
    files.value = files.value.filter((x) => x.repoPath !== f.repoPath);
    // 删除改变了缓存计数：通知父页刷新统计（changed 事件在父页触发列表重载）
    emit('changed', false);
  } catch (e) {
    notifyError(`删除失败: ${e.message}`);
  }
}

onMounted(load);
</script>

<template>
  <Teleport to="body">
    <div ref="layerRef" class="ui-drawer-layer" role="dialog" aria-modal="true" aria-label="管理已缓存构件文件" tabindex="-1" @click.self="emit('close')">
      <section class="ui-drawer">
        <header class="ui-drawer-head">
          <div>
            <h2 class="ui-drawer-title">管理已缓存构件文件</h2>
            <p class="ui-drawer-sub ui-mono">{{ coords }}</p>
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
          <div v-else-if="files === null" class="ui-empty">
            <p class="ui-empty-title">加载中…</p>
          </div>
          <template v-else-if="files.length">
            <div v-for="f in files" :key="f.repoPath" class="ui-dep-row">
              <div class="ui-version-main">
                <div class="ui-version-line">
                  <span v-if="f.version" class="ui-version-tag">{{ f.version }}</span>
                  <span class="ui-chip ui-chip-ok">已缓存</span>
                </div>
                <span class="ui-version-meta ui-mono">{{ f.filename }}</span>
                <span class="ui-version-meta">{{ fmtBytes(f.fileSizeBytes) }} · {{ fmtTimeShort(f.cachedAt) }}</span>
              </div>
              <div class="ui-version-side">
                <button class="ui-btn ui-btn-danger ui-btn-sm" type="button" @click="askRemove(f)">删除</button>
              </div>
            </div>
          </template>
          <div v-else class="ui-empty">
            <Icon name="database" />
            <p class="ui-empty-title">该构件暂无已缓存文件</p>
          </div>
        </div>
      </section>
    </div>

    <ConfirmDialog
      :open="!!pendingRemove"
      title="删除确认"
      :message="pendingRemove ? `确定删除 ${pendingRemove.filename}？磁盘文件与缓存记录将一并移除（构件记录保留，文件下次请求会重新回源）。` : ''"
      confirm-text="确认删除"
      danger
      @confirm="doRemove"
      @cancel="pendingRemove = null"
    />
  </Teleport>
</template>
