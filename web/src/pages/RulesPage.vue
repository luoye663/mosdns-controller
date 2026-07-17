<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NInput, NInputNumber, NModal, NSelect, NSwitch } from 'naive-ui'
import { api, type GeositeStatus, type Rule, type RuleInput, type Version } from '@/lib/api'

const router = useRouter()
const rules = ref<Rule[]>([])
const geosite = ref<GeositeStatus | null>(null)
const geositeError = ref('')
const active = ref('block')
const loading = ref(false)
const error = ref('')
const published = ref<Version | null>(null)
const showEditor = ref(false)
const editing = ref<Rule | null>(null)
const draft = reactive({ category: 'access', action: 'block', match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true })
const tabs = [{ key: 'allow', label: '白名单', category: 'access', action: 'allow' }, { key: 'block', label: '黑名单', category: 'access', action: 'block' }, { key: 'local', label: '强制国内', category: 'route', action: 'local' }, { key: 'remote', label: '强制国外', category: 'route', action: 'remote' }, { key: 'no_log', label: '不记录日志', category: 'logging', action: 'no_log' }, { key: 'geosite', label: '国内域名订阅', category: '', action: '' }]
const selected = computed(() => tabs.find((tab) => tab.key === active.value) || tabs[1]!)
const visibleRules = computed(() => rules.value.filter((rule) => rule.category === selected.value.category && rule.action === selected.value.action))
const draftPatterns = computed(() => draft.pattern.split(/\r?\n/).map((pattern) => pattern.trim()).filter(Boolean))

