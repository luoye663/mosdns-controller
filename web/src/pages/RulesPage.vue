<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch } from 'naive-ui'
import { Download, Plus, Upload } from '@lucide/vue'
import { api, type ApiError, type Rule, type RuleInput, type RuleSubscription, type RuleSubscriptionInput, type UpstreamGroup } from '@/lib/api'
import { notify } from '@/lib/notify'
import { confirmAction } from '@/lib/dialog'

const rules = ref<Rule[]>([])
const subscriptions = ref<RuleSubscription[]>([])
const groups = ref<UpstreamGroup[]>([])
const registryVersion = ref(0)
const defaultGroupID = ref('')
const active = ref('block')
const ruleView = ref<'manual' | 'subscription'>('manual')
const loading = ref(false)
const showEditor = ref(false)
const showSourceEditor = ref(false)
const showBindingEditor = ref(false)
const editing = ref<Rule | null>(null)
const bindingTarget = ref<RuleSubscription | null>(null)
const uploadInput = ref<HTMLInputElement | null>(null)
const ruleImportInput = ref<HTMLInputElement | null>(null)
const uploadFiles = ref<File[]>([])
const draft = reactive({ category: 'access', action: 'block', upstream_group_id: '', match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true, ipv4_text: '', ipv6_text: '', ttl: 300 })
const sourceDraft = reactive({ url: '', refresh_minutes: 1440, upstream_group_id: '', priority: 100 })
const bindingDraft = reactive({ upstream_group_id: '', priority: 100 })
const tabs = [
  { key: 'allow', label: '白名单', category: 'access', action: 'allow', description: '作为黑名单的放行例外，适合处理订阅误拦截；未命中黑名单时不会改变正常解析结果。' },
  { key: 'block', label: '黑名单', category: 'access', action: 'block', description: '拦截匹配域名并返回 NXDOMAIN。规则在缓存前生效，新增拦截无需等待已有缓存过期。' },
  { key: 'route', label: '路由规则', category: 'route', action: 'upstream', description: '将匹配域名交给指定上游组。手工路由优先于订阅源绑定，优先级数值越小越优先。' },
	{ key: 'answer', label: '自定义解析', category: 'answer', action: 'static', description: '直接返回自定义 IPv4/IPv6 地址；黑名单仍优先，未配置的地址族返回空应答。' },
  { key: 'no_log', label: '不记录', category: 'logging', action: 'no_log', description: '匹配域名仍会正常解析，但不会写入查询日志，也不会进入基于查询日志生成的统计。' },
]
const selected = computed(() => tabs.find((tab) => tab.key === active.value) ?? tabs[1]!)
const visibleRules = computed(() => rules.value.filter((rule) => rule.category === selected.value.category && (!selected.value.action || rule.action === selected.value.action)))
const visibleSubscriptions = computed(() => subscriptions.value.filter((source) => source.category === selected.value.category && (!selected.value.action || source.action === selected.value.action)))
const supportsSubscriptions = computed(() => selected.value.category === 'access' || selected.value.category === 'route')
const draftPatterns = computed(() => draft.pattern.split(/\r?\n/).map((pattern) => pattern.trim()).filter(Boolean))
const enabledGroupOptions = computed(() => groups.value.filter((group) => group.enabled).map((group) => ({ label: `${group.name} (${group.id})`, value: group.id })))

