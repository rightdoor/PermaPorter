<script setup>
// 通用确认对话框：替代 window.confirm（原生框可能被浏览器抑制，且无品牌样式）。
// Esc 取消；确认键自动聚焦，回车直接确认。
import { onMounted, onUnmounted, ref, watch, nextTick } from 'vue';
import Icon from './Icon.vue';

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '确认操作' },
  message: { type: String, required: true },
  confirmText: { type: String, default: '确认' },
  cancelText: { type: String, default: '取消' },
  danger: { type: Boolean, default: false },
});
const emit = defineEmits(['confirm', 'cancel']);

const confirmBtn = ref(null);

function close(result) {
  if (!props.open) return;
  emit(result ? 'confirm' : 'cancel');
}

function onKeydown(e) {
  if (!props.open) return;
  if (e.key === 'Escape') close(false);
}

let lockedScroll = false; // 只清理自己加的锁：嵌在抽屉内时，锁属于外层抽屉

watch(
  () => props.open,
  async (open) => {
    if (open) {
      if (!document.body.classList.contains('no-scroll')) {
        document.body.classList.add('no-scroll');
        lockedScroll = true;
      }
    } else if (lockedScroll) {
      document.body.classList.remove('no-scroll');
      lockedScroll = false;
    }
    if (open) {
      await nextTick();
      setTimeout(() => confirmBtn.value?.focus(), 50);
    }
  },
);

onMounted(() => window.addEventListener('keydown', onKeydown));
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown);
  if (lockedScroll) document.body.classList.remove('no-scroll');
});
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="ui-confirm-layer" @click.self="close(false)">
      <section class="ui-confirm" role="alertdialog" aria-modal="true" :aria-label="title">
        <h2 class="ui-confirm-title">
          <Icon :name="danger ? 'alert' : 'check-circle'" />
          {{ title }}
        </h2>
        <p class="ui-confirm-message">{{ message }}</p>
        <div class="ui-confirm-actions">
          <button class="ui-btn ui-btn-outline" type="button" @click="close(false)">
            {{ cancelText }}
          </button>
          <button
            ref="confirmBtn"
            class="ui-btn"
            :class="danger ? 'ui-btn-danger' : 'ui-btn-primary'"
            type="button"
            @click="close(true)"
          >
            {{ confirmText }}
          </button>
        </div>
      </section>
    </div>
  </Teleport>
</template>
