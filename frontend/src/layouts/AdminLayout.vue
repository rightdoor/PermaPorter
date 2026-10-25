<script setup>
// 后台布局：桌面固定侧栏，窄屏抽屉；顶部栏显示当前页面与全局操作。
// 后台导航按生态分区（§8）：GO / NPM / PYTHON / JAVA；预热、日志、设置为各生态共用页面。
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { app } from '../lib/store';
import { api } from '../lib/api';
import { toggleTheme } from '../lib/theme';
import { notify } from '../lib/toast';
import Icon from '../components/Icon.vue';
import { LOGO_URL } from '../lib/assets';

const route = useRoute();
const router = useRouter();
const bare = computed(() => !!route.meta.bare);
const drawerOpen = ref(false);

const navGroups = [
  {
    label: '总览',
    items: [{ name: 'dashboard', label: '仪表盘', icon: 'view-dashboard' }],
  },
  {
    label: 'GO',
    items: [{ name: 'admin-modules', label: '模块管理', icon: 'hexagon-multiple' }],
  },
  {
    label: 'NPM',
    items: [{ name: 'admin-packages', label: '包管理', icon: 'package-variant-closed' }],
  },
  {
    label: 'PYTHON',
    items: [{ name: 'admin-pyprojects', label: 'Py 项目管理', icon: 'hexagon-multiple' }],
  },
  {
    label: 'JAVA',
    items: [{ name: 'admin-mvnartifacts', label: 'Maven 构件管理', icon: 'database' }],
  },
  {
    label: '运维',
    items: [
      { name: 'preheat', label: '依赖预热', icon: 'fire' },
      { name: 'logs', label: '请求日志', icon: 'format-list-bulleted' },
      { name: 'settings', label: '系统配置', icon: 'cog' },
    ],
  },
];

const titles = {
  dashboard: '仪表盘',
  'admin-modules': '模块管理',
  'admin-packages': '包管理',
  'admin-pyprojects': 'Py 项目管理',
  'admin-mvnartifacts': 'Maven 构件管理',
  preheat: '依赖预热',
  logs: '请求日志',
  settings: '系统配置',
};

const title = computed(() => titles[route.name] || '管理后台');

watch(
  () => route.fullPath,
  () => (drawerOpen.value = false),
);

async function logout() {
  try {
    await api.post('/api/auth/logout');
  } catch {
    /* 会话已失效也可直接离开 */
  }
  app.authed = false;
  notify('已退出登录');
  router.push({ name: 'login' });
}
</script>

<template>
  <router-view v-if="bare" />
  <div v-else class="ui ui-admin">
    <button v-if="drawerOpen" class="ui-overlay" type="button" aria-label="关闭菜单" @click="drawerOpen = false"></button>

    <aside class="ui-side" :class="{ 'is-open': drawerOpen }">
      <RouterLink to="/" class="ui-side-brand ui-brand">
        <img class="ui-brand-mark" :src="LOGO_URL" alt="" />
        <span class="ui-brand-name">PermaPorter</span>
      </RouterLink>

      <template v-for="group in navGroups" :key="group.label">
        <div class="ui-side-label">{{ group.label }}</div>
        <nav :aria-label="`后台导航 - ${group.label}`">
          <RouterLink v-for="n in group.items" :key="n.name" :to="{ name: n.name }" class="ui-side-link">
            <Icon :name="n.icon" />
            <span>{{ n.label }}</span>
          </RouterLink>
        </nav>
      </template>

      <div class="ui-side-foot">
        <RouterLink to="/" class="ui-side-link">
          <Icon name="web" />
          <span>查看前台</span>
        </RouterLink>
        <div class="ui-user">
          <span class="ui-user-avatar">{{ app.authed ? 'A' : '?' }}</span>
          <div class="ui-grow">
            <div class="ui-user-name">{{ app.authed ? '管理员' : '未登录' }}</div>
            <div class="ui-user-role">内网镜像控制台</div>
          </div>
          <button class="ui-btn ui-btn-ghost ui-btn-sm" type="button" aria-label="退出登录" title="退出登录" @click="logout">
            <Icon name="logout" />
          </button>
        </div>
      </div>
    </aside>

    <div class="ui-admin-main">
      <header class="ui-topbar">
        <button class="ui-drawer-btn" type="button" aria-label="打开菜单" @click="drawerOpen = true">
          <Icon name="format-list-bulleted" />
        </button>
        <span class="ui-topbar-crumb">管理后台 /</span>
        <span class="ui-topbar-title">{{ title }}</span>

        <div class="ui-topbar-actions">
          <button class="ui-btn ui-btn-ghost ui-btn-icon ui-btn-sm" type="button" aria-label="切换主题" title="切换日/夜模式" @click="toggleTheme">
            <span class="theme-ico" aria-hidden="true">
              <Icon class="icon-moon" name="weather-night" />
              <Icon class="icon-sun" name="weather-sunny" />
            </span>
          </button>
          <RouterLink to="/" class="ui-btn ui-btn-outline ui-btn-sm ui-topbar-front">
            <Icon name="open-in-new" />
            <span>前台</span>
          </RouterLink>
        </div>
      </header>

      <main class="ui-admin-content">
        <router-view />
      </main>
    </div>
  </div>
</template>
