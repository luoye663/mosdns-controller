<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore(); const router = useRouter()
const username = ref(''); const password = ref(''); const loading = ref(false); const error = ref('')
async function submit() {
  loading.value = true; error.value = ''
  try { await auth.login(username.value, password.value); await router.replace('/') }
  catch { error.value = '用户名或密码错误，或当前登录暂不可用。' }
  finally { loading.value = false }
}
</script>
<template><main class="login-page"><NCard class="login-card" title="mosdns 管理平台"><NAlert v-if="error" type="error" class="form-alert">{{ error }}</NAlert><NForm @submit.prevent="submit"><NFormItem label="用户名"><NInput v-model:value="username" autocomplete="username" /></NFormItem><NFormItem label="密码"><NInput v-model:value="password" type="password" show-password-on="click" autocomplete="current-password" /></NFormItem><NButton type="primary" attr-type="submit" :loading="loading" block>登录</NButton></NForm></NCard></main></template>
