<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { NButton, NInput, NInputNumber, NModal, NSelect, NSwitch } from 'naive-ui'
import { api, type Rule, type RuleInput, type RuleSubscriptionInput } from '@/lib/api'
import { notify } from '@/lib/notify'

const rules = ref<Rule[]>([])
const subscriptions = ref<Awaited<ReturnType<typeof api.ruleSubscriptions>>['items']>([])
const active = ref('block')
const ruleView = ref<'manual' | 'subscription'>('manual')
const loading = ref(false)
const showEditor = ref(false)
const showSourceEditor = ref(false)
const editing = ref<Rule | null>(null)
const uploadInput = ref<HTMLInputElement | null>(null)
const uploadFiles = ref<File[]>([])
const draft = reactive({ category: 'access', action: 'block', match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true })
const sourceDraft = reactive({ url: '', refresh_minutes: 1440 })
const tabs = [{ key: 'allow', label: '白名单', category: 'access', action: 'allow' }, { key: 'block', label: '黑名单', category: 'access', action: 'block' }, { key: 'local', label: '强制国内', category: 'route', action: 'local' }, { key: 'remote', label: '强制国外', category: 'route', action: 'remote' }, { key: 'no_log', label: '不记录日志', category: 'logging', action: 'no_log' }]
const selected = computed(() => tabs.find((tab) => tab.key === active.value) || tabs[1]!)
const visibleRules = computed(() => rules.value.filter((rule) => rule.category === selected.value.category && rule.action === selected.value.action))
const visibleSubscriptions = computed(() => subscriptions.value.filter((source) => source.category === selected.value.category && source.action === selected.value.action))
const subscriptionRuleCount = computed(() => visibleSubscriptions.value.reduce((total, source) => total + source.rule_count, 0))
function tabRuleCount(category: string, action: string) { return rules.value.filter((rule) => rule.category === category && rule.action === action).length + subscriptions.value.filter((source) => source.category === category && source.action === action).reduce((total, source) => total + source.rule_count, 0) }
const supportsSubscriptions = computed(() => selected.value.category !== 'logging')
const draftPatterns = computed(() => draft.pattern.split(/\r?\n/).map((pattern) => pattern.trim()).filter(Boolean))

