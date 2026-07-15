<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore(); const router = useRouter()
const username = ref(''); const password = ref(''); const confirmPassword = ref(''); const loading = ref(false); const error = ref('')
async function submit() {
  error.value = ''
  if (password.value.length < 8) { error.value = '密码至少需要 8 个字符。'; return }
  if (password.value !== confirmPassword.value) { error.value = '两次输入的密码不一致。'; return }
  loading.value = true
  try { await auth.bootstrap(username.value, password.value); await router.replace('/') }
  catch (cause) {
    const apiError = cause as { code?: string; message?: string }
    error.value = apiError.code === 'INITIAL_SETUP_COMPLETE' ? '管理员已完成初始化，请使用已有账号登录。' : (apiError.message ?? '初始化失败，请稍后重试。')
  } finally { loading.value = false }
}
</script>
<template><main class="login-page"><NCard class="login-card" title="初始化管理员"><p class="setup-note">为 mosdns 管理平台创建首个管理员账号。</p><NAlert v-if="error" type="error" class="form-alert">{{ error }}</NAlert><NForm @submit.prevent="submit"><NFormItem label="用户名"><NInput v-model:value="username" autocomplete="username" /></NFormItem><NFormItem label="密码"><NInput v-model:value="password" type="password" show-password-on="click" autocomplete="new-password" /></NFormItem><NFormItem label="确认密码"><NInput v-model:value="confirmPassword" type="password" show-password-on="click" autocomplete="new-password" /></NFormItem><NButton type="primary" attr-type="submit" :loading="loading" block>创建管理员</NButton></NForm></NCard></main></template>
