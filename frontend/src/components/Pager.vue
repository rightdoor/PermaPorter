<script setup>
// 轻量分页器：窗口式页码，最多 7 个按钮。
import { computed } from 'vue';
import Icon from './Icon.vue';

const props = defineProps({
  page: { type: Number, required: true },
  totalPages: { type: Number, required: true },
});

const emit = defineEmits(['go']);

const visible = computed(() => {
  const total = Math.max(1, props.totalPages || 1);
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
  const start = Math.max(1, Math.min(props.page - 2, total - 4));
  return Array.from({ length: 5 }, (_, i) => start + i);
});

function go(n) {
  const next = Math.max(1, Math.min(props.totalPages || 1, n));
  if (next !== props.page) emit('go', next);
}
</script>

<template>
  <nav class="ui-pager" aria-label="分页">
    <button class="ui-pager-btn" type="button" :disabled="page <= 1" aria-label="上一页" @click="go(page - 1)">
      <Icon name="chevron-left" />
    </button>
    <button
      v-for="n in visible"
      :key="n"
      class="ui-pager-btn"
      :class="{ 'is-active': n === page }"
      type="button"
      :aria-current="n === page ? 'page' : undefined"
      :aria-label="`第 ${n} 页`"
      @click="go(n)"
    >
      {{ n }}
    </button>
    <button class="ui-pager-btn" type="button" :disabled="page >= totalPages" aria-label="下一页" @click="go(page + 1)">
      <Icon name="chevron-right" />
    </button>
  </nav>
</template>
