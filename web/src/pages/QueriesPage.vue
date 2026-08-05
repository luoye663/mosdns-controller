<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { NAlert, NButton, NDatePicker, NInput, NModal, NPopover, NSelect, NSwitch, NTooltip } from 'naive-ui'
import { CircleHelp } from '@lucide/vue'
import { api, eventStream, type AnswerDiagnostics, type ApiError, type QueryEvent, type QueryParams, type QueryResultClass, type Rule, type UpstreamGroup } from '@/lib/api'
import { formatLatencyMs } from '@/lib/format'
import { notify } from '@/lib/notify'

defineOptions({ name: 'QueriesPage' })

type SavedFilter = { label: string; filters: Record<string, string>; range: [number, number] | null }
const storageKey = 'mosdns_query_filters'
const rows = ref<QueryEvent[]>([])
const cursor = ref('')
const pageSize = ref(100)
const loading = ref(false)
const live = ref(false)
const paused = ref(false)
const streamState = ref('未连接')
const pending = ref<QueryEvent[]>([])
const ruleLabels = ref<Record<number, string>>({})
const savedFilters = ref<SavedFilter[]>(readSavedFilters())
const selectedSaved = ref<string | null>(null)
const ruleTarget = ref<QueryEvent | null>(null)
const diagnosticTarget = ref<QueryEvent | null>(null)
const answerDiagnostics = ref<AnswerDiagnostics | null>(null)
const answerDiagnosticsError = ref('')
const ruleAction = ref('block')
const ruleUpstreamGroupID = ref('')
const upstreamGroups = ref<UpstreamGroup[]>([])
const upstreamRegistryVersion = ref(0)
const range = ref<[number, number] | null>(null)
const tableScroller = ref<HTMLElement | null>(null)
const topScroller = ref<HTMLElement | null>(null)
const topScrollContent = ref<HTMLElement | null>(null)
const queriesTable = ref<HTMLTableElement | null>(null)
const hasHorizontalScroll = ref(false)
const filters = reactive({ client_ip: '', qname: '', qname_match: 'contains', qtype: '', rcode: '', result_class: '', route: '', route_source: '', upstream_group: '', upstream_tag: '', protocol: '', cache_hit: '' })
let stream: EventSource | undefined
let tableResizeObserver: ResizeObserver | undefined

const routeOptions = [{ label: '全部路由', value: '' }, { label: '上游组', value: 'forward' }, { label: '拦截', value: 'block' }]
const booleanOptions = [{ label: '不限', value: '' }, { label: '是', value: 'true' }, { label: '否', value: 'false' }]
const typeOptions = [{ label: '全部类型', value: '' }, { label: 'A', value: '1' }, { label: 'AAAA', value: '28' }, { label: 'CNAME', value: '5' }, { label: 'MX', value: '15' }, { label: 'TXT', value: '16' }, { label: 'PTR', value: '12' }, { label: 'HTTPS', value: '65' }]
const rcodeOptions = [{ label: '全部结果', value: '' }, { label: 'NOERROR', value: '0' }, { label: 'FORMERR', value: '1' }, { label: 'SERVFAIL', value: '2' }, { label: 'NXDOMAIN', value: '3' }, { label: 'REFUSED', value: '5' }]
const resultClassOptions = [{ label: '全部分类', value: '' }, { label: '成功', value: 'success' }, { label: '负向应答', value: 'negative_answer' }, { label: '策略拦截', value: 'policy_block' }, { label: '处理错误', value: 'processing_error' }]
const actionOptions = [{ label: '拦截', value: 'block' }, { label: '放行', value: 'allow' }, { label: '指定上游组', value: 'upstream' }, { label: '不记录', value: 'no_log' }]
const upstreamGroupFilterOptions = computed(() => [{ label: '全部上游组', value: '' }, ...upstreamGroups.value.map((group) => ({ label: `${group.name} (${group.id})`, value: group.id }))])
const enabledUpstreamGroupOptions = computed(() => upstreamGroups.value.filter((group) => group.enabled).map((group) => ({ label: `${group.name} (${group.id})`, value: group.id })))
const timeShortcuts = {
  '1 小时': () => { const now = Date.now(); return [now - 60 * 60 * 1000, now] as [number, number] },
  '24 小时': () => { const now = Date.now(); return [now - 24 * 60 * 60 * 1000, now] as [number, number] },
  '7 天': () => { const now = Date.now(); return [now - 7 * 24 * 60 * 60 * 1000, now] as [number, number] },
}

