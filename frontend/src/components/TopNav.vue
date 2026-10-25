<script setup>
// 公开端顶部导航：文档式文字链接；窄屏折叠菜单。
// 四生态导航（§8）：GO / NPM / PYTHON / JAVA 并列。
import { ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { app } from '../lib/store';
import { toggleTheme } from '../lib/theme';
import Icon from './Icon.vue';
import { LOGO_URL } from '../lib/assets';

const route = useRoute();
const menuOpen = ref(false);

const links = [
  { name: 'home', label: '首页', match: ['home'] },
  { name: 'goproxy', label: 'GO', match: ['goproxy', 'module'] },
  { name: 'npmregistry', label: 'NPM', match: ['npmregistry', 'package'] },
  { name: 'pythonsimple', label: 'PYTHON', match: ['pythonsimple', 'pyproject'] },
  { name: 'maven', label: 'JAVA', match: ['maven', 'mvnartifact'] },
];

function isActive(item) {
  return item.match.includes(route.name);
}

watch(
  () => route.fullPath,
  () => (menuOpen.value = false),
);
</script>

<template>
  <header class="ui-nav">
    <div class="ui-container ui-nav-inner">
      <RouterLink to="/" class="ui-brand" aria-label="PermaPorter 首页">
        <img class="ui-brand-mark" :src="LOGO_URL" alt="" />
        <span class="ui-brand-name">PermaPorter</span>
      </RouterLink>

      <nav class="ui-nav-links" aria-label="主导航">
        <RouterLink
          v-for="item in links"
          :key="item.name"
          :to="{ name: item.name }"
          class="ui-nav-link"
          active-class="ui-link-ignore"
          :class="{ 'router-link-active': isActive(item) }"
        >
          {{ item.label }}
        </RouterLink>
      </nav>

      <div class="ui-nav-actions">
        <button class="ui-btn ui-btn-ghost ui-btn-icon" type="button" aria-label="切换主题" title="切换日/夜模式" @click="toggleTheme">
          <span class="theme-ico" aria-hidden="true">
            <Icon class="icon-moon" name="weather-night" />
            <Icon class="icon-sun" name="weather-sunny" />
          </span>
        </button>
        <RouterLink v-if="app.authed" to="/admin/dashboard" class="ui-btn ui-btn-primary">管理后台</RouterLink>
        <RouterLink v-else to="/admin/login" class="ui-btn ui-btn-ghost">登录</RouterLink>
        <button
          class="ui-nav-toggle"
          type="button"
          :aria-expanded="menuOpen"
          aria-label="打开菜单"
          @click="menuOpen = !menuOpen"
        >
          <Icon :name="menuOpen ? 'close' : 'format-list-bulleted'" />
        </button>
      </div>
    </div>

    <div class="ui-mobile-panel" :class="{ 'is-open': menuOpen }">
      <div class="ui-container">
        <RouterLink
          v-for="item in links"
          :key="item.name"
          :to="{ name: item.name }"
          class="ui-nav-link"
          active-class="ui-link-ignore"
          :class="{ 'router-link-active': isActive(item) }"
        >
          {{ item.label }}
        </RouterLink>
        <RouterLink v-if="app.authed" to="/admin/dashboard" class="ui-nav-link">管理后台</RouterLink>
        <RouterLink v-else to="/admin/login" class="ui-nav-link">登录</RouterLink>
      </div>
    </div>
  </header>
</template>