function formatTime(value: string) { return value ? new Date(value).toLocaleString() : '-' }
function geositeSource(status: GeositeStatus) { return status.source_url || '本地上传' }
function resetDraft() { Object.assign(draft, { category: selected.value.category, action: selected.value.action, match_type: 'domain', pattern: '', priority: 100, source: 'manual', comment: '', enabled: true }); editing.value = null }
function openCreate() { resetDraft(); showEditor.value = true }
function openEdit(rule: Rule) { editing.value = rule; Object.assign(draft, rule); showEditor.value = true }
async function load() {
  loading.value = true
  error.value = ''
  geositeError.value = ''
  try {
    const [ruleResult, geositeResult] = await Promise.allSettled([api.rules(), api.geositeStatus()])
    if (ruleResult.status === 'rejected') throw ruleResult.reason
    rules.value = ruleResult.value.items ?? []
    if (geositeResult.status === 'fulfilled') geosite.value = geositeResult.value
    else {
      geosite.value = null
      geositeError.value = geositeResult.reason instanceof Error ? geositeResult.reason.message : '无法读取订阅状态'
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : '无法加载规则'
  } finally {
    loading.value = false
  }
}
async function save() {
  loading.value = true
  error.value = ''
  try {
    const patterns = draftPatterns.value
    if (patterns.length === 0) throw new Error('请至少填写一个域名')
    if (editing.value) {
      if (patterns.length !== 1) throw new Error('编辑规则时只能填写一个域名')
      published.value = await api.updateRule(editing.value.id, { ...draft, pattern: patterns[0]!, id: editing.value.id, updated_at_ms: editing.value.updated_at_ms })
    } else {
      const entries: RuleInput[] = patterns.map((pattern) => ({ ...draft, pattern }))
      if (entries.length === 1) published.value = await api.createRule(entries[0]!)
      else published.value = await api.importRules((await api.previewRuleImport(entries)).rules)
    }
    showEditor.value = false
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : '发布失败，已保留编辑内容'
  } finally {
    loading.value = false
  }
}
async function remove(rule: Rule) { if (!window.confirm(`确认删除规则 ${rule.pattern}？`)) return; loading.value = true; try { published.value = await api.deleteRule(rule.id); await load() } catch (e) { error.value = e instanceof Error ? e.message : '删除失败' } finally { loading.value = false } }
async function switchRule(rule: Rule, enabled: boolean) { try { published.value = await api.updateRule(rule.id, { ...rule, enabled }); await load() } catch (e) { error.value = e instanceof Error ? e.message : '更新失败' } }
onMounted(load)
</script>

<template>
  <section class="page">
    <header class="page-heading"><div><p class="eyebrow">动态策略</p><h1>规则管理</h1></div><NButton v-if="active !== 'geosite'" type="primary" @click="openCreate">新增规则</NButton></header>
    <NAlert v-if="published" type="success" closable @close="published = null" class="form-alert">已发布版本 {{ published.version }}，状态：{{ published.status }}。</NAlert>
    <NAlert v-if="error" type="error" closable @close="error = ''" class="form-alert">{{ error }}</NAlert>
    <nav class="rule-tabs"><button v-for="tab in tabs" :key="tab.key" :class="{ active: active === tab.key }" @click="active = tab.key">{{ tab.label }} <small v-if="tab.key !== 'geosite'">{{ rules.filter((r) => r.category === tab.category && r.action === tab.action).length }}</small></button></nav>
    <section v-if="active === 'geosite'" class="subscription-panel">
      <NAlert v-if="geositeError" type="warning">国内域名订阅状态不可用：{{ geositeError }}</NAlert>
      <template v-else-if="geosite"><div><p class="eyebrow">国内域名订阅</p><h2>手工规则优先</h2></div><dl class="status-list"><dt>来源</dt><dd class="mono" :title="geositeSource(geosite)">{{ geositeSource(geosite) }}</dd><dt>规则数量</dt><dd>{{ geosite.rule_count.toLocaleString() }}</dd><dt>运行时版本</dt><dd>{{ geosite.version }}</dd><dt>更新时间</dt><dd>{{ formatTime(geosite.loaded_at) }}</dd></dl><NButton @click="router.push({ name: 'settings' })">管理订阅</NButton></template>
      <span v-else class="empty-cell">暂无国内域名订阅状态</span>
    </section>
    <div v-else class="table-wrap"><table><thead><tr><th>ID</th><th>域名</th><th>匹配</th><th>优先级</th><th>来源</th><th>备注</th><th>启用</th><th></th></tr></thead><tbody><tr v-for="rule in visibleRules" :key="rule.id"><td>{{ rule.id }}</td><td class="mono">{{ rule.pattern }}</td><td>{{ rule.match_type }}</td><td>{{ rule.priority }}</td><td>{{ rule.source }}</td><td>{{ rule.comment || '-' }}</td><td><NSwitch :value="rule.enabled" @update:value="switchRule(rule, $event)" /></td><td class="actions"><NButton size="small" @click="openEdit(rule)">编辑</NButton><NButton size="small" type="error" secondary @click="remove(rule)">删除</NButton></td></tr><tr v-if="!loading && !visibleRules.length"><td colspan="8" class="empty-cell">该分类没有规则</td></tr></tbody></table></div>
    <NModal v-model:show="showEditor" preset="card" :title="editing ? '编辑规则' : '新增规则'" class="rule-modal"><div class="form-grid rule-form"><label class="rule-domain-field">域名<NInput v-model:value="draft.pattern" :type="editing ? 'text' : 'textarea'" :autosize="editing ? false : { minRows: 5, maxRows: 12 }" placeholder="example.com&#10;example.net" /></label><label>匹配类型<NSelect v-model:value="draft.match_type" :options="[{ label: '域名后缀', value: 'domain' }, { label: '精确域名', value: 'full' }, { label: '正则表达式', value: 'regexp' }]" /></label><label>优先级<NInputNumber v-model:value="draft.priority" :min="0" :max="1000" /></label><label>备注<NInput v-model:value="draft.comment" maxlength="500" /></label></div><small v-if="!editing && draftPatterns.length > 1" class="bulk-rule-count">将创建 {{ draftPatterns.length }} 条规则</small><div class="modal-actions"><NButton @click="showEditor = false">取消</NButton><NButton type="primary" :loading="loading" @click="save">保存并发布</NButton></div></NModal>
  </section>
</template>