function readSavedFilters(): SavedFilter[] {
  try { const value = JSON.parse(localStorage.getItem(storageKey) ?? '[]'); return Array.isArray(value) ? value : [] } catch { return [] }
}
function params(nextCursor = ''): QueryParams {
  return { limit: pageSize.value, cursor: nextCursor, from: range.value?.[0], to: range.value?.[1], ...filters }
}
function formatTime(ms: number) { return new Date(ms).toLocaleString() }
function formatClient(row: QueryEvent) { return row.device_name ? `${row.device_name} (${row.client_ip})` : row.client_ip }
function qtypeLabel(value: number) { return ({ 1: 'A', 5: 'CNAME', 12: 'PTR', 15: 'MX', 16: 'TXT', 28: 'AAAA', 65: 'HTTPS' } as Record<number, string>)[value] ?? `类型 ${value}` }
function qclassLabel(value: number) { return value === 1 ? 'IN' : `类别 ${value}` }
function rcodeLabel(value: number) { return ({ 0: '成功', 1: '格式错误', 2: '服务器失败', 3: '域名不存在', 4: '未实现', 5: '已拒绝' } as Record<number, string>)[value] ?? `返回码 ${value}` }
function routeLabel(value: string) { return ({ forward: '上游组', block: '拦截' } as Record<string, string>)[value] ?? value }
function resultClassLabel(value: QueryResultClass) { return ({ success: '成功', negative_answer: '负向应答', policy_block: '策略拦截', processing_error: '处理错误' } as Record<QueryResultClass, string>)[value] }
function formatTTL(value: number | null) { return value === null ? '-' : `${value} s` }
function ruleLabel(rule: Rule) {
  return ({
    'access:allow': '白名单',
    'access:block': '黑名单',
    'route:upstream': `上游组路由${rule.upstream_group_id ? ` (${rule.upstream_group_id})` : ''}`,
    'logging:no_log': '不记录日志',
  } as Record<string, string>)[`${rule.category}:${rule.action}`] ?? '动态规则'
}
function routeSourceLabel(row: QueryEvent) {
	if (row.subscription_categories?.length) return row.subscription_categories.map((category) => ({ allow: '白名单订阅集合', block: '黑名单订阅集合', upstream: '上游组订阅集合', access: '访问订阅', route: '路由订阅' } as Record<string, string>)[category] ?? category).join(' · ')
  if (row.route_source === 'default') return '默认路由'
	if (row.route_source === 'subscription') return '订阅集合'
	if (row.subscription_source_id) return `${row.subscription_source_name || '订阅源'} #${row.subscription_source_id}`
  return ruleLabels.value[row.route_rule_id] ?? ruleLabels.value[row.access_rule_id] ?? '已删除规则'
}
function errorText(row: QueryEvent) { return row.error_text || row.error_code || (row.rcode !== 0 ? rcodeLabel(row.rcode) : '') }
function hasError(row: QueryEvent) { return row.result_class === 'processing_error' }
function updateTopScrollbar() {
  const scroller = tableScroller.value
  const content = topScrollContent.value
  if (!scroller || !content) return
  hasHorizontalScroll.value = scroller.scrollWidth > scroller.clientWidth
  content.style.width = `${scroller.scrollWidth}px`
}
function syncTableScroll() { if (tableScroller.value && topScroller.value) tableScroller.value.scrollLeft = topScroller.value.scrollLeft }
function syncTopScroll() { if (tableScroller.value && topScroller.value) topScroller.value.scrollLeft = tableScroller.value.scrollLeft }
function closeStream() { stream?.close(); stream = undefined; streamState.value = '已断开' }
function insertLive(event: QueryEvent) { if (rows.value.some((row) => row.event_id === event.event_id)) return; rows.value = [event, ...rows.value].slice(0, 300) }
function startStream() {
  closeStream()
  streamState.value = '连接中'
  stream = eventStream(params())
  stream.onopen = () => { streamState.value = '已连接' }
  stream.onerror = () => { streamState.value = '重连中' }
  stream.addEventListener('connected', () => { streamState.value = '已连接' })
  stream.addEventListener('query', (message) => {
    const event = JSON.parse((message as MessageEvent).data) as QueryEvent
    if (paused.value) { pending.value = [...pending.value, event].slice(-300); return }
    insertLive(event)
  })
}
async function load(nextCursor = '') {
  loading.value = true
  try {
    const page = await api.queries(params(nextCursor))
    const items = page.items ?? []
    rows.value = nextCursor ? [...rows.value, ...items.filter((item) => !rows.value.some((row) => row.event_id === item.event_id))] : items
    cursor.value = page.next_cursor ?? ''
  } catch (e) { notify.error(e instanceof Error ? e.message : '无法加载查询日志') } finally { loading.value = false }
}
async function loadRuleLabels() {
  const result = await api.rules()
  ruleLabels.value = Object.fromEntries(result.items.map((rule) => [rule.id, ruleLabel(rule)]).filter(([, label]) => label))
}
async function loadUpstreamGroups() {
  const registry = await api.upstreamGroups()
  upstreamGroups.value = registry.groups ?? []
  upstreamRegistryVersion.value = registry.version
  ruleUpstreamGroupID.value = registry.groups.find((group) => group.enabled && group.id === registry.default_group_id)?.id ?? registry.groups.find((group) => group.enabled)?.id ?? ''
}
async function applyFilters() { pending.value = []; await load(); if (live.value) startStream() }
function toggleLive(enabled: boolean) { if (enabled) startStream(); else closeStream() }
function togglePaused(value: boolean) { if (!value && pending.value.length) { for (const event of pending.value) insertLive(event); pending.value = [] } }
function saveFilter() {
  const label = window.prompt('筛选名称')?.trim()
  if (!label) return
  const saved: SavedFilter = { label, filters: { ...filters }, range: range.value }
  savedFilters.value = [...savedFilters.value.filter((item) => item.label !== label), saved]
  localStorage.setItem(storageKey, JSON.stringify(savedFilters.value))
  selectedSaved.value = label
}
async function applySaved(label: string | null) {
  selectedSaved.value = label
  const saved = savedFilters.value.find((item) => item.label === label)
  if (!saved) return
  Object.assign(filters, saved.filters)
  range.value = saved.range
  await applyFilters()
}
function removeSaved() {
  if (!selectedSaved.value) return
  savedFilters.value = savedFilters.value.filter((item) => item.label !== selectedSaved.value)
  localStorage.setItem(storageKey, JSON.stringify(savedFilters.value))
  selectedSaved.value = null
}
async function createRule() {
  if (!ruleTarget.value) return
  loading.value = true
  const action = ruleAction.value
  const category = action === 'block' || action === 'allow' ? 'access' : action === 'no_log' ? 'logging' : 'route'
  try {
    if (action === 'upstream' && !ruleUpstreamGroupID.value) throw new Error('请选择上游组')
    const published = await api.createRule({ category, action, upstream_group_id: action === 'upstream' ? ruleUpstreamGroupID.value : '', match_type: 'domain', pattern: ruleTarget.value.qname, priority: 100, source: 'query', comment: `来自查询日志 ${ruleTarget.value.event_id}`, enabled: true, ...(action === 'upstream' ? { expected_upstream_registry_version: upstreamRegistryVersion.value } : {}) })
    notify.success(`已发布版本 ${published.version}，状态：${published.status}。`)
    ruleTarget.value = null
  } catch (e) { if ((e as ApiError).status === 409) await loadUpstreamGroups().catch(() => {}); notify.error(e instanceof Error ? e.message : '规则发布失败') } finally { loading.value = false }
}
async function openDiagnostics(row: QueryEvent) {
  diagnosticTarget.value = row
  answerDiagnostics.value = null
  answerDiagnosticsError.value = ''
  try {
    const diagnostics = await api.answerDiagnostics(row.event_id)
    answerDiagnostics.value = { answer_ips: diagnostics.answer_ips ?? [], answer_records: diagnostics.answer_records ?? [] }
  } catch (e) {
    answerDiagnosticsError.value = e instanceof Error ? e.message : '应答诊断数据已不可用'
  }
}
onMounted(async () => {
  await Promise.all([load(), loadRuleLabels().catch(() => {}), loadUpstreamGroups().catch(() => {})])
  await nextTick()
  updateTopScrollbar()
  tableResizeObserver = new ResizeObserver(updateTopScrollbar)
  if (tableScroller.value) tableResizeObserver.observe(tableScroller.value)
  if (queriesTable.value) tableResizeObserver.observe(queriesTable.value)
})
onBeforeUnmount(() => { closeStream(); tableResizeObserver?.disconnect() })
</script>

