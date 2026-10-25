<script setup>
// 数字滚动：值变化时以 ease-out-quart 补间到新值，尊重系统减少动态偏好。
import { onMounted, onUnmounted, ref, watch } from 'vue';
import { fmtNum } from '../lib/format';

const props = defineProps({
  value: { type: Number, default: 0 },
  duration: { type: Number, default: 900 },
});

const el = ref(null);
let shown = 0;
let rafId = 0;

function animate(to) {
  // 新值到来先取消进行中的动画：连续变更不叠多个并行补间循环
  cancelAnimationFrame(rafId);
  if (!el.value) return;
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  if (reduced || to <= 0) {
    el.value.textContent = fmtNum(to);
    shown = to;
    return;
  }
  const from = shown;
  const start = performance.now();
  const step = (ts) => {
    // 动画进行中组件被卸载：Vue 已把模板 ref 置空，继续写 textContent 会抛错
    if (!el.value) return;
    const p = Math.min((ts - start) / props.duration, 1);
    const eased = 1 - Math.pow(1 - p, 4);
    el.value.textContent = fmtNum(Math.round(from + (to - from) * eased));
    if (p < 1) {
      rafId = requestAnimationFrame(step);
    } else {
      shown = to;
    }
  };
  rafId = requestAnimationFrame(step);
}

onMounted(() => animate(props.value));
watch(() => props.value, (v) => animate(v));
onUnmounted(() => cancelAnimationFrame(rafId));
</script>

<template>
  <span ref="el">{{ fmtNum(value) }}</span>
</template>