function matchesTab(category: string, action: string, rule: { category: string; action: string }) { return rule.category === category && (!action || rule.action === action) }
function tabRuleCount(category: string, action: string) { return rules.value.filter((rule) => matchesTab(category, action, rule)).length + subscriptions.value.filter((source) => matchesTab(category, action, source)).reduce((sum, source) => sum + source.rule_count, 0) }
function formatTime(value: number) { return value ? new Date(value).toLocaleString() : '-' }
function groupName(id: string) { const group = groups.value.find((item) => item.id === id); return group ? `${group.name} (${group.id})` : id }
function selectTab(key: string) { active.value = key; ruleView.value = 'manual' }
function preferredGroupID() { return groups.value.some((group) => group.enabled && group.id === defaultGroupID.value) ? defaultGroupID.value : enabledGroupOptions.value[0]?.value ?? '' }
function resetDraft() { Object.assign(draft, { category: selected.value.category, action: selected.value.category === 'route' ? 'upstream' : selected.value.action, upstream_group_id: selected.value.category === 'route' ? preferredGroupID() : '', match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true, ipv4_text: '', ipv6_text: '', ttl: 300 }); editing.value = null }
function openCreate() { resetDraft(); showEditor.value = true }
function openEdit(rule: Rule) { resetDraft(); editing.value = rule; Object.assign(draft, rule, { ipv4_text: (rule.ipv4_addresses ?? []).join('\n'), ipv6_text: (rule.ipv6_addresses ?? []).join('\n'), ttl: rule.ttl ?? 300 }); showEditor.value = true }
function addresses(value: string) { return value.split(/\s+/).map((item) => item.trim()).filter(Boolean) }
function openSources() { Object.assign(sourceDraft, { url: '', refresh_minutes: 1440, upstream_group_id: preferredGroupID(), priority: 100 }); uploadFiles.value = []; showSourceEditor.value = true }
function openBinding(source: RuleSubscription) { bindingTarget.value = source; Object.assign(bindingDraft, { upstream_group_id: source.binding?.upstream_group_id ?? preferredGroupID(), priority: source.binding?.priority ?? 100 }); showBindingEditor.value = true }
function chooseFiles() { uploadInput.value?.click() }
function selectFiles(event: Event) { uploadFiles.value = Array.from((event.target as HTMLInputElement).files ?? []) }

function exportRules() {
  const items: RuleInput[] = visibleRules.value.map((rule) => ({
    category: rule.category, action: rule.action, upstream_group_id: rule.upstream_group_id,
    match_type: rule.match_type, pattern: rule.pattern, priority: rule.priority,
    source: rule.source, comment: rule.comment, enabled: rule.enabled,
    ...(rule.category === 'answer' ? { ipv4_addresses: rule.ipv4_addresses ?? [], ipv6_addresses: rule.ipv6_addresses ?? [], ttl: rule.ttl ?? 300 } : {}),
  }))
  const payload = { format: 'managed-dns-rules', version: 1, category: selected.value.category, action: selected.value.action, exported_at: new Date().toISOString(), rules: items }
  const blob = new Blob([`${JSON.stringify(payload, null, 2)}\n`], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url; link.download = `managed-dns-${selected.value.key}-${new Date().toISOString().slice(0, 10)}.json`; link.click()
  URL.revokeObjectURL(url)
  notify.success(`已导出 ${items.length} 条${selected.value.label}手工规则`)
}
function chooseRuleImport() { ruleImportInput.value?.click() }
function importRuleInput(value: unknown): RuleInput {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('规则条目必须是 JSON 对象')
  const item = value as Partial<Rule>
  if (item.category !== selected.value.category || item.action !== selected.value.action) throw new Error(`文件包含不属于“${selected.value.label}”的规则`)
  if (typeof item.pattern !== 'string' || !item.pattern.trim()) throw new Error('规则缺少有效域名')
  if (!['domain', 'full', 'regexp'].includes(item.match_type ?? '')) throw new Error(`规则 ${item.pattern} 的匹配类型无效`)
  if (typeof item.priority !== 'number' || !Number.isInteger(item.priority) || item.priority < 0 || item.priority > 1000) throw new Error(`规则 ${item.pattern} 的优先级无效`)
  if (selected.value.category === 'route' && (typeof item.upstream_group_id !== 'string' || !item.upstream_group_id)) throw new Error(`路由规则 ${item.pattern} 缺少上游组`)
  return {
    category: selected.value.category, action: selected.value.action, upstream_group_id: item.upstream_group_id,
    match_type: item.match_type!, pattern: item.pattern, priority: item.priority,
    source: typeof item.source === 'string' && item.source ? item.source : 'import', comment: typeof item.comment === 'string' ? item.comment : '', enabled: item.enabled !== false,
    ...(selected.value.category === 'answer' ? { ipv4_addresses: item.ipv4_addresses ?? [], ipv6_addresses: item.ipv6_addresses ?? [], ttl: item.ttl ?? 300 } : {}),
    ...(selected.value.category === 'route' ? { expected_upstream_registry_version: registryVersion.value } : {}),
  }
}
async function importRuleFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  loading.value = true
  try {
    if (file.size > 10 * 1024 * 1024) throw new Error('导入文件不能超过 10 MB')
    const decoded: unknown = JSON.parse(await file.text())
    const rawRules = Array.isArray(decoded) ? decoded : decoded && typeof decoded === 'object' ? (decoded as { rules?: unknown }).rules : undefined
    if (!Array.isArray(rawRules) || !rawRules.length) throw new Error('导入文件中没有规则')
    if (rawRules.length > 200000) throw new Error('导入规则不能超过 200,000 条')
    const entries = rawRules.map(importRuleInput)
    const seen = new Set<string>()
    for (const rule of entries) {
      const normalizedPattern = rule.match_type === 'regexp' ? rule.pattern.trim() : rule.pattern.trim().toLowerCase().replace(/\.$/, '')
      const key = `${rule.match_type}\u0000${normalizedPattern}`
      if (seen.has(key)) throw new Error(`导入文件包含重复规则：${rule.pattern}`)
      seen.add(key)
      if (visibleRules.value.some((current) => current.match_type === rule.match_type && (rule.match_type === 'regexp' ? current.pattern.trim() : current.pattern.toLowerCase().replace(/\.$/, '')) === normalizedPattern)) throw new Error(`规则已存在：${rule.pattern}`)
    }
    if (!await confirmAction(`将向“${selected.value.label}”追加导入 ${entries.length} 条手工规则，并立即发布新版本。`, { title: '确认导入规则', positiveText: '导入并发布' })) return
    const preview = await api.previewRuleImport(entries)
    const published = await api.importRules(preview.rules)
    notify.success(`已导入 ${entries.length} 条规则并发布版本 ${published.version}`)
    await load()
  } catch (error) { notify.error(error instanceof Error ? error.message : '导入规则失败') } finally { loading.value = false }
}

