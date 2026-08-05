<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Plus, RefreshCw, Save, Trash2 } from '@lucide/vue'
import { NAlert, NButton, NCard, NFormItem, NInput, NInputNumber, NModal, NPopconfirm, NSelect, NSwitch, NTooltip } from 'naive-ui'
import { api, type UpstreamGroup, type UpstreamRegistry } from '@/lib/api'
import { notify } from '@/lib/notify'

const registry = ref<UpstreamRegistry | null>(null)
const loading = ref(false)
const saving = ref('')
const flushing = ref('')
const showCreate = ref(false)
const draft = ref(newGroup())
const baselines = ref<Record<string, string>>({})

const modeOptions = [{ label: '竞速', value: 'race' }, { label: '加权选择', value: 'weighted' }, { label: '主备优先级', value: 'failover' }]
const ecsOptions = [{ label: '关闭', value: 'off' }, { label: '客户端网段', value: 'client_subnet' }, { label: '固定网段', value: 'fixed_subnet' }]
const bootstrapVersionOptions = [{ label: 'IPv4（A）', value: 4 }, { label: 'IPv6（AAAA）', value: 6 }]

function newGroup(): UpstreamGroup {
  return { id: '', name: '', enabled: true, mode: 'race', concurrent: 1, max_in_flight: null, query_timeout_ms: null, socks5: '', bootstrap: '', bootstrap_version: 4, ecs: { mode: 'off', mask4: 24, mask6: 48 }, cache: { enabled: true, size: 1024 }, upstreams: [{ tag: 'primary', addr: '', priority: 100, weight: 1, timeout_ms: 1000 }] }
}
function cloneGroup(group: UpstreamGroup): UpstreamGroup { return { ...group, ecs: { ...group.ecs }, cache: { ...group.cache }, upstreams: group.upstreams.map((item) => ({ ...item })) } }
function serialized(group: UpstreamGroup) { return JSON.stringify(group) }
function applySnapshot(value: UpstreamRegistry) {
  registry.value = { ...value, groups: value.groups.map(cloneGroup) }
  baselines.value = Object.fromEntries(value.groups.map((group) => [group.id, serialized(group)]))
}
function isDirty(group: UpstreamGroup) { return baselines.value[group.id] !== serialized(group) }
function hasDirtyGroup(exceptID = '') { return registry.value?.groups.some((group) => group.id !== exceptID && isDirty(group)) ?? false }
function mergeSavedGroup(value: UpstreamRegistry, id: string) {
  if (!registry.value) { applySnapshot(value); return }
  const normalized = value.groups.find((group) => group.id === id)
  if (!normalized) { applySnapshot(value); return }
  const index = registry.value.groups.findIndex((group) => group.id === id)
  if (index < 0) { applySnapshot(value); return }
  registry.value.groups[index] = cloneGroup(normalized)
  registry.value.version = value.version
  registry.value.expected_current_version = value.expected_current_version
  registry.value.default_group_id = value.default_group_id
  registry.value.cache = value.cache
  registry.value.protection = value.protection
  baselines.value[id] = serialized(normalized)
}
async function load() { loading.value = true; try { applySnapshot(await api.upstreamGroups()) } catch (e) { notify.error(e instanceof Error ? e.message : '无法读取上游组') } finally { loading.value = false } }
function addNode(group: UpstreamGroup) { if (group.upstreams.length < 16) group.upstreams.push({ tag: `upstream_${group.upstreams.length + 1}`, addr: '', priority: 100, weight: 1, timeout_ms: 1000 }) }
function removeNode(group: UpstreamGroup, index: number) { if (group.upstreams.length > 1) group.upstreams.splice(index, 1) }
function setMaxInFlightOverride(group: UpstreamGroup, enabled: boolean) { group.max_in_flight = enabled ? (registry.value?.protection.default_group_max_in_flight ?? 1) : null }
function setQueryTimeoutOverride(group: UpstreamGroup, enabled: boolean) { group.query_timeout_ms = enabled ? (registry.value?.protection.default_group_query_timeout_ms ?? 1000) : null }
function setQueryTimeoutSeconds(group: UpstreamGroup, value: number | null) { if (value !== null) group.query_timeout_ms = Math.max(1, Math.min(30, Math.round(value))) * 1000 }
function modeHint(mode: UpstreamGroup['mode']) {
  if (mode === 'weighted') return '按权重排列节点，并按并发数分批查询；优先级不参与该模式。'
  if (mode === 'failover') return '优先查询优先级数值较小的节点，失败、SERVFAIL 或 REFUSED 后尝试下一批；权重不参与该模式。'
  return '同时查询全部节点并采用最先成功的响应；优先级、权重和并发查询数不参与该模式。'
}
function ecsHint(mode: UpstreamGroup['ecs']['mode']) {
  if (mode === 'client_subnet') return '查询未携带 ECS 时，按配置掩码发送客户端网段；已有 ECS 会保留原值。不同网段使用不同缓存条目。'
  if (mode === 'fixed_subnet') return '查询未携带 ECS 时发送指定网段；已有 ECS 会保留原值。不同网段使用不同缓存条目。'
  return '不主动添加 ECS；查询本身已有 ECS 时会保留原值。'
}
async function save(group: UpstreamGroup) { if (!registry.value) return; saving.value = group.id; try { const value = await api.updateUpstreamGroup({ expected_current_version: registry.value.version, group: cloneGroup(group) }); mergeSavedGroup(value, group.id); notify.success('上游组已热加载') } catch (e) { notify.error(e instanceof Error ? e.message : '保存上游组失败') } finally { saving.value = '' } }
async function create() { if (!registry.value) return; if (hasDirtyGroup()) { notify.warning('请先保存或刷新现有上游组的修改'); return } saving.value = 'create'; try { applySnapshot(await api.createUpstreamGroup({ expected_current_version: registry.value.version, group: cloneGroup(draft.value) })); showCreate.value = false; draft.value = newGroup(); notify.success('上游组已创建') } catch (e) { notify.error(e instanceof Error ? e.message : '创建上游组失败') } finally { saving.value = '' } }
async function removeGroup(id: string) { if (!registry.value) return; if (hasDirtyGroup(id)) { notify.warning('请先保存或刷新其他上游组的修改'); return } saving.value = id; try { applySnapshot(await api.deleteUpstreamGroup(id, { expected_current_version: registry.value.version })); notify.success('上游组已删除') } catch (e) { notify.error(e instanceof Error ? e.message : '删除上游组失败') } finally { saving.value = '' } }
async function flush(id: string) { if (!registry.value) return; flushing.value = id; try { await api.flushUpstreamGroup(id, { expected_current_version: registry.value.version }); notify.success('该组缓存已清空') } catch (e) { notify.error(e instanceof Error ? e.message : '清空缓存失败') } finally { flushing.value = '' } }
function openCreate() { if (hasDirtyGroup()) { notify.warning('请先保存或刷新现有上游组的修改'); return } draft.value = newGroup(); showCreate.value = true }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><p class="eyebrow">解析路径</p><h1>上游 DNS</h1></div>
      <div class="inline-controls">
        <NButton secondary :loading="loading" @click="load"><template #icon><RefreshCw /></template>刷新</NButton>
        <NButton type="primary" :disabled="(registry?.groups.length ?? 0) >= 32" @click="openCreate"><template #icon><Plus /></template>新增组</NButton>
      </div>
    </header>
    <NAlert type="info" class="form-alert">默认组在设置页选择。已被规则、订阅或设置引用的上游组无法停用或删除时，后端会返回具体原因。</NAlert>
    <div class="upstream-group-list">
      <div v-for="group in registry?.groups ?? []" :key="group.id" class="upstream-group-item">
      <NCard size="small" class="upstream-group-card">
        <template #header><div class="group-card-heading"><div><strong>{{ group.name }}</strong><span class="mono">{{ group.id }}</span></div><span v-if="registry?.default_group_id === group.id" class="default-group-label">默认</span></div></template>
        <template #header-extra><div class="inline-controls"><NButton size="small" secondary :loading="flushing === group.id" @click="flush(group.id)">清空缓存</NButton><NPopconfirm v-if="group.id !== registry?.default_group_id" @positive-click="removeGroup(group.id)"><template #trigger><NTooltip><template #trigger><NButton size="small" type="error" quaternary :loading="saving === group.id" aria-label="删除上游组"><template #icon><Trash2 /></template></NButton></template>删除上游组</NTooltip></template>确定删除该上游组？</NPopconfirm></div></template>
        <div class="group-settings-grid">
          <NFormItem label="名称"><NInput v-model:value="group.name" maxlength="128" /></NFormItem>
          <NFormItem label="启用"><NSwitch v-model:value="group.enabled" :disabled="group.id === registry?.default_group_id" /></NFormItem>
          <div class="setting-field"><NFormItem label="调度模式" :show-feedback="false"><NSelect v-model:value="group.mode" :options="modeOptions" /></NFormItem><p class="field-hint">{{ modeHint(group.mode) }}</p></div>
          <NFormItem :label="group.mode === 'race' ? '参与节点' : '并发查询'"><NInput v-if="group.mode === 'race'" :value="`全部节点（${group.upstreams.length}）`" disabled /><NInputNumber v-else v-model:value="group.concurrent" :min="1" :max="16" /></NFormItem>
          <NFormItem label="组并发上限"><div class="protection-override"><NSwitch :value="group.max_in_flight != null" @update:value="(enabled) => setMaxInFlightOverride(group, enabled)" /><span>{{ group.max_in_flight == null ? `继承（${registry?.protection.default_group_max_in_flight}）` : '自定义' }}</span><NInputNumber v-if="group.max_in_flight != null" v-model:value="group.max_in_flight" :min="1" :max="65535" /></div></NFormItem>
          <NFormItem label="组总超时"><div class="protection-override"><NSwitch :value="group.query_timeout_ms != null" @update:value="(enabled) => setQueryTimeoutOverride(group, enabled)" /><span>{{ group.query_timeout_ms == null ? `继承（${Math.round((registry?.protection.default_group_query_timeout_ms ?? 1000) / 1000)} 秒）` : '自定义' }}</span><NInputNumber v-if="group.query_timeout_ms != null" :value="Math.round(group.query_timeout_ms / 1000)" :min="1" :max="30" :precision="0" @update:value="(value) => setQueryTimeoutSeconds(group, value)" /></div></NFormItem>
          <NFormItem label="SOCKS5 代理"><NInput v-model:value="group.socks5" placeholder="host:port" /></NFormItem>
          <div class="setting-field"><NFormItem label="引导 DNS" :show-feedback="false"><NInput v-model:value="group.bootstrap" placeholder="223.5.5.5 或 [2400:3200::1]:53" /></NFormItem><p class="field-hint">专门解析本组域名型上游节点。必须填写 IP，可带端口；留空使用系统 DNS，配置后失败不会回退。</p></div>
          <div class="setting-field"><NFormItem label="解析地址族" :show-feedback="false"><NSelect v-model:value="group.bootstrap_version" :options="bootstrapVersionOptions" :disabled="!group.bootstrap?.trim()" /></NFormItem><p class="field-hint">决定解析上游节点域名时查询 A 记录还是 AAAA 记录。</p></div>
          <div class="setting-field"><NFormItem label="组缓存" :show-feedback="false"><NSwitch v-model:value="group.cache.enabled" /></NFormItem><p class="field-hint">独立缓存本组 DNS 响应；还需在设置中开启全局缓存，命中后不会访问上游。</p></div>
          <div class="setting-field"><NFormItem label="缓存容量" :show-feedback="false"><NInputNumber v-model:value="group.cache.size" :min="1" :max="65536" /></NFormItem><p class="field-hint">容量按缓存条目计数，所有组的容量总和不能超过 65536。</p></div>
          <div class="setting-field"><NFormItem label="ECS" :show-feedback="false"><NSelect v-model:value="group.ecs.mode" :options="ecsOptions" /></NFormItem><p class="field-hint">{{ ecsHint(group.ecs.mode) }}</p></div>
          <NFormItem v-if="group.ecs.mode === 'client_subnet'" label="IPv4 / IPv6 掩码"><div class="mask-inputs"><NInputNumber v-model:value="group.ecs.mask4" :min="1" :max="32" /><NInputNumber v-model:value="group.ecs.mask6" :min="1" :max="128" /></div></NFormItem>
          <NFormItem v-if="group.ecs.mode === 'fixed_subnet'" label="固定 IPv4 网段"><NInput v-model:value="group.ecs.preset4" placeholder="203.0.113.0/24" /></NFormItem>
          <NFormItem v-if="group.ecs.mode === 'fixed_subnet'" label="固定 IPv6 网段"><NInput v-model:value="group.ecs.preset6" placeholder="2001:db8::/48" /></NFormItem>
        </div>
        <div class="group-node-heading"><h2>上游节点</h2><NButton size="small" secondary :disabled="group.upstreams.length >= 16" @click="addNode(group)"><template #icon><Plus /></template>添加节点</NButton></div>
        <div class="table-wrap upstream-table-wrap"><table><thead><tr><th>标签</th><th>DNS 地址</th><th>优先级</th><th>权重</th><th>超时（ms）</th><th></th></tr></thead><tbody><tr v-for="(node, index) in group.upstreams" :key="index"><td><NInput v-model:value="node.tag" /></td><td><NInput v-model:value="node.addr" placeholder="8.8.8.8 或 https://.../dns-query" /></td><td><NInputNumber v-model:value="node.priority" :min="1" :max="1000" /></td><td><NInputNumber v-model:value="node.weight" :min="1" :max="100" /></td><td><NInputNumber v-model:value="node.timeout_ms" :min="100" :max="4000" :step="100" /></td><td><NTooltip><template #trigger><NButton type="error" quaternary :disabled="group.upstreams.length === 1" aria-label="移除节点" @click="removeNode(group, index)"><template #icon><Trash2 /></template></NButton></template>移除节点</NTooltip></td></tr></tbody></table></div>
        <div class="group-save-action"><NButton type="primary" :disabled="!isDirty(group)" :loading="saving === group.id" @click="save(group)"><template #icon><Save /></template>保存并热加载</NButton></div>
      </NCard>
      </div>
      <p v-if="!loading && !registry?.groups.length" class="muted">暂无上游组</p>
    </div>
    <NModal v-model:show="showCreate" preset="card" title="新增上游组" class="upstream-create-modal">
      <div class="group-settings-grid">
        <NFormItem label="稳定 ID"><NInput v-model:value="draft.id" placeholder="例如 office_dns" /></NFormItem>
        <NFormItem label="名称"><NInput v-model:value="draft.name" /></NFormItem>
        <div class="setting-field"><NFormItem label="调度模式" :show-feedback="false"><NSelect v-model:value="draft.mode" :options="modeOptions" /></NFormItem><p class="field-hint">{{ modeHint(draft.mode) }}</p></div>
        <NFormItem :label="draft.mode === 'race' ? '参与节点' : '并发查询'"><NInput v-if="draft.mode === 'race'" :value="`全部节点（${draft.upstreams.length}）`" disabled /><NInputNumber v-else v-model:value="draft.concurrent" :min="1" :max="16" /></NFormItem>
        <NFormItem label="组并发上限"><div class="protection-override"><NSwitch :value="draft.max_in_flight != null" @update:value="(enabled) => setMaxInFlightOverride(draft, enabled)" /><span>{{ draft.max_in_flight == null ? `继承（${registry?.protection.default_group_max_in_flight}）` : '自定义' }}</span><NInputNumber v-if="draft.max_in_flight != null" v-model:value="draft.max_in_flight" :min="1" :max="65535" /></div></NFormItem>
        <NFormItem label="组总超时"><div class="protection-override"><NSwitch :value="draft.query_timeout_ms != null" @update:value="(enabled) => setQueryTimeoutOverride(draft, enabled)" /><span>{{ draft.query_timeout_ms == null ? `继承（${Math.round((registry?.protection.default_group_query_timeout_ms ?? 1000) / 1000)} 秒）` : '自定义' }}</span><NInputNumber v-if="draft.query_timeout_ms != null" :value="Math.round(draft.query_timeout_ms / 1000)" :min="1" :max="30" :precision="0" @update:value="(value) => setQueryTimeoutSeconds(draft, value)" /></div></NFormItem>
        <NFormItem label="SOCKS5 代理"><NInput v-model:value="draft.socks5" placeholder="host:port" /></NFormItem>
        <div class="setting-field"><NFormItem label="引导 DNS" :show-feedback="false"><NInput v-model:value="draft.bootstrap" placeholder="223.5.5.5 或 [2400:3200::1]:53" /></NFormItem><p class="field-hint">专门解析本组域名型上游节点。必须填写 IP，可带端口；留空使用系统 DNS，配置后失败不会回退。</p></div>
        <div class="setting-field"><NFormItem label="解析地址族" :show-feedback="false"><NSelect v-model:value="draft.bootstrap_version" :options="bootstrapVersionOptions" :disabled="!draft.bootstrap?.trim()" /></NFormItem><p class="field-hint">决定解析上游节点域名时查询 A 记录还是 AAAA 记录。</p></div>
        <div class="setting-field"><NFormItem label="组缓存" :show-feedback="false"><NSwitch v-model:value="draft.cache.enabled" /></NFormItem><p class="field-hint">独立缓存本组 DNS 响应；还需在设置中开启全局缓存，命中后不会访问上游。</p></div>
        <div class="setting-field"><NFormItem label="缓存容量" :show-feedback="false"><NInputNumber v-model:value="draft.cache.size" :min="1" :max="65536" /></NFormItem><p class="field-hint">容量按缓存条目计数，所有组的容量总和不能超过 65536。</p></div>
        <div class="setting-field"><NFormItem label="ECS" :show-feedback="false"><NSelect v-model:value="draft.ecs.mode" :options="ecsOptions" /></NFormItem><p class="field-hint">{{ ecsHint(draft.ecs.mode) }}</p></div>
        <NFormItem v-if="draft.ecs.mode === 'client_subnet'" label="IPv4 / IPv6 掩码"><div class="mask-inputs"><NInputNumber v-model:value="draft.ecs.mask4" :min="1" :max="32" /><NInputNumber v-model:value="draft.ecs.mask6" :min="1" :max="128" /></div></NFormItem>
        <NFormItem v-if="draft.ecs.mode === 'fixed_subnet'" label="固定 IPv4 网段"><NInput v-model:value="draft.ecs.preset4" placeholder="203.0.113.0/24" /></NFormItem>
        <NFormItem v-if="draft.ecs.mode === 'fixed_subnet'" label="固定 IPv6 网段"><NInput v-model:value="draft.ecs.preset6" placeholder="2001:db8::/48" /></NFormItem>
      </div>
      <div class="group-node-heading"><h2>初始节点</h2><NButton size="small" secondary :disabled="draft.upstreams.length >= 16" @click="addNode(draft)"><template #icon><Plus /></template>添加节点</NButton></div>
      <div class="table-wrap upstream-table-wrap"><table><thead><tr><th>标签</th><th>DNS 地址</th><th>优先级</th><th>权重</th><th>超时（ms）</th><th></th></tr></thead><tbody><tr v-for="(node, index) in draft.upstreams" :key="index"><td><NInput v-model:value="node.tag" /></td><td><NInput v-model:value="node.addr" placeholder="8.8.8.8 或 https://.../dns-query" /></td><td><NInputNumber v-model:value="node.priority" :min="1" :max="1000" /></td><td><NInputNumber v-model:value="node.weight" :min="1" :max="100" /></td><td><NInputNumber v-model:value="node.timeout_ms" :min="100" :max="4000" :step="100" /></td><td><NTooltip><template #trigger><NButton type="error" quaternary :disabled="draft.upstreams.length === 1" aria-label="移除节点" @click="removeNode(draft, index)"><template #icon><Trash2 /></template></NButton></template>移除节点</NTooltip></td></tr></tbody></table></div>
      <div class="modal-actions"><NButton @click="showCreate = false">取消</NButton><NButton type="primary" :loading="saving === 'create'" @click="create">创建并热加载</NButton></div>
    </NModal>
  </section>
</template>
