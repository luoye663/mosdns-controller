<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { NButton, NConfigProvider, NLayout, NLayoutContent, NLayoutHeader } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
const auth = useAuthStore(); const route = useRoute(); const router = useRouter()
const isLogin = computed(() => route.name === 'login')
async function logout() { await auth.logout(); await router.push({ name: 'login' }) }
</script>

<template>
  <NConfigProvider :theme-overrides="{ common: { primaryColor: '#087e8b', primaryColorHover: '#066d78', borderRadius: '6px' } }">
    <RouterView v-if="isLogin" />
    <NLayout v-else class="shell" has-sider>
      <aside class="sidebar"><RouterLink class="brand" to="/"><span class="brand-mark">M</span><span>mosdns<br><small>控制面</small></span></RouterLink><nav><RouterLink to="/">概览</RouterLink><RouterLink to="/queries">查询日志</RouterLink><RouterLink to="/rules">规则管理</RouterLink><RouterLink to="/versions">版本发布</RouterLink><RouterLink to="/devices">设备</RouterLink><RouterLink to="/system">系统状态</RouterLink></nav><div class="sidebar-footer"><span>{{ auth.username }}</span><NButton size="small" quaternary @click="logout">退出登录</NButton></div></aside>
      <NLayout><NLayoutHeader bordered class="topbar"><span>{{ route.meta.title ?? '局域网 DNS 管理' }}</span><span class="topbar-note">数据面独立运行</span></NLayoutHeader><NLayoutContent content-style="padding: 28px; min-height: calc(100vh - 59px)"><RouterView /></NLayoutContent></NLayout>
    </NLayout>
  </NConfigProvider>
</template>
