// ESLint 扁平配置：Vue 3 单文件组件 + 基础 JS 规则。
// 只保留「能真正抓到 bug」的规则（未定义变量、重复键、v-for key 等），
// 不做风格类检查（风格交给 Prettier）。
import js from '@eslint/js';
import pluginVue from 'eslint-plugin-vue';

export default [
  {
    ignores: ['dist/**', '../internal/webui/assets/**', 'node_modules/**'],
  },
  js.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    // 测试文件：Vitest 运行环境（jsdom + Vitest 注入的全局）
    files: ['src/**/*.test.js'],
    languageOptions: {
      globals: {
        global: 'readonly',
        describe: 'readonly',
        it: 'readonly',
        expect: 'readonly',
        vi: 'readonly',
        beforeEach: 'readonly',
        afterEach: 'readonly',
      },
    },
    rules: {
      // 测试内用 defineComponent 构造宿主组件是常规做法
      'vue/one-component-per-file': 'off',
    },
  },
  {
    files: ['**/*.{js,vue}'],
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'module',
      globals: {
        window: 'readonly',
        document: 'readonly',
        localStorage: 'readonly',
        matchMedia: 'readonly',
        fetch: 'readonly',
        URLSearchParams: 'readonly',
        AbortController: 'readonly',
        setTimeout: 'readonly',
        clearTimeout: 'readonly',
        setInterval: 'readonly',
        clearInterval: 'readonly',
        console: 'readonly',
        navigator: 'readonly',
      },
    },
    rules: {
      // 抓空值访问类 bug（如 dashboard 的 data.hitRate.toFixed）
      'no-undef': 'error',
      'no-dupe-keys': 'error',
      'no-unreachable': 'error',
      'vue/no-unused-vars': 'warn',
      'vue/require-v-for-key': 'error',
      'vue/no-mutating-props': 'error',
      'vue/multi-word-component-names': 'off',
      // v-html 的两处使用是有意为之：Icon 渲染本地 SVG 文件，
      // 详情页 README 由 goldmark 渲染且未开启 Unsafe（原始 HTML 会被转义）
      'vue/no-v-html': 'off',
      // 排版类规则交给 Prettier，避免两套工具打架
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-self-closing': 'off',
      'vue/attributes-order': 'off',
    },
  },
];
