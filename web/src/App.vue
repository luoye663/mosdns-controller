<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { dateZhCN, NAlert, NButton, NConfigProvider, NLayout, NLayoutContent, NLayoutHeader, zhCN } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { api } from '@/lib/api'
const auth = useAuthStore(); const route = useRoute(); const router = useRouter()
const isPublicAuth = computed(() => route.name === 'login' || route.name === 'setup')
const mosdnsError = ref('')
let statusTimer: ReturnType<typeof setInterval> | undefined
async function logout() { await auth.logout(); await router.push({ name: 'login' }) }
async function refreshMosdnsStatus() {
  if (isPublicAuth.value || !auth.authenticated) return
  try {
    const status = await api.systemStatus()
    mosdnsError.value = status.mosdns?.state === 'ready' ? '' : status.mosdns_error || `mosdns 状态为 ${status.mosdns?.state ?? '未知'}`
  } catch {
    // Controller 自身不可用时由页面请求错误呈现；不能误报为 mosdns 故障。
  }
}
function startStatusPolling() {
  if (statusTimer) return
  void refreshMosdnsStatus()
  statusTimer = setInterval(() => { void refreshMosdnsStatus() }, 15_000)
}
function stopStatusPolling() { if (statusTimer) clearInterval(statusTimer); statusTimer = undefined; mosdnsError.value = '' }
watch(isPublicAuth, (publicRoute) => { if (publicRoute) stopStatusPolling(); else startStatusPolling() })
onMounted(startStatusPolling)
onBeforeUnmount(stopStatusPolling)
</script>

<template>
  <NConfigProvider :locale="zhCN" :date-locale="dateZhCN" :theme-overrides="{ common: { primaryColor: '#087e8b', primaryColorHover: '#066d78', borderRadius: '6px' } }">
    <RouterView v-if="isPublicAuth" />
    <NLayout v-else class="shell" has-sider>
      <aside class="sidebar"><RouterLink class="brand" to="/"><span class="brand-mark">M</span><span>mosdns<br><small>控制面</small></span></RouterLink><nav><RouterLink to="/">概览</RouterLink><RouterLink to="/queries">查询日志</RouterLink><RouterLink to="/rules">规则管理</RouterLink><RouterLink to="/versions">版本发布</RouterLink><RouterLink to="/upstreams">上游 DNS</RouterLink><RouterLink to="/devices">设备</RouterLink><RouterLink to="/settings">设置</RouterLink><RouterLink to="/system">系统状态</RouterLink></nav><div class="sidebar-footer"><span>{{ auth.username }}</span><NButton size="small" quaternary @click="logout">退出登录</NButton></div></aside>
      <NLayout><NLayoutHeader bordered class="topbar"><span>{{ route.meta.title ?? '局域网 DNS 管理' }}</span><span class="topbar-note">数据面独立运行</span></NLayoutHeader><NLayoutContent content-style="padding: 28px; min-height: calc(100vh - 59px)"><NAlert v-if="mosdnsError" type="warning" class="form-alert" :show-icon="true">mosdns 当前不可连接，DNS 将继续使用最近一次成功持久化的快照服务。{{ mosdnsError }}</NAlert><RouterView v-slot="{ Component }"><KeepAlive include="QueriesPage"><component :is="Component" /></KeepAlive></RouterView></NLayoutContent></NLayout>
    </NLayout>
  </NConfigProvider>
</template>
