<script setup lang="ts">defineProps<{ title: string; endpoint: string }>()</script>
<template><section class="page"><h1>{{ title }}</h1><div class="empty-state"><strong>该功能等待控制面接口完成</strong><span>需要的接口：{{ endpoint }}</span></div></section></template>
