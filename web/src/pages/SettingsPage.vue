<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NFormItem, NInputNumber, NModal, NSelect, NSwitch } from 'naive-ui'
import { api, type Settings, type UpstreamGroup } from '@/lib/api'
import { notify } from '@/lib/notify'

const settings = ref<Settings>({ cache_enabled: true, cache_ttl: 0, negative_cache_enabled: true, negative_cache_ttl: 30, query_retention_days: 7, database_max_size_gib: 2, address_family_mode: 'dual_stack', default_upstream_group_id: 'remote_dns', upstream_registry_version: 0 })
const groups = ref<UpstreamGroup[]>([])
const loading = ref(false)
const saving = ref(false)
const clearing = ref(false)
const showClearConfirmation = ref(false)
async function load() { loading.value = true; try { const [nextSettings, registry] = await Promise.all([api.settings(), api.upstreamGroups()]); settings.value = nextSettings; groups.value = registry.groups } catch (e) { notify.error(e instanceof Error ? e.message : '无法读取设置') } finally { loading.value = false } }
async function save() { saving.value = true; try { settings.value = await api.updateSettings(settings.value); notify.success('设置已保存') } catch (e) { notify.error(e instanceof Error ? e.message : '保存设置失败') } finally { saving.value = false } }
async function clearQueryHistory() { clearing.value = true; try { await api.clearQueryHistory(); showClearConfirmation.value = false; notify.success('查询日志和统计已清空') } catch (e) { notify.error(e instanceof Error ? e.message : '清空查询日志失败') } finally { clearing.value = false } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><p class="eyebrow">系统配置</p><h1>设置</h1></div>
      <NButton :loading="loading" @click="load">刷新</NButton>
    </header>
    <div class="settings-grid"><NCard title="DNS 解析" size="small" class="settings-card">
      <NFormItem label="默认上游组"><NSelect v-model:value="settings.default_upstream_group_id" :options="groups.filter((group) => group.enabled).map((group) => ({ label: `${group.name} (${group.id})`, value: group.id }))" /></NFormItem>
      <NFormItem label="启用缓存"><NSwitch v-model:value="settings.cache_enabled" /></NFormItem><NFormItem label="缓存 TTL（秒）"><NInputNumber v-model:value="settings.cache_ttl" :min="0" :max="604800" /></NFormItem><p class="field-hint">0 表示不延长缓存，响应有效期完全遵循上游 DNS 的 TTL。</p>
      <NFormItem label="否定缓存"><NSwitch v-model:value="settings.negative_cache_enabled" /></NFormItem><NFormItem label="否定缓存 TTL（秒）"><NInputNumber v-model:value="settings.negative_cache_ttl" :min="1" :max="86400" :disabled="!settings.negative_cache_enabled" /></NFormItem>
      <NFormItem label="地址族策略"><NSelect v-model:value="settings.address_family_mode" :options="[{ label: '双栈', value: 'dual_stack' }, { label: '优先 IPv4', value: 'prefer_ipv4' }, { label: '优先 IPv6', value: 'prefer_ipv6' }, { label: '仅 IPv4', value: 'ipv4_only' }, { label: '仅 IPv6', value: 'ipv6_only' }]" /></NFormItem><p class="field-hint">优先模式会在首选地址存在时隐藏另一种地址；仅限模式会直接返回被禁用地址族的空响应。</p>
      <NAlert v-if="settings.address_family_mode === 'prefer_ipv4' || settings.address_family_mode === 'prefer_ipv6'" type="warning" :show-icon="false">优先模式会为非首选地址查询补发首选地址查询；缓存未命中时，上游查询 QPS 最高可能接近原来的两倍。</NAlert>
    </NCard>
    <NCard title="查询日志" size="small" class="settings-card">
      <NFormItem label="保留天数"><NInputNumber v-model:value="settings.query_retention_days" :min="1" :max="365" /></NFormItem>
      <NFormItem label="数据库上限（GiB）"><NInputNumber v-model:value="settings.database_max_size_gib" :min="1" :max="128" /></NFormItem>
      <NAlert type="info" :show-icon="false">查询日志按保留天数滚动删除。数据库主文件与 WAL 合计达到所设上限的 90% 时，也会优先清理最旧的原始查询记录。默认上限为 2 GiB。管理员审计、规则和设备资料不会被自动清除。</NAlert>
      <div class="inline-controls settings-save-action"><NButton type="primary" :loading="saving" @click="save">保存设置</NButton><NButton type="error" secondary :loading="clearing" @click="showClearConfirmation = true">清空日志</NButton></div>
    </NCard></div>
    <NModal v-model:show="showClearConfirmation" preset="card" title="清空查询日志" class="rule-modal"><NAlert type="warning">此操作会删除全部 DNS 查询明细和统计聚合，无法恢复。管理员审计、规则和设备资料不会删除；新的查询仍会继续写入。</NAlert><div class="modal-actions"><NButton :disabled="clearing" @click="showClearConfirmation = false">取消</NButton><NButton type="error" :loading="clearing" @click="clearQueryHistory">确认清空</NButton></div></NModal>
  </section>
</template>
