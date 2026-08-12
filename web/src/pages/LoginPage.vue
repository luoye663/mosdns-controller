<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NCard, NForm, NFormItem, NInput } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import type { ApiError } from '@/lib/api'
import { notify } from '@/lib/notify'

const auth = useAuthStore(); const router = useRouter()
const username = ref(''); const password = ref(''); const loading = ref(false)
async function submit() {
  loading.value = true
  try { await auth.login(username.value, password.value); await router.replace('/') }
  catch (error) {
    const apiError = error as ApiError
    if (apiError.status === 429) notify.error('登录尝试过于频繁，请稍后再试。', '请求已限流')
    else notify.error('用户名或密码错误，或当前登录暂不可用。', '登录失败')
  }
  finally { loading.value = false }
}
</script>
<template><main class="login-page"><NCard class="login-card" title="mosdns 管理平台"><NForm @submit.prevent="submit"><NFormItem label="用户名"><NInput v-model:value="username" autocomplete="username" /></NFormItem><NFormItem label="密码"><NInput v-model:value="password" type="password" show-password-on="click" autocomplete="current-password" /></NFormItem><NButton type="primary" attr-type="submit" :loading="loading" block>登录</NButton></NForm></NCard></main></template>
