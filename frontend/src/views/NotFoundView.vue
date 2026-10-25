<script setup>
// 通用 404：页面不存在（模块路径的 404 引导在模块详情页内展示）。
</script>

<template>
  <div class="ui-container ui-page" style="max-width: 820px">
    <div class="ui-card">
      <div class="ui-card-body" style="padding: 44px; text-align: center">
        <p class="ui-mono ui-muted" style="margin: 0; font-size: 64px; font-weight: 700; letter-spacing: -0.04em; line-height: 1">404</p>
        <h1 class="ui-page-title" style="margin-top: 14px">页面不存在</h1>
        <p class="ui-page-sub">你访问的地址不在这台镜像上。</p>
        <p class="ui-hint" style="margin-top: 18px">如果要找的是某个 Go 模块或 npm 包，先在 GO / NPM 页面搜索一下，或直接在客户端拉取它。</p>

        <div class="ui-row" style="justify-content: center; flex-wrap: wrap; margin-top: 24px">
          <RouterLink :to="{ name: 'home' }" class="ui-btn ui-btn-primary">返回首页</RouterLink>
          <RouterLink :to="{ name: 'goproxy' }" class="ui-btn ui-btn-ghost">GO</RouterLink>
          <RouterLink :to="{ name: 'npmregistry' }" class="ui-btn ui-btn-ghost">NPM</RouterLink>
          <RouterLink :to="{ name: 'pythonsimple' }" class="ui-btn ui-btn-ghost">PYTHON</RouterLink>
        </div>
      </div>
    </div>
  </div>
</template>
