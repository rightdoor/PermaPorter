import { createApp } from 'vue';
import App from './App.vue';
import { router } from './router';
import { setUnauthorizedHandler } from './lib/api';
import { app } from './lib/store';
import { notifyError } from './lib/toast';
import './css/main.css';
import './css/ui.css';

// 会话过期：任意接口 401 时置为未登录并回到登录页
setUnauthorizedHandler(() => {
  app.authed = false;
  router.push({ name: 'login' });
});

const vueApp = createApp(App);

// 统一错误出口：组件内未捕获的异常至少给出提示，不再静默白屏
vueApp.config.errorHandler = (err, instance, info) => {
  console.error('[vue]', info, err);
  notifyError(err?.message ? `页面异常：${err.message}` : '页面出现异常，请刷新重试');
};

vueApp.use(router).mount('#app');
