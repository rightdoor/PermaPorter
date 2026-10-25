<script setup>
// 自动增高的多行文本框：不显示滚动条，也不允许手动拖拽。
import { nextTick, onMounted, ref, watch } from 'vue';

const props = defineProps({
  modelValue: { type: String, default: '' },
});

const emit = defineEmits(['update:modelValue']);
const el = ref(null);

function resize() {
  const node = el.value;
  if (!node) return;
  node.style.height = 'auto';
  node.style.height = `${node.scrollHeight}px`;
}

function onInput(event) {
  emit('update:modelValue', event.target.value);
  resize();
}

watch(
  () => props.modelValue,
  () => nextTick(resize),
);

onMounted(() => nextTick(resize));
</script>

<template>
  <textarea ref="el" class="ui-textarea" :value="modelValue" rows="1" @input="onInput"></textarea>
</template>