<template>
  <section class="page">
    <header class="page-heading">
      <div><p class="eyebrow">查询审计</p><h1>查询日志</h1></div>
      <div class="inline-controls"><span class="connection" :class="{ online: streamState === '已连接' }">{{ streamState }}</span><NSwitch v-model:value="live" @update:value="toggleLive"><template #checked>实时</template><template #unchecked>实时</template></NSwitch><NSwitch v-model:value="paused" :disabled="!live" @update:value="togglePaused"><template #checked>暂停</template><template #unchecked>暂停</template></NSwitch></div>
    </header>
    <div class="filters query-filters">
      <div class="time-filter"><NDatePicker v-model:value="range" type="datetimerange" clearable :shortcuts="timeShortcuts" /></div>
      <NInput v-model:value="filters.client_ip" placeholder="客户端 IP" clearable />
      <div class="domain-filter"><NInput v-model:value="filters.qname" placeholder="查询域名" clearable /><NSelect v-model:value="filters.qname_match" :options="[{ label: '包含', value: 'contains' }, { label: '精确', value: 'exact' }]" /></div>
      <NSelect v-model:value="filters.qtype" :options="typeOptions" /><NSelect v-model:value="filters.rcode" :options="rcodeOptions" /><NSelect v-model:value="filters.result_class" :options="resultClassOptions" /><NSelect v-model:value="filters.route" :options="routeOptions" />
      <NSelect v-model:value="filters.cache_hit" :options="booleanOptions.map((item) => ({ ...item, label: item.value === '' ? '全部缓存' : `缓存${item.label}` }))" />
      <NInput v-model:value="filters.route_source" placeholder="路由来源" clearable /><NSelect v-model:value="filters.upstream_group" :options="upstreamGroupFilterOptions" /><NInput v-model:value="filters.upstream_tag" placeholder="上游标签" clearable />
      <NSelect v-model:value="filters.protocol" :options="[{ label: '全部协议', value: '' }, { label: 'UDP', value: 'udp' }, { label: 'TCP', value: 'tcp' }]" />
      <NSelect v-model:value="pageSize" :options="[{ label: '每页 50 条', value: 50 }, { label: '每页 100 条', value: 100 }, { label: '每页 200 条', value: 200 }]" />
      <NButton type="primary" :loading="loading" @click="applyFilters">筛选</NButton>
    </div>
    <div class="inline-controls query-presets"><NSelect v-model:value="selectedSaved" :options="savedFilters.map((item) => ({ label: item.label, value: item.label }))" placeholder="保存的筛选" clearable @update:value="applySaved" /><NButton @click="saveFilter">保存筛选</NButton><NButton :disabled="!selectedSaved" @click="removeSaved">删除</NButton><span v-if="paused && pending.length" class="muted">已暂停 {{ pending.length }} 条新查询</span></div>
    <div v-show="hasHorizontalScroll" ref="topScroller" class="queries-top-scrollbar" aria-label="查询表格横向滚动" @scroll="syncTableScroll"><div ref="topScrollContent"></div></div>
    <div ref="tableScroller" class="table-wrap queries-table-wrap" @scroll="syncTopScroll"><table ref="queriesTable"><colgroup><col class="query-time-column"><col class="query-client-column"><col class="query-domain-column"><col class="query-result-column"><col class="query-route-column"><col class="query-cache-column"><col class="query-ttl-column"><col class="query-latency-column"><col class="query-rules-column"><col class="query-diagnostics-column"><col class="query-actions-column"></colgroup><thead><tr><th>时间</th><th>客户端</th><th>域名 / 类型</th><th>结果</th><th>路由</th><th>缓存</th><th>TTL</th><th>延迟</th><th>规则 / 版本</th><th>诊断</th><th>操作</th></tr></thead><tbody>
		<tr v-for="row in rows" :key="row.event_id"><td>{{ formatTime(row.timestamp_unix_ms) }}</td><td><NTooltip :delay="300"><template #trigger><div class="query-cell-text">{{ formatClient(row) }}</div></template>{{ formatClient(row) }}</NTooltip></td><td><NTooltip :delay="300"><template #trigger><div class="query-cell-text mono">{{ row.qname }}</div></template>{{ row.qname }}</NTooltip><small>{{ row.protocol.toUpperCase() }} / {{ qtypeLabel(row.qtype) }} / {{ qclassLabel(row.qclass) }}</small></td><td><span class="result-class" :class="row.result_class">{{ resultClassLabel(row.result_class) }}</span><small>{{ rcodeLabel(row.rcode) }}</small></td><td class="route-upstream"><div class="route-cell"><span class="route-tag" :class="row.route">{{ routeLabel(row.route) }}</span></div><div class="route-upstream-value mono" :title="[row.upstream_group, row.upstream_tag].filter(Boolean).join(' / ') || '未记录上游'">{{ [row.upstream_group, row.upstream_tag].filter(Boolean).join(' / ') || '-' }}</div><small :title="routeSourceLabel(row)">{{ routeSourceLabel(row) }}</small></td><td><span class="cache-status" :class="row.cache_hit ? 'hit' : 'miss'">{{ row.cache_hit ? '命中' : '未命中' }}</span></td><td>{{ formatTTL(row.answer_min_ttl_seconds) }}</td><td>{{ formatLatencyMs(row.latency_us) }}</td><td><div class="query-cell-text">访问 {{ row.access_rule_id || '-' }} / 路由 {{ row.route_rule_id || '-' }} / 绑定 {{ row.subscription_binding_id || '-' }}</div><small>快照 {{ row.snapshot_version }}</small></td><td :title="errorText(row)"><div v-if="hasError(row)" class="query-cell-text query-error">{{ errorText(row) }}</div><div v-if="row.answer_count"><NButton size="tiny" text @click="openDiagnostics(row)">{{ row.answer_count }} 条 DNS 应答</NButton></div></td><td><NButton size="small" @click="ruleTarget = row">建规则</NButton></td></tr>
      <tr v-if="!loading && !rows.length"><td colspan="11" class="empty-cell">暂无查询日志</td></tr>
    </tbody></table></div>
    <footer class="table-footer"><span>已显示 {{ rows.length }} 条；查询使用 cursor 分页</span><NButton v-if="cursor" :loading="loading" @click="load(cursor)">加载下一页</NButton></footer>
    <NModal :show="Boolean(ruleTarget)" preset="card" title="从查询创建规则" class="rule-modal" @update:show="(show) => { if (!show) ruleTarget = null }"><p class="mono">{{ ruleTarget?.qname }}</p><div class="form-grid"><label>规则类型<NSelect v-model:value="ruleAction" :options="actionOptions" /></label><label v-if="ruleAction === 'upstream'">上游组<NSelect v-model:value="ruleUpstreamGroupID" :options="enabledUpstreamGroupOptions" /></label></div><div class="modal-actions"><NButton @click="ruleTarget = null">取消</NButton><NButton type="primary" :loading="loading" @click="createRule">保存并发布</NButton></div></NModal>
    <NModal :show="Boolean(diagnosticTarget)" preset="card" class="rule-modal" @update:show="(show) => { if (!show) diagnosticTarget = null }"><template #header><div class="modal-title-with-help"><span>DNS 应答详情</span><NPopover trigger="click"><template #trigger><NButton text circle size="small" aria-label="DNS 应答采集说明"><template #icon><CircleHelp :size="17" /></template></NButton></template><div class="answer-diagnostics-help">需在 <code>query_audit</code> 配置中启用 <code>include_answers: true</code> 才会采集 DNS 应答数据。应答明细仅保留在内存中，未持久化；缓存淘汰或服务重启后将不可查看。</div></NPopover></div></template><p class="mono">{{ diagnosticTarget?.qname }}</p><NAlert v-if="answerDiagnosticsError" type="warning">{{ answerDiagnosticsError }}</NAlert><p v-else-if="answerDiagnostics === null" class="muted">正在读取内存缓存...</p><template v-else><p class="muted">共 {{ diagnosticTarget?.answer_count ?? 0 }} 条 DNS 应答，提取到 {{ answerDiagnostics.answer_ips.length }} 个唯一 A/AAAA 地址。</p><div v-if="answerDiagnostics.answer_ips.length" class="mono">{{ answerDiagnostics.answer_ips.join('\n') }}</div><p v-else class="muted">该应答不包含 A 或 AAAA 地址。</p><p class="muted">Answer 区记录（已展示 {{ answerDiagnostics.answer_records.length }} / {{ diagnosticTarget?.answer_count ?? 0 }} 条）</p><pre v-if="answerDiagnostics.answer_records.length" class="answer-records mono">{{ answerDiagnostics.answer_records.join('\n') }}</pre><p v-else class="muted">该查询未采集 Answer 区记录。</p></template></NModal>
  </section>
</template>
