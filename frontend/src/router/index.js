// 前端路由：公开端与后台各一棵嵌套路由树（布局组件承载 <router-view>）。
// 单 SPA 双路由树（§8）：GO 模块树（goproxy/module）与 NPM 包树（npmregistry/package）并存。
// 认证/初始化状态由服务端外壳注入的 app store 驱动路由守卫。
// 视图按路由懒加载：首屏只加载首页 + 布局，后台页面访问时才拉对应 chunk。
import { createRouter, createWebHistory } from 'vue-router';
import PublicLayout from '../layouts/PublicLayout.vue';
import HomeView from '../views/HomeView.vue';
import { app, loadState } from '../lib/store';

const ModulesView = () => import('../views/ModulesView.vue');
const ModuleDetailView = () => import('../views/ModuleDetailView.vue');
const PackagesView = () => import('../views/PackagesView.vue');
const PackageDetailView = () => import('../views/PackageDetailView.vue');
const PyProjectsView = () => import('../views/PyProjectsView.vue');
const PyProjectDetailView = () => import('../views/PyProjectDetailView.vue');
const MvnArtifactsView = () => import('../views/MvnArtifactsView.vue');
const MvnArtifactDetailView = () => import('../views/MvnArtifactDetailView.vue');
const NotFoundView = () => import('../views/NotFoundView.vue');
const AdminLayout = () => import('../layouts/AdminLayout.vue');
const LoginView = () => import('../views/LoginView.vue');
const SetupView = () => import('../views/SetupView.vue');
const WizardView = () => import('../views/WizardView.vue');
const DashboardView = () => import('../views/DashboardView.vue');
const AdminModulesView = () => import('../views/AdminModulesView.vue');
const AdminPackagesView = () => import('../views/AdminPackagesView.vue');
const AdminPyProjectsView = () => import('../views/AdminPyProjectsView.vue');
const AdminMvnArtifactsView = () => import('../views/AdminMvnArtifactsView.vue');
const PreheatView = () => import('../views/PreheatView.vue');
const LogsView = () => import('../views/LogsView.vue');
const SettingsView = () => import('../views/SettingsView.vue');

const routes = [
  {
    path: '/',
    component: PublicLayout,
    children: [
      { path: '', name: 'home', component: HomeView },
      { path: 'goproxy', name: 'goproxy', component: ModulesView },
      // :path 使用自定义正则以兼容嵌套模块路径（github.com/gin-gonic/gin）
      { path: 'module/:path(.*)', name: 'module', component: ModuleDetailView },
      { path: 'npmregistry', name: 'npmregistry', component: PackagesView },
      // :pkg 使用自定义正则以兼容 @scope/name（路径中含 /）
      { path: 'package/:pkg(.*)', name: 'package', component: PackageDetailView },
      { path: 'pythonsimple', name: 'pythonsimple', component: PyProjectsView },
      { path: 'pyproject/:name(.*)', name: 'pyproject', component: PyProjectDetailView },
      // :coords 为 Maven 坐标 groupId:artifactId（路径段内含冒号）
      { path: 'maven', name: 'maven', component: MvnArtifactsView },
      { path: 'mvnartifact/:coords(.*)', name: 'mvnartifact', component: MvnArtifactDetailView },
      { path: ':pathMatch(.*)*', name: 'notfound', component: NotFoundView },
    ],
  },
  {
    path: '/admin',
    component: AdminLayout,
    children: [
      { path: '', redirect: { name: 'dashboard' } },
      // 登录/初始化为独立页（无侧栏骨架）：meta.bare 由 AdminLayout 识别
      { path: 'login', name: 'login', component: LoginView, meta: { bare: true } },
      { path: 'setup', name: 'setup', component: SetupView, meta: { bare: true } },
      // 初始化向导：数据库未配置（setupMode）时的首次部署入口，与 setup 密码页互斥
      { path: 'wizard', name: 'wizard', component: WizardView, meta: { bare: true } },
      { path: 'dashboard', name: 'dashboard', component: DashboardView, meta: { requiresAuth: true } },
      { path: 'modules', name: 'admin-modules', component: AdminModulesView, meta: { requiresAuth: true } },
      { path: 'packages', name: 'admin-packages', component: AdminPackagesView, meta: { requiresAuth: true } },
      { path: 'pyprojects', name: 'admin-pyprojects', component: AdminPyProjectsView, meta: { requiresAuth: true } },
      { path: 'mvnartifacts', name: 'admin-mvnartifacts', component: AdminMvnArtifactsView, meta: { requiresAuth: true } },
      { path: 'preheat', name: 'preheat', component: PreheatView, meta: { requiresAuth: true } },
      { path: 'logs', name: 'logs', component: LogsView, meta: { requiresAuth: true } },
      { path: 'settings', name: 'settings', component: SettingsView, meta: { requiresAuth: true } },
    ],
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(to, from, saved) {
    if (saved) return saved;
    return { top: 0 };
  },
});