async function load() {
  loading.value = true
  try {
    const [ruleResult, sourceResult, registry] = await Promise.all([api.rules(), api.ruleSubscriptions(), api.upstreamGroups()])
    rules.value = ruleResult.items ?? []; subscriptions.value = sourceResult.items ?? []; groups.value = registry.groups ?? []; registryVersion.value = registry.version; defaultGroupID.value = registry.default_group_id
  } catch (e) { notify.error(e instanceof Error ? e.message : '无法加载规则') } finally { loading.value = false }
}
async function save() {
  loading.value = true
  try {
    const patterns = draftPatterns.value
    if (!patterns.length) throw new Error('请至少填写一个域名')
    if (selected.value.category === 'route' && !draft.upstream_group_id) throw new Error('请选择上游组')
    const registryPrecondition = selected.value.category === 'route' ? { expected_upstream_registry_version: registryVersion.value } : {}
    if (editing.value) {
      if (patterns.length !== 1) throw new Error('编辑规则时只能填写一个域名')
		const { ipv4_text, ipv6_text, ...base } = draft
		const ipv4 = addresses(ipv4_text); const ipv6 = addresses(ipv6_text)
		const answerFields = selected.value.category === 'answer' ? { ipv4_addresses: ipv4, ipv6_addresses: ipv6, ttl: draft.ttl } : {}
		if (selected.value.category === 'answer' && !ipv4.length && !ipv6.length) throw new Error('请至少填写一个 IPv4 或 IPv6 地址')
		const published = await api.updateRule(editing.value.id, { ...base, ...answerFields, pattern: patterns[0]!, updated_at_ms: editing.value.updated_at_ms, ...registryPrecondition })
      notify.success(`已发布版本 ${published.version}`)
    } else {
		const { ipv4_text, ipv6_text, ...base } = draft
		const ipv4 = addresses(ipv4_text); const ipv6 = addresses(ipv6_text)
		const answerFields = selected.value.category === 'answer' ? { ipv4_addresses: ipv4, ipv6_addresses: ipv6, ttl: draft.ttl } : {}
		if (selected.value.category === 'answer' && !ipv4.length && !ipv6.length) throw new Error('请至少填写一个 IPv4 或 IPv6 地址')
		const entries: RuleInput[] = patterns.map((pattern) => ({ ...base, ...answerFields, pattern, ...registryPrecondition }))
      const published = entries.length === 1 ? await api.createRule(entries[0]!) : await api.importRules((await api.previewRuleImport(entries)).rules)
      notify.success(`已发布版本 ${published.version}`)
    }
    showEditor.value = false; await load()
  } catch (e) { if ((e as ApiError).status === 409) { const editingID = editing.value?.id; await load(); if (editingID) editing.value = rules.value.find((rule) => rule.id === editingID) ?? null } notify.error(e instanceof Error ? e.message : '发布失败，已保留编辑内容') } finally { loading.value = false }
}
async function remove(rule: Rule) { if (!await confirmAction(`删除规则 ${rule.pattern} 后将立即发布新版本。`, { title: '删除规则', positiveText: '删除并发布', danger: true })) return; loading.value = true; try { const published = await api.deleteRule(rule.id); notify.success(`已发布版本 ${published.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '删除失败') } finally { loading.value = false } }
async function switchRule(rule: Rule, enabled: boolean) { try { const { id, ...input } = rule; const published = await api.updateRule(id, { ...input, enabled, ...(rule.category === 'route' ? { expected_upstream_registry_version: registryVersion.value } : {}) }); notify.success(`已发布版本 ${published.version}`); await load() } catch (e) { if ((e as ApiError).status === 409) await load(); notify.error(e instanceof Error ? e.message : '更新失败') } }
async function createSource() {
  const url = sourceDraft.url.trim(); const file = uploadFiles.value[0]
  if ((!url && !file) || (url && file)) { notify.error(url || file ? '一次只能添加一个 URL 或 TXT 订阅源' : '请输入订阅地址或选择 TXT 文件'); return }
  loading.value = true
  try {
    const routeBinding = selected.value.category === 'route' ? { upstream_group_id: sourceDraft.upstream_group_id, priority: sourceDraft.priority, expected_upstream_registry_version: registryVersion.value } : {}
    const base: Omit<RuleSubscriptionInput, 'source_url'> = { category: selected.value.category, action: selected.value.category === 'route' ? 'upstream' : selected.value.action, name: '', refresh_interval_seconds: sourceDraft.refresh_minutes * 60, enabled: true, ...routeBinding }
    const result = url ? await api.createRuleSubscription({ ...base, name: url, source_url: url }) : await api.uploadRuleSubscription({ ...base, name: file!.name }, file!)
    notify.success(`已添加订阅源，版本 ${result.version.version}`); showSourceEditor.value = false; await load()
  } catch (e) { if ((e as ApiError).status === 409) await load(); notify.error(e instanceof Error ? e.message : '添加订阅源失败') } finally { loading.value = false }
}
async function updateBinding() { if (!bindingTarget.value) return; loading.value = true; try { const result = await api.updateRuleSubscriptionBinding(bindingTarget.value.id, { ...bindingDraft, expected_upstream_registry_version: registryVersion.value }); if (result.version.version) notify.success(`已发布版本 ${result.version.version}`); showBindingEditor.value = false; await load() } catch (e) { if ((e as ApiError).status === 409) await load(); notify.error(e instanceof Error ? e.message : '绑定更新失败') } finally { loading.value = false } }
async function refreshSource(id: number) { loading.value = true; try { const result = await api.refreshRuleSubscription(id); notify.success(result.version ? `已更新并发布版本 ${result.version.version}` : '订阅内容未变化'); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '刷新订阅失败') } finally { loading.value = false } }
async function switchSource(id: number, enabled: boolean) { try { const result = await api.updateRuleSubscription(id, enabled); if (result.version.version) notify.success(`已发布版本 ${result.version.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '更新订阅失败') } }
async function removeSource(source: RuleSubscription) { if (!await confirmAction(`删除订阅源 ${source.name} 后，其规则将从运行时移除并发布新版本。`, { title: '删除订阅源', positiveText: '删除并发布', danger: true })) return; loading.value = true; try { const version = await api.deleteRuleSubscription(source.id); notify.success(`已发布版本 ${version.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '删除订阅失败') } finally { loading.value = false } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading"><div><p class="eyebrow">动态策略</p><h1>规则管理</h1></div></header>
    <nav class="rule-tabs"><button v-for="tab in tabs" :key="tab.key" :class="{ active: active === tab.key }" @click="selectTab(tab.key)">{{ tab.label }} <small>{{ tabRuleCount(tab.category, tab.action).toLocaleString() }}</small></button></nav>
    <div class="rule-context" :class="{ 'description-only': !supportsSubscriptions }"><p class="rule-description">{{ selected.description }}</p><nav v-if="supportsSubscriptions" class="rule-view-tabs" aria-label="规则来源"><button :class="{ active: ruleView === 'manual' }" @click="ruleView = 'manual'">手工规则 <small>{{ visibleRules.length }}</small></button><button :class="{ active: ruleView === 'subscription' }" @click="ruleView = 'subscription'">订阅源 <small>{{ visibleSubscriptions.length }}</small></button></nav></div>
    <section v-if="!supportsSubscriptions || ruleView === 'manual'" class="manual-rules">
      <header class="rule-section-heading"><div><p class="eyebrow">{{ selected.label }}</p><h2>手工规则</h2></div><div class="manual-rule-actions"><input ref="ruleImportInput" type="file" accept=".json,application/json" hidden @change="importRuleFile" /><NButton size="small" title="导入当前类型的 JSON 手工规则" :disabled="loading" aria-label="导入规则" @click="chooseRuleImport"><template #icon><Upload /></template></NButton><NButton size="small" title="导出当前类型的全部手工规则" :disabled="loading" aria-label="导出规则" @click="exportRules"><template #icon><Download /></template></NButton><NButton size="small" type="primary" @click="openCreate"><template #icon><Plus /></template>添加规则</NButton></div></header>
      <div class="table-wrap"><table><thead><tr><th>ID</th><th>域名</th><th v-if="selected.category === 'route'">上游组</th><th v-if="selected.category === 'answer'">IPv4</th><th v-if="selected.category === 'answer'">IPv6</th><th v-if="selected.category === 'answer'">TTL</th><th>匹配</th><th>优先级</th><th>备注</th><th>启用</th><th></th></tr></thead><tbody><tr v-for="rule in visibleRules" :key="rule.id"><td>{{ rule.id }}</td><td class="mono">{{ rule.pattern }}</td><td v-if="selected.category === 'route'">{{ groupName(rule.upstream_group_id ?? '') }}</td><td v-if="selected.category === 'answer'" class="mono address-list">{{ rule.ipv4_addresses?.join(', ') || '-' }}</td><td v-if="selected.category === 'answer'" class="mono address-list">{{ rule.ipv6_addresses?.join(', ') || '-' }}</td><td v-if="selected.category === 'answer'">{{ rule.ttl }} s</td><td>{{ rule.match_type }}</td><td>{{ rule.priority }}</td><td>{{ rule.comment || '-' }}</td><td><NSwitch :value="rule.enabled" @update:value="switchRule(rule, $event)" /></td><td class="actions"><NButton size="small" @click="openEdit(rule)">编辑</NButton><NButton size="small" type="error" secondary @click="remove(rule)">删除</NButton></td></tr><tr v-if="!loading && !visibleRules.length"><td :colspan="selected.category === 'route' ? 8 : selected.category === 'answer' ? 10 : 7" class="empty-cell">暂无手工规则</td></tr></tbody></table></div>
    </section>
    <section v-else class="subscription-sources">
      <header class="rule-section-heading"><div><p class="eyebrow">{{ selected.label }}</p><h2>订阅源</h2></div><NButton size="small" type="primary" @click="openSources">添加订阅源</NButton></header>
      <div class="table-wrap"><table><thead><tr><th>ID / 名称</th><th>来源</th><th>规则数</th><th v-if="selected.category === 'route'">上游组</th><th v-if="selected.category === 'route'">优先级</th><th>最后成功</th><th>状态</th><th>启用</th><th></th></tr></thead><tbody><tr v-for="source in visibleSubscriptions" :key="source.id"><td><span class="mono">#{{ source.id }}</span> {{ source.name }}</td><td class="mono source-url" :title="source.source_url">{{ source.kind === 'url' ? source.source_url : '本地 TXT' }}</td><td>{{ source.rule_count.toLocaleString() }}</td><td v-if="selected.category === 'route'">{{ groupName(source.binding?.upstream_group_id ?? '') }}</td><td v-if="selected.category === 'route'">{{ source.binding?.priority ?? '-' }}</td><td>{{ formatTime(source.last_success_at_ms) }}</td><td><span :class="{ 'source-error': source.last_error }">{{ source.last_error || '正常' }}</span></td><td><NSwitch :value="source.enabled" @update:value="switchSource(source.id, $event)" /></td><td class="actions"><NButton v-if="selected.category === 'route'" size="small" @click="openBinding(source)">绑定</NButton><NButton v-if="source.kind === 'url'" size="small" @click="refreshSource(source.id)">刷新</NButton><NButton size="small" type="error" secondary @click="removeSource(source)">删除</NButton></td></tr><tr v-if="!visibleSubscriptions.length"><td :colspan="selected.category === 'route' ? 9 : 7" class="empty-cell">暂无订阅源</td></tr></tbody></table></div>
    </section>
    <NModal v-model:show="showEditor" preset="card" :title="editing ? '编辑规则' : '新增规则'" class="rule-modal"><div class="form-grid rule-form"><label class="rule-domain-field">域名<NInput v-model:value="draft.pattern" :type="editing ? 'text' : 'textarea'" :autosize="editing ? false : { minRows: 5, maxRows: 12 }" placeholder="example.com&#10;example.net" /></label><label v-if="selected.category === 'route'">上游组<NSelect v-model:value="draft.upstream_group_id" :options="enabledGroupOptions" /></label><label v-if="selected.category === 'answer'" class="answer-address-field">IPv4 地址<NInput v-model:value="draft.ipv4_text" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="192.0.2.10&#10;192.0.2.11" /></label><label v-if="selected.category === 'answer'" class="answer-address-field">IPv6 地址<NInput v-model:value="draft.ipv6_text" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="2001:db8::10" /></label><label v-if="selected.category === 'answer'">TTL（秒）<NInputNumber v-model:value="draft.ttl" :min="1" :max="86400" /></label><label>匹配类型<NSelect v-model:value="draft.match_type" :options="[{ label: '域名后缀', value: 'domain' }, { label: '精确域名', value: 'full' }, { label: '正则表达式', value: 'regexp' }]" /></label><label>优先级（越小越优先）<NInputNumber v-model:value="draft.priority" :min="0" :max="1000" /></label><label>备注<NInput v-model:value="draft.comment" maxlength="500" /></label></div><small v-if="!editing && draftPatterns.length > 1" class="bulk-rule-count">将创建 {{ draftPatterns.length }} 条规则</small><div class="modal-actions"><NButton @click="showEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="save">保存并发布</NButton></div></NModal>
    <NModal v-model:show="showSourceEditor" preset="card" :title="`添加${selected.label}订阅源`" class="rule-modal"><div class="form-grid"><label class="rule-domain-field">订阅地址<NInput v-model:value="sourceDraft.url" placeholder="https://example.com/rules.txt" /></label><label>刷新间隔（分钟）<NInputNumber v-model:value="sourceDraft.refresh_minutes" :min="15" :max="43200" :step="15" /></label><label v-if="selected.category === 'route'">上游组<NSelect v-model:value="sourceDraft.upstream_group_id" :options="enabledGroupOptions" /></label><label v-if="selected.category === 'route'">优先级（越小越优先）<NInputNumber v-model:value="sourceDraft.priority" :min="0" :max="1000" /></label></div><input ref="uploadInput" type="file" accept=".txt,text/plain" hidden @change="selectFiles" /><div class="source-upload"><NButton @click="chooseFiles">选择 TXT</NButton><span class="muted">{{ uploadFiles.length ? uploadFiles[0]?.name : '未选择文件' }}</span></div><div class="modal-actions"><NButton @click="showSourceEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="createSource">添加并发布</NButton></div></NModal>
    <NModal v-model:show="showBindingEditor" preset="card" title="修改订阅绑定" class="rule-modal"><div class="form-grid"><label>上游组<NSelect v-model:value="bindingDraft.upstream_group_id" :options="enabledGroupOptions" /></label><label>优先级（越小越优先）<NInputNumber v-model:value="bindingDraft.priority" :min="0" :max="1000" /></label></div><div class="modal-actions"><NButton @click="showBindingEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="updateBinding">保存并发布</NButton></div></NModal>
  </section>
</template>