function formatTime(value: number) { return value ? new Date(value).toLocaleString() : '-' }
function resetDraft() { Object.assign(draft, { category: selected.value.category, action: selected.value.action, match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true }); editing.value = null }
function openCreate() { resetDraft(); showEditor.value = true }
function openEdit(rule: Rule) { editing.value = rule; Object.assign(draft, rule); showEditor.value = true }
function openSources() { sourceDraft.url = ''; sourceDraft.refresh_minutes = 1440; uploadFiles.value = []; showSourceEditor.value = true }
async function load() { loading.value = true; try { const [ruleResult, sourceResult] = await Promise.all([api.rules(), api.ruleSubscriptions()]); rules.value = ruleResult.items ?? []; subscriptions.value = sourceResult.items ?? [] } catch (e) { notify.error(e instanceof Error ? e.message : '无法加载规则') } finally { loading.value = false } }
async function save() { loading.value = true; try { const patterns = draftPatterns.value; if (patterns.length === 0) throw new Error('请至少填写一个域名'); if (editing.value) { if (patterns.length !== 1) throw new Error('编辑规则时只能填写一个域名'); const published = await api.updateRule(editing.value.id, { ...draft, pattern: patterns[0]!, id: editing.value.id, updated_at_ms: editing.value.updated_at_ms }); notify.success(`已发布版本 ${published.version}`) } else { const entries: RuleInput[] = patterns.map((pattern) => ({ ...draft, pattern })); const published = entries.length === 1 ? await api.createRule(entries[0]!) : await api.importRules((await api.previewRuleImport(entries)).rules); notify.success(`已发布版本 ${published.version}`) }; showEditor.value = false; await load() } catch (e) { notify.error(e instanceof Error ? e.message : '发布失败，已保留编辑内容') } finally { loading.value = false } }
async function remove(rule: Rule) { if (!window.confirm(`确认删除规则 ${rule.pattern}？`)) return; loading.value = true; try { const published = await api.deleteRule(rule.id); notify.success(`已发布版本 ${published.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '删除失败') } finally { loading.value = false } }
async function switchRule(rule: Rule, enabled: boolean) { try { const published = await api.updateRule(rule.id, { ...rule, enabled }); notify.success(`已发布版本 ${published.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '更新失败') } }
function chooseFiles() { uploadInput.value?.click() }
function selectFiles(event: Event) { uploadFiles.value = Array.from((event.target as HTMLInputElement).files ?? []) }
async function createSources() { const url = sourceDraft.url.trim(); const file = uploadFiles.value[0]; if (!url && !file) { notify.error('请输入订阅地址或选择 TXT 文件'); return }; if (url && file) { notify.error('一次只能添加一个 URL 或 TXT 订阅源'); return }; loading.value = true; try { const base: Omit<RuleSubscriptionInput, 'source_url'> = { category: selected.value.category, action: selected.value.action, name: '', refresh_interval_seconds: sourceDraft.refresh_minutes * 60, enabled: true }; const result = url ? await api.createRuleSubscription({ ...base, name: url, source_url: url }) : await api.uploadRuleSubscription({ ...base, name: file!.name }, file!); notify.success(`已添加订阅源，版本 ${result.version.version}`); showSourceEditor.value = false; await load() } catch (e) { notify.error(e instanceof Error ? e.message : '添加订阅源失败') } finally { loading.value = false } }
async function refreshSource(id: number) { loading.value = true; try { const result = await api.refreshRuleSubscription(id); notify.success(result.version ? `已更新并发布版本 ${result.version.version}` : '订阅内容未变化'); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '刷新订阅失败') } finally { loading.value = false } }
async function switchSource(id: number, enabled: boolean) { try { const result = await api.updateRuleSubscription(id, enabled); if (result.version.version) notify.success(`已发布版本 ${result.version.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '更新订阅失败') } }
async function removeSource(id: number, name: string) { if (!window.confirm(`确认删除订阅源 ${name}？`)) return; loading.value = true; try { const version = await api.deleteRuleSubscription(id); notify.success(`已发布版本 ${version.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '删除订阅失败') } finally { loading.value = false } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading"><div><p class="eyebrow">动态策略</p><h1>规则管理</h1></div></header>
    <nav class="rule-tabs"><button v-for="tab in tabs" :key="tab.key" :class="{ active: active === tab.key }" @click="active = tab.key">{{ tab.label }} <small>{{ tabRuleCount(tab.category, tab.action).toLocaleString() }}</small></button></nav>
    <nav v-if="supportsSubscriptions" class="rule-view-tabs" aria-label="规则来源">
      <button :class="{ active: ruleView === 'manual' }" @click="ruleView = 'manual'">手工规则 <small>{{ visibleRules.length.toLocaleString() }}</small></button>
      <button :class="{ active: ruleView === 'subscription' }" @click="ruleView = 'subscription'">订阅源 <small>{{ subscriptionRuleCount.toLocaleString() }}</small></button>
    </nav>
    <section v-if="!supportsSubscriptions || ruleView === 'manual'" class="manual-rules">
      <header><p class="eyebrow">手工规则</p><h2>{{ selected.label }}规则</h2></header>
      <div class="section-action"><NButton size="small" type="primary" @click="openCreate">添加规则</NButton></div>
      <div class="table-wrap"><table><thead><tr><th>ID</th><th>域名</th><th>匹配</th><th>优先级</th><th>来源</th><th>备注</th><th>启用</th><th></th></tr></thead><tbody><tr v-for="rule in visibleRules" :key="rule.id"><td>{{ rule.id }}</td><td class="mono">{{ rule.pattern }}</td><td>{{ rule.match_type }}</td><td>{{ rule.priority }}</td><td>{{ rule.source }}</td><td>{{ rule.comment || '-' }}</td><td><NSwitch :value="rule.enabled" @update:value="switchRule(rule, $event)" /></td><td class="actions"><NButton size="small" @click="openEdit(rule)">编辑</NButton><NButton size="small" type="error" secondary @click="remove(rule)">删除</NButton></td></tr><tr v-if="!loading && !visibleRules.length"><td colspan="8" class="empty-cell">该分类没有手工规则</td></tr></tbody></table></div>
    </section>
    <section v-else class="subscription-sources">
      <header><p class="eyebrow">订阅源</p><h2>{{ selected.label }}订阅</h2></header>
      <div class="section-action"><NButton size="small" type="primary" @click="openSources">添加订阅源</NButton></div>
      <div class="table-wrap"><table><thead><tr><th>名称</th><th>来源</th><th>规则数</th><th>刷新间隔</th><th>最后成功</th><th>状态</th><th>启用</th><th></th></tr></thead><tbody><tr v-for="source in visibleSubscriptions" :key="source.id"><td>{{ source.name }}</td><td class="mono source-url" :title="source.source_url">{{ source.kind === 'url' ? source.source_url : '本地 TXT' }}</td><td>{{ source.rule_count.toLocaleString() }}</td><td>{{ source.kind === 'url' ? `${Math.round(source.refresh_interval_seconds / 60)} 分钟` : '-' }}</td><td>{{ formatTime(source.last_success_at_ms) }}</td><td><span :class="{ 'source-error': source.last_error }">{{ source.last_error || '正常' }}</span></td><td><NSwitch :value="source.enabled" @update:value="switchSource(source.id, $event)" /></td><td class="actions"><NButton v-if="source.kind === 'url'" size="small" @click="refreshSource(source.id)">刷新</NButton><NButton size="small" type="error" secondary @click="removeSource(source.id, source.name)">删除</NButton></td></tr><tr v-if="!visibleSubscriptions.length"><td colspan="8" class="empty-cell">暂无订阅源</td></tr></tbody></table></div>
    </section>
    <NModal v-model:show="showEditor" preset="card" :title="editing ? '编辑规则' : '新增规则'" class="rule-modal"><div class="form-grid rule-form"><label class="rule-domain-field">域名<NInput v-model:value="draft.pattern" :type="editing ? 'text' : 'textarea'" :autosize="editing ? false : { minRows: 5, maxRows: 12 }" placeholder="example.com&#10;example.net" /></label><label>匹配类型<NSelect v-model:value="draft.match_type" :options="[{ label: '域名后缀', value: 'domain' }, { label: '精确域名', value: 'full' }, { label: '正则表达式', value: 'regexp' }]" /></label><label>优先级<NInputNumber v-model:value="draft.priority" :min="0" :max="1000" /></label><label>备注<NInput v-model:value="draft.comment" maxlength="500" /></label></div><small v-if="!editing && draftPatterns.length > 1" class="bulk-rule-count">将创建 {{ draftPatterns.length }} 条规则</small><div class="modal-actions"><NButton @click="showEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="save">保存并发布</NButton></div></NModal>
    <NModal v-model:show="showSourceEditor" preset="card" :title="`添加${selected.label}订阅源`" class="rule-modal"><div class="form-grid"><label class="rule-domain-field">订阅地址<NInput v-model:value="sourceDraft.url" placeholder="https://example.com/rules.txt" /></label><label>刷新间隔（分钟）<NInputNumber v-model:value="sourceDraft.refresh_minutes" :min="15" :max="43200" :step="15" /></label></div><input ref="uploadInput" type="file" accept=".txt,text/plain" hidden @change="selectFiles" /><div class="source-upload"><NButton @click="chooseFiles">选择 TXT</NButton><span class="muted">{{ uploadFiles.length ? uploadFiles[0]?.name : '未选择文件' }}</span></div><div class="modal-actions"><NButton @click="showSourceEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="createSources">添加并发布</NButton></div></NModal>
  </section>
</template>
