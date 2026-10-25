<script setup>
import Toasts from './components/Toasts.vue';
import BackTop from './components/BackTop.vue';
</script>

<template>
  <router-view />
  <Toasts />
  <BackTop />
</template>
