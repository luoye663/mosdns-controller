<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NFormItem, NInput, NInputNumber, NSwitch } from 'naive-ui'
import { api, type GeositeStatus, type Settings } from '@/lib/api'

const settings = ref<Settings>({ cache_enabled: true, cache_ttl: 0, query_retention_days: 1 })
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const message = ref('')
const geosite = ref<GeositeStatus | null>(null)
const geositeSource = ref('')
const uploadInput = ref<HTMLInputElement | null>(null)
const geositeFile = ref<File | null>(null)
function formatTime(value: string) { return value ? new Date(value).toLocaleString() : '-' }
async function load() { loading.value = true; error.value = ''; try { const [nextSettings, nextGeosite] = await Promise.all([api.settings(), api.geositeStatus()]); settings.value = nextSettings; geosite.value = nextGeosite; geositeSource.value = nextGeosite.source_url } catch (e) { error.value = e instanceof Error ? e.message : '无法读取设置' } finally { loading.value = false } }
async function save() { saving.value = true; error.value = ''; message.value = ''; try { settings.value = await api.updateSettings(settings.value); message.value = '设置已保存' } catch (e) { error.value = e instanceof Error ? e.message : '保存设置失败' } finally { saving.value = false } }
async function updateGeosite() { saving.value = true; error.value = ''; message.value = ''; try { geosite.value = await api.updateGeosite(geositeSource.value); geositeSource.value = geosite.value.source_url; message.value = `国内域名集合已更新，版本 ${geosite.value.version}` } catch (e) { error.value = e instanceof Error ? e.message : '更新国内域名集合失败' } finally { saving.value = false } }
function chooseGeositeFile() { uploadInput.value?.click() }
function selectGeositeFile(event: Event) { const input = event.target as HTMLInputElement; geositeFile.value = input.files?.[0] ?? null }
async function uploadGeosite() { if (!geositeFile.value) return; saving.value = true; error.value = ''; message.value = ''; try { geosite.value = await api.uploadGeosite(geositeFile.value); geositeSource.value = ''; geositeFile.value = null; if (uploadInput.value) uploadInput.value.value = ''; message.value = `国内域名集合已上传，版本 ${geosite.value.version}` } catch (e) { error.value = e instanceof Error ? e.message : '上传国内域名集合失败' } finally { saving.value = false } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><p class="eyebrow">系统配置</p><h1>设置</h1></div>
      <NButton :loading="loading" @click="load">刷新</NButton>
    </header>
    <NAlert v-if="message" type="success" class="form-alert">{{ message }}</NAlert>
    <NAlert v-if="error" type="error" class="form-alert">{{ error }}</NAlert>
    <div class="settings-grid"><NCard title="DNS 缓存" size="small" class="settings-card">
      <NFormItem label="启用缓存"><NSwitch v-model:value="settings.cache_enabled" /></NFormItem><NFormItem label="缓存 TTL（秒）"><NInputNumber v-model:value="settings.cache_ttl" :min="0" :max="604800" /></NFormItem><p class="field-hint">0 表示不延长缓存，响应有效期完全遵循上游 DNS 的 TTL。</p>
    </NCard>
    <NCard title="查询日志" size="small" class="settings-card">
      <NFormItem label="保留天数"><NInputNumber v-model:value="settings.query_retention_days" :min="1" :max="365" /></NFormItem>
      <div class="inline-controls settings-save-action"><NButton type="primary" :loading="saving" @click="save">保存设置</NButton></div>
    </NCard>
    <NCard title="国内域名订阅" size="small" class="settings-card">
      <NFormItem label="订阅地址"><NInput v-model:value="geositeSource" placeholder="https://raw.githubusercontent.com/Loyalsoldier/v2ray-rules-dat/release/china-list.txt" /></NFormItem>
      <div class="subscription-actions">
        <div class="inline-controls"><NButton type="primary" :loading="saving" @click="updateGeosite">立即更新</NButton></div>
        <input ref="uploadInput" type="file" accept=".txt,text/plain" hidden @change="selectGeositeFile" />
        <div class="inline-controls"><NButton @click="chooseGeositeFile">选择 TXT</NButton><span class="muted">{{ geositeFile?.name || '未选择文件' }}</span><NButton type="primary" :disabled="!geositeFile" :loading="saving" @click="uploadGeosite">上传并应用</NButton></div>
      </div>
      <dl v-if="geosite" class="status-list"><dt>运行时版本</dt><dd>{{ geosite.version }}</dd><dt>规则数量</dt><dd>{{ geosite.rule_count }}</dd><dt>更新时间</dt><dd>{{ formatTime(geosite.loaded_at) }}</dd></dl>
    </NCard></div>
  </section>
</template>