router.beforeEach(async (to) => {
  if (!app.ready) await loadState();
  // 数据库未配置（初始化模式）：向导是唯一有效页面，全部路由（含首页与公开页）导向向导——
  // 初始化前数据端点均不存在，公开页无可展示内容；容器/二进制部署文档均以
  // "打开控制台根路径进入向导"为承诺行为
  if (app.setupMode && to.name !== 'wizard') {
    return { name: 'wizard' };
  }
  // 数据库已配置但未设密码：管理端先完成密码初始化（公开页保持可浏览）。
  // 必须排除 setupMode：初始化模式下 initialized 恒为 false，若不排除，上一条导向
  // wizard 后本条又把 wizard 导回 setup，两守卫互踢形成无限重定向（导航被中止，
  // 表现为点击管理端入口无任何响应）
  if (!app.setupMode && !app.initialized && to.name !== 'setup' && to.matched.some((r) => r.path.startsWith('/admin'))) {
    return { name: 'setup' };
  }
  // 向导仅在初始化模式可达；直接访问时按初始化状态分流
  if (to.name === 'wizard' && !app.setupMode) {
    return { name: app.initialized ? 'dashboard' : 'login' };
  }
  if (to.meta.requiresAuth && !app.authed) {
    return { name: 'login', query: to.fullPath !== '/' ? { redirect: to.fullPath } : {} };
  }
  if (to.name === 'login' && app.authed) return { name: 'dashboard' };
  if (to.name === 'setup' && app.initialized) return { name: 'login' };
  return true;
});

const titles = {
  home: 'PermaPorter · 内网四生态镜像',
  goproxy: 'GO - PermaPorter',
  module: '模块详情 - PermaPorter',
  npmregistry: 'NPM - PermaPorter',
  package: '包详情 - PermaPorter',
  pythonsimple: 'PYTHON - PermaPorter',
  pyproject: 'Py 项目详情 - PermaPorter',
  maven: 'JAVA - PermaPorter',
  mvnartifact: 'Maven 构件详情 - PermaPorter',
  notfound: '404 - PermaPorter',
  login: '登录 - PermaPorter',
  setup: '初始化设置 - PermaPorter',
  wizard: '初始化向导 - PermaPorter',
  dashboard: '仪表盘 - PermaPorter',
  'admin-modules': '模块管理 - PermaPorter',
  'admin-packages': '包管理 - PermaPorter',
  'admin-pyprojects': 'Py 项目管理 - PermaPorter',
  'admin-mvnartifacts': 'Maven 构件管理 - PermaPorter',
  preheat: '依赖预热 - PermaPorter',
  logs: '请求日志 - PermaPorter',
  settings: '系统配置 - PermaPorter',
};

router.afterEach((to) => {
  document.title = titles[to.name] || 'PermaPorter';
});
