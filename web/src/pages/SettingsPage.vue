<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NCard, NFormItem, NInput, NInputNumber, NSwitch } from 'naive-ui'
import { api, type Settings } from '@/lib/api'
import { notify } from '@/lib/notify'

const settings = ref<Settings>({ cache_enabled: true, cache_ttl: 0, query_retention_days: 1 })
const loading = ref(false)
const saving = ref(false)
async function load() { loading.value = true; try { settings.value = await api.settings() } catch (e) { notify.error(e instanceof Error ? e.message : '无法读取设置') } finally { loading.value = false } }
async function save() { saving.value = true; try { settings.value = await api.updateSettings(settings.value); notify.success('设置已保存') } catch (e) { notify.error(e instanceof Error ? e.message : '保存设置失败') } finally { saving.value = false } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><p class="eyebrow">系统配置</p><h1>设置</h1></div>
      <NButton :loading="loading" @click="load">刷新</NButton>
    </header>
    <div class="settings-grid"><NCard title="DNS 缓存" size="small" class="settings-card">
      <NFormItem label="启用缓存"><NSwitch v-model:value="settings.cache_enabled" /></NFormItem><NFormItem label="缓存 TTL（秒）"><NInputNumber v-model:value="settings.cache_ttl" :min="0" :max="604800" /></NFormItem><p class="field-hint">0 表示不延长缓存，响应有效期完全遵循上游 DNS 的 TTL。</p>
    </NCard>
    <NCard title="查询日志" size="small" class="settings-card">
      <NFormItem label="保留天数"><NInputNumber v-model:value="settings.query_retention_days" :min="1" :max="365" /></NFormItem>
      <div class="inline-controls settings-save-action"><NButton type="primary" :loading="saving" @click="save">保存设置</NButton></div>
    </NCard></div>
  </section>
</template>
