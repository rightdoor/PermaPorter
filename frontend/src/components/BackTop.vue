<script setup>
// 全局滚动快捷按钮：返回顶部 + 前往底部。
import { onMounted, onUnmounted, ref } from 'vue';
import Icon from './Icon.vue';

const canUp = ref(false);
const canDown = ref(false);
let ticking = false;

function update() {
  const y = window.scrollY;
  const scrollable = Math.max(
    document.documentElement.scrollHeight,
    document.body.scrollHeight
  ) - window.innerHeight;
  canUp.value = y > 320;
  canDown.value = scrollable > 320 && y < scrollable - 320;
}

function onScroll() {
  if (ticking) return;
  ticking = true;
  requestAnimationFrame(() => {
    ticking = false;
    update();
  });
}

function toTop() {
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  window.scrollTo({ top: 0, behavior: reduced ? 'auto' : 'smooth' });
}

function toBottom() {
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  window.scrollTo({ top: document.body.scrollHeight, behavior: reduced ? 'auto' : 'smooth' });
}

onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true });
  window.addEventListener('resize', onScroll, { passive: true });
  update();
});
onUnmounted(() => {
  window.removeEventListener('scroll', onScroll);
  window.removeEventListener('resize', onScroll);
});
</script>

<template>
  <div class="ui-scroll" aria-label="页面滚动">
    <button v-show="canUp" class="ui-scroll-btn" type="button" aria-label="返回顶部" title="返回顶部" @click="toTop">
      <Icon name="arrow-up" />
    </button>
    <button v-show="canDown" class="ui-scroll-btn" type="button" aria-label="前往底部" title="前往底部" @click="toBottom">
      <Icon name="arrow-down" />
    </button>
  </div>
</template>
