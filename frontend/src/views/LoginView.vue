<script setup>
// 管理后台登录：密码错误提示剩余次数，5 次失败 IP 封禁 15 分钟。
import { ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { api } from '../lib/api';
import { app } from '../lib/store';
import Icon from '../components/Icon.vue';
import { LOGO_URL } from '../lib/assets';

const route = useRoute();
const router = useRouter();

const password = ref('');
const err = ref('');
const submitting = ref(false);

async function submit() {
  if (!password.value || submitting.value) return;
  submitting.value = true;
  err.value = '';
  try {
    await api.post('/api/auth/login', { password: password.value });
    app.authed = true;
    router.push(String(route.query.redirect || '/admin/dashboard'));
  } catch (e) {
    err.value = e.message;
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="ui ui-auth">
    <div class="ui-card ui-auth-card">
      <RouterLink to="/" class="ui-brand" aria-label="返回首页">
        <img class="ui-brand-mark" :src="LOGO_URL" alt="" />
        <span class="ui-brand-name">PermaPorter</span>
      </RouterLink>

      <h1 class="ui-auth-title">管理后台登录</h1>
      <p class="ui-auth-sub">PermaPorter · 内网四生态镜像</p>

      <div v-if="err" class="ui-alert ui-alert-warn" role="alert">
        <Icon name="close-circle" />
        <span>{{ err }}</span>
      </div>

      <form class="ui-auth-form" @submit.prevent="submit">
        <label class="ui-field">
          <span class="ui-label">管理密码</span>
          <input
            id="password"
            v-model="password"
            class="ui-input"
            type="password"
            placeholder="请输入管理密码"
            required
            autofocus
            autocomplete="current-password"
          >
        </label>
        <button class="ui-btn ui-btn-primary ui-btn-lg ui-btn-block" type="submit" :disabled="submitting">
          {{ submitting ? '登录中…' : '登录' }}
          <Icon name="arrow-right" />
        </button>
      </form>

      <div class="ui-auth-foot">
        <span>仅限授权管理员访问。连续登录失败会触发临时 IP 限制。</span>
        <RouterLink to="/">← 返回首页</RouterLink>
      </div>
    </div>
  </div>
</template>
