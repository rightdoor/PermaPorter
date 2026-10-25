<script setup>
// 命令框："$" 前缀 + 复制按钮（复制成功短暂高亮）。
import { ref } from 'vue';
import Icon from './Icon.vue';

const props = defineProps({
  cmd: { type: String, required: true },
});

const copied = ref(false);
let timer = null;

async function copy() {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(props.cmd);
    } else {
      fallbackCopy(props.cmd);
    }
    copied.value = true;
    clearTimeout(timer);
    timer = setTimeout(() => (copied.value = false), 1800);
  } catch {
    fallbackCopy(props.cmd);
  }
}

function fallbackCopy(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try {
    document.execCommand('copy');
  } catch {
    /* 忽略 */
  }
  document.body.removeChild(ta);
}
</script>

<template>
  <div class="ui-cmd">
    <code>{{ cmd }}</code>
    <button type="button" :class="{ 'is-copied': copied }" aria-label="复制命令" @click="copy">
      <Icon name="content-copy" />
      <span>{{ copied ? '已复制' : '复制' }}</span>
    </button>
  </div>
</template>
