<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { dateZhCN, NAlert, NButton, NConfigProvider, NDropdown, NFormItem, NInput, NLayout, NLayoutContent, NLayoutHeader, NModal, zhCN } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { api } from '@/lib/api'
import { notify } from '@/lib/notify'
const auth = useAuthStore(); const route = useRoute(); const router = useRouter()
const isPublicAuth = computed(() => route.name === 'login' || route.name === 'setup')
const mosdnsError = ref('')
const showPassword = ref(false)
const password = ref({ current: '', next: '', confirm: '' })
const passwordSaving = ref(false)
let statusTimer: ReturnType<typeof setInterval> | undefined
async function logout() { await auth.logout(); await router.push({ name: 'login' }) }
const accountOptions = [{ label: '修改密码', key: 'password' }, { label: '退出登录', key: 'logout' }]
function accountAction(key: string | number) { if (key === 'logout') void logout(); else { password.value = { current: '', next: '', confirm: '' }; showPassword.value = true } }
async function changePassword() { if (password.value.next !== password.value.confirm) { notify.error('两次输入的新密码不一致', '修改密码失败'); return }; passwordSaving.value = true; try { await api.changePassword(password.value.current, password.value.next); showPassword.value = false; notify.success('密码已修改') } catch (error) { notify.error(error instanceof Error ? error.message : '修改密码失败') } finally { passwordSaving.value = false } }
async function refreshMosdnsStatus() {
  if (isPublicAuth.value || !auth.authenticated) return
  try {
    const status = await api.systemStatus()
    mosdnsError.value = status.mosdns ? '' : status.mosdns_error || 'mosdns 运行时状态未知'
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
      <aside class="sidebar"><RouterLink class="brand" to="/"><span class="brand-mark">M</span><span>mosdns<br><small>控制面板</small></span></RouterLink><nav><RouterLink to="/">概览</RouterLink><RouterLink to="/queries">查询日志</RouterLink><RouterLink to="/rules">规则管理</RouterLink><RouterLink to="/versions">版本发布</RouterLink><RouterLink to="/upstreams">上游 DNS</RouterLink><RouterLink to="/devices">设备列表</RouterLink><RouterLink to="/settings">系统设置</RouterLink><RouterLink to="/system">系统状态</RouterLink></nav></aside>
      <NLayout><NLayoutHeader bordered class="topbar"><span>{{ route.meta.title ?? '局域网 DNS 管理' }}</span><div class="topbar-account"><NDropdown trigger="hover" :options="accountOptions" @select="accountAction"><NButton size="small" quaternary>{{ auth.username }}</NButton></NDropdown></div></NLayoutHeader><NLayoutContent content-style="padding: 28px; min-height: calc(100vh - 59px)"><NAlert v-if="mosdnsError" type="warning" class="form-alert" :show-icon="true">mosdns 当前不可连接，DNS 将继续使用最近一次成功持久化的快照服务。{{ mosdnsError }}</NAlert><RouterView v-slot="{ Component }"><KeepAlive include="QueriesPage"><component :is="Component" /></KeepAlive></RouterView></NLayoutContent></NLayout>
    </NLayout>
    <NModal v-model:show="showPassword" preset="card" title="修改密码" class="rule-modal"><div class="form-grid password-form"><NFormItem label="当前密码"><NInput v-model:value="password.current" type="password" show-password-on="click" /></NFormItem><NFormItem label="新密码"><NInput v-model:value="password.next" type="password" show-password-on="click" /></NFormItem><NFormItem label="确认新密码"><NInput v-model:value="password.confirm" type="password" show-password-on="click" @keyup.enter="changePassword" /></NFormItem></div><div class="modal-actions"><NButton @click="showPassword = false">取消</NButton><NButton type="primary" :loading="passwordSaving" @click="changePassword">保存</NButton></div></NModal>
  </NConfigProvider>
</template>
