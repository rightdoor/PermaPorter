// Vitest 配置：jsdom 环境 + @vitejs/plugin-vue（组件测试），
// 与 vite.config.js 的构建配置互相独立。
import { defineConfig } from 'vitest/config';
import vue from '@vitejs/plugin-vue';

export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.js'],
  },
});
