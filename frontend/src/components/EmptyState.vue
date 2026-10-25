<script setup>
// 空状态：图标 + 标题 + 副标题 + 可选操作区。
import Icon from './Icon.vue';

defineProps({
  ico: { type: String, default: 'magnify' },
  title: { type: String, required: true },
  sub: { type: String, default: '' },
  flat: { type: Boolean, default: false },
});
</script>

<template>
  <div class="ui-empty" :class="{ 'empty-flat': flat }">
    <Icon :name="ico" />
    <h3 class="ui-empty-title">{{ title }}</h3>
    <p v-if="$slots.sub" class="ui-empty-sub"><slot name="sub" /></p>
    <p v-else-if="sub" class="ui-empty-sub">{{ sub }}</p>
    <div v-if="$slots.actions" class="ui-empty-actions">
      <slot name="actions" />
    </div>
    <slot />
  </div>
</template>
