<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NFormItem, NInputNumber, NModal, NSelect, NSwitch } from 'naive-ui'
import { api, type Settings, type UpstreamGroup } from '@/lib/api'
import { notify } from '@/lib/notify'

const settings = ref<Settings>({ cache_enabled: true, cache_ttl: 0, negative_cache_enabled: true, negative_cache_ttl: 30, query_retention_days: 7, database_max_size_gib: 2, address_family_mode: 'dual_stack', default_upstream_group_id: 'default_dns', upstream_registry_version: 0 })
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
    <div class="settings-grid"><NCard title="DNS 解析" size="small" class="settings-card"><div class="settings-fields">
      <div class="setting-field"><NFormItem label="默认上游组" :show-feedback="false"><NSelect v-model:value="settings.default_upstream_group_id" :options="groups.filter((group) => group.enabled).map((group) => ({ label: `${group.name} (${group.id})`, value: group.id }))" /></NFormItem><p class="field-hint">查询未命中手工路由或订阅绑定时，使用该上游组完成解析。</p></div>
      <div class="setting-field"><NFormItem label="启用缓存" :show-feedback="false"><NSwitch v-model:value="settings.cache_enabled" /></NFormItem><p class="field-hint">统一控制所有上游组是否复用已取得的 DNS 响应；关闭后每次查询都会访问上游。</p></div>
      <div class="setting-field"><NFormItem label="缓存 TTL（秒）" :show-feedback="false"><NInputNumber v-model:value="settings.cache_ttl" :min="0" :max="604800" /></NFormItem><p class="field-hint">0 表示不保留过期响应，完全遵循上游 TTL；大于 0 时可在刷新期间短暂复用过期响应。</p></div>
      <div class="setting-field"><NFormItem label="否定缓存" :show-feedback="false"><NSwitch v-model:value="settings.negative_cache_enabled" /></NFormItem><p class="field-hint">缓存上游返回的“域名不存在”或“无对应记录”结果；不会缓存超时、SERVFAIL 或黑名单拦截。</p></div>
      <div class="setting-field"><NFormItem label="否定响应缓存上限（秒）" :show-feedback="false"><NInputNumber v-model:value="settings.negative_cache_ttl" :min="1" :max="86400" :disabled="!settings.negative_cache_enabled" /></NFormItem><p class="field-hint">存在 SOA 时取该值与权威 TTL 的较小值；响应没有 SOA 时使用该值。</p></div>
      <div class="setting-field"><NFormItem label="地址族策略" :show-feedback="false"><NSelect v-model:value="settings.address_family_mode" :options="[{ label: '双栈', value: 'dual_stack' }, { label: '优先 IPv4', value: 'prefer_ipv4' }, { label: '优先 IPv6', value: 'prefer_ipv6' }, { label: '仅 IPv4', value: 'ipv4_only' }, { label: '仅 IPv6', value: 'ipv6_only' }]" /></NFormItem><p class="field-hint">优先模式会在首选地址存在时隐藏另一种地址；仅限模式会直接返回被禁用地址族的空响应。</p></div>
      <NAlert v-if="settings.address_family_mode === 'prefer_ipv4' || settings.address_family_mode === 'prefer_ipv6'" type="warning" :show-icon="false">优先模式会为非首选地址查询补发首选地址查询；缓存未命中时，上游查询 QPS 最高可能接近原来的两倍。</NAlert>
    </div></NCard>
    <NCard title="查询日志" size="small" class="settings-card"><div class="settings-fields">
      <div class="setting-field"><NFormItem label="保留天数" :show-feedback="false"><NInputNumber v-model:value="settings.query_retention_days" :min="1" :max="365" /></NFormItem><p class="field-hint">超过该天数的查询明细会滚动删除，相关查询统计也不再包含这些记录。</p></div>
      <div class="setting-field"><NFormItem label="数据库上限（GiB）" :show-feedback="false"><NInputNumber v-model:value="settings.database_max_size_gib" :min="1" :max="128" /></NFormItem><p class="field-hint">主文件与 WAL 合计达到上限的 90% 时优先清理最旧查询；管理员审计、规则和设备资料不会被自动清除。</p></div>
      <div class="inline-controls settings-save-action"><NButton type="primary" :loading="saving" @click="save">保存设置</NButton><NButton type="error" secondary :loading="clearing" @click="showClearConfirmation = true">清空日志</NButton></div>
    </div></NCard></div>
    <NModal v-model:show="showClearConfirmation" preset="card" title="清空查询日志" class="rule-modal"><NAlert type="warning">此操作会删除全部 DNS 查询明细和统计聚合，无法恢复。管理员审计、规则和设备资料不会删除；新的查询仍会继续写入。</NAlert><div class="modal-actions"><NButton :disabled="clearing" @click="showClearConfirmation = false">取消</NButton><NButton type="error" :loading="clearing" @click="clearQueryHistory">确认清空</NButton></div></NModal>
  </section>
</template>
