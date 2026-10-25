<script setup>
// 公开端布局：文档式顶部导航 + 路由出口 + 页脚。
import TopNav from '../components/TopNav.vue';
import { app } from '../lib/store';
</script>

<template>
  <div class="ui page-public">
    <TopNav />
    <main class="ui-public-main">
      <router-view />
    </main>
    <footer class="ui-footer">
      <div class="ui-container ui-footer-inner">
        <span class="ui-mono">v{{ app.version }}</span>
      </div>
    </footer>
  </div>
</template>
