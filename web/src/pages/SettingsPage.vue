<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NFormItem, NInputNumber, NSwitch } from 'naive-ui'
import { api, type Settings } from '@/lib/api'

const settings = ref<Settings>({ cache_enabled: true, query_retention_days: 1 })
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const message = ref('')
async function load() { loading.value = true; error.value = ''; try { settings.value = await api.settings() } catch (e) { error.value = e instanceof Error ? e.message : '无法读取设置' } finally { loading.value = false } }
async function save() { saving.value = true; error.value = ''; message.value = ''; try { settings.value = await api.updateSettings(settings.value); message.value = '设置已保存' } catch (e) { error.value = e instanceof Error ? e.message : '保存设置失败' } finally { saving.value = false } }
onMounted(load)
</script>

<template><section class="page"><header class="page-heading"><div><p class="eyebrow">系统配置</p><h1>设置</h1></div><NButton :loading="loading" @click="load">刷新</NButton></header><NAlert v-if="message" type="success" class="form-alert">{{ message }}</NAlert><NAlert v-if="error" type="error" class="form-alert">{{ error }}</NAlert><NCard title="DNS 缓存" size="small" class="settings-card"><NFormItem label="启用缓存"><NSwitch v-model:value="settings.cache_enabled" /></NFormItem></NCard><NCard title="查询日志" size="small" class="settings-card"><NFormItem label="保留天数"><NInputNumber v-model:value="settings.query_retention_days" :min="1" :max="365" /></NFormItem></NCard><div class="inline-controls"><NButton type="primary" :loading="saving" @click="save">保存设置</NButton></div></section></template>
