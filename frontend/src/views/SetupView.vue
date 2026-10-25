<script setup>
// 首次初始化：仅设置管理密码（四生态共用，存储于单库 platform_settings）。
// 各生态上游在首次启动时已按默认配置初始化，之后可在「系统配置」按生态修改。
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { api } from '../lib/api';
import { app } from '../lib/store';
import Icon from '../components/Icon.vue';
import { LOGO_URL } from '../lib/assets';

const router = useRouter();

const password = ref('');
const confirm = ref('');
const err = ref('');
const submitting = ref(false);

async function submit() {
  if (submitting.value) return;
  submitting.value = true;
  err.value = '';
  try {
    await api.post('/api/auth/setup', {
      password: password.value,
      confirm: confirm.value,
    });
    app.initialized = true;
    router.push({ name: 'login' });
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

      <h1 class="ui-auth-title">首次初始化设置</h1>
      <p class="ui-auth-sub">设置管理密码（Go 模块、npm 包、Python 项目与 Maven 构件四个生态共用）。各生态的默认上游已在启动时配置，之后可在后台「系统配置」中按生态修改。</p>

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
            placeholder="至少 6 位"
            required
            minlength="6"
            autocomplete="new-password"
          >
        </label>
        <label class="ui-field">
          <span class="ui-label">确认密码</span>
          <input
            id="confirm_password"
            v-model="confirm"
            class="ui-input"
            type="password"
            placeholder="再次输入密码"
            required
            autocomplete="new-password"
          >
        </label>
        <button class="ui-btn ui-btn-primary ui-btn-lg ui-btn-block" type="submit" :disabled="submitting">
          {{ submitting ? '初始化中…' : '完成初始化' }}
          <Icon name="arrow-right" />
        </button>
      </form>

      <p class="ui-auth-foot">初始化完成后，此入口将自动关闭。</p>
    </div>
  </div>
</template>
