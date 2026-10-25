<script setup>
// 每日下载趋势：高度按相对峰值百分比，今日高亮；整段窗口零请求显示空态。
import { computed } from 'vue';
import EmptyState from './EmptyState.vue';

const props = defineProps({
  // [{ day: '2006-01-02', meta, zip|tarball, hits }]（field 指定产物字段）
  rows: { type: Array, default: () => [] },
  // 产物字段名：go 侧 zip、npm 侧 tarball（D6 双卡片各用一段）
  field: { type: String, default: 'zip' },
  unit: { type: String, default: 'zip' },
});

const bars = computed(() => {
  const rows = props.rows || [];
  const max = Math.max(0, ...rows.map((r) => r[props.field] ?? r.zip ?? 0));
  if (max <= 0) return [];
  const today = new Date();
  const todayStr = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`;
  return rows.map((r) => ({
    ...r,
    short: r.day.slice(8),
    label: r.day.slice(5),
    isToday: r.day === todayStr,
    pct: Math.max(4, Math.round(((r[props.field] ?? r.zip ?? 0) * 100) / max)),
    title: `${r.day} ${props.unit} 请求 ${r[props.field] ?? r.zip ?? 0} 次（命中 ${r.hits}）· 元数据 ${r.meta} 次`,
  }));
});
</script>

<template>
  <EmptyState
    v-if="!bars.length"
    flat
    ico="cloud-download-outline"
    title="暂无趋势数据"
    sub="客户端通过本镜像发起下载后，这里会按天统计请求量"
  />
  <div v-else class="ui-trend">
    <div class="ui-trend-bars" role="img" aria-label="最近 14 天每日请求趋势柱状图">
      <div
        v-for="b in bars"
        :key="b.day"
        class="ui-trend-bar"
        :class="{ 'is-last': b.isToday }"
        :title="b.title"
      >
        <i :style="{ height: b.pct + '%' }"></i>
      </div>
    </div>
    <div class="ui-trend-labels" aria-hidden="true">
      <span v-for="b in bars" :key="b.day">{{ b.label }}</span>
    </div>
  </div>
</template>
