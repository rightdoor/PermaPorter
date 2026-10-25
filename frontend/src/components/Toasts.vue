<script setup>
// 全局轻提示：右上角堆叠，成功自动消失、错误停留更久。
import { toasts, dismiss } from '../lib/toast';
import Icon from './Icon.vue';

const icoByType = { success: 'check-circle', error: 'close-circle', warning: 'alert' };
</script>

<template>
  <div class="toast-stack" aria-live="polite">
    <div
      v-for="t in toasts"
      :key="t.id"
      class="alert alert-toast"
      :class="`alert-${t.type}`"
      role="status"
      @click="dismiss(t.id)"
    >
      <span class="alert-ico"><Icon :name="icoByType[t.type] || 'check-circle'" /></span>
      {{ t.message }}
    </div>
  </div>
</template>
