<script setup>
// 内联 MDI 图标：数据来自本地生成的 src/icons.js，运行时零外部调用。
import { computed } from 'vue';
import { ICONS } from '../icons';

const props = defineProps({
  name: { type: String, required: true },
});

const body = computed(() => ICONS[props.name] || '');
</script>

<template>
  <svg class="icon" aria-hidden="true" viewBox="0 0 24 24" v-html="body"></svg>
</template>
