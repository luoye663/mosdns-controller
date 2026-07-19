<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { NAlert, NButton, NDatePicker, NInput, NModal, NSelect, NSwitch, NTooltip } from 'naive-ui'
import { api, eventStream, type QueryEvent, type QueryParams, type Rule } from '@/lib/api'
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
const answerIPs = ref<string[] | null>(null)
const answerIPsError = ref('')
const ruleAction = ref('block')
const range = ref<[number, number] | null>(null)
const tableScroller = ref<HTMLElement | null>(null)
const topScroller = ref<HTMLElement | null>(null)
const topScrollContent = ref<HTMLElement | null>(null)
const queriesTable = ref<HTMLTableElement | null>(null)
const hasHorizontalScroll = ref(false)
const filters = reactive({ client_ip: '', qname: '', qname_match: 'contains', qtype: '', rcode: '', route: '', route_source: '', upstream_tag: '', protocol: '', cache_hit: '', has_error: '' })
let stream: EventSource | undefined
let tableResizeObserver: ResizeObserver | undefined

const routeOptions = [{ label: '全部路由', value: '' }, { label: '本地', value: 'local' }, { label: '远程', value: 'remote' }, { label: '拦截', value: 'block' }]
const booleanOptions = [{ label: '不限', value: '' }, { label: '是', value: 'true' }, { label: '否', value: 'false' }]
const typeOptions = [{ label: '全部类型', value: '' }, { label: 'A', value: '1' }, { label: 'AAAA', value: '28' }, { label: 'CNAME', value: '5' }, { label: 'MX', value: '15' }, { label: 'TXT', value: '16' }, { label: 'PTR', value: '12' }, { label: 'HTTPS', value: '65' }]
const rcodeOptions = [{ label: '全部结果', value: '' }, { label: 'NOERROR', value: '0' }, { label: 'FORMERR', value: '1' }, { label: 'SERVFAIL', value: '2' }, { label: 'NXDOMAIN', value: '3' }, { label: 'REFUSED', value: '5' }]
const actionOptions = [{ label: '拦截', value: 'block' }, { label: '放行', value: 'allow' }, { label: '强制本地', value: 'local' }, { label: '强制远程', value: 'remote' }, { label: '不记录', value: 'no_log' }]
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
function routeLabel(value: string) { return ({ local: '本地', remote: '远程', block: '拦截' } as Record<string, string>)[value] ?? value }
function formatTTL(value: number | null) { return value === null ? '-' : `${value} 秒` }
function ruleLabel(rule: Rule) {
  return ({
    'access:allow': '白名单',
    'access:block': '黑名单',
    'route:local': '强制国内',
    'route:remote': '强制国外',
    'logging:no_log': '不记录日志',
  } as Record<string, string>)[`${rule.category}:${rule.action}`] ?? '动态规则'
}
function routeSourceLabel(row: QueryEvent) {
  if (row.route_source === 'default') return '默认路由'
	if (row.route_source === 'subscription') return '订阅集合'
	if (row.subscription_source_id) return `${row.subscription_source_name || '订阅源'} #${row.subscription_source_id}`
  return ruleLabels.value[row.route_rule_id] ?? ruleLabels.value[row.access_rule_id] ?? '已删除规则'
}
function errorText(row: QueryEvent) { return row.error_text || row.error_code || (row.rcode !== 0 ? rcodeLabel(row.rcode) : '') }
function hasError(row: QueryEvent) { return Boolean(row.error_code || row.error_text || row.rcode !== 0) }
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
    const published = await api.createRule({ category, action, match_type: 'domain', pattern: ruleTarget.value.qname, priority: 100, source: 'query', comment: `来自查询日志 ${ruleTarget.value.event_id}`, enabled: true })
    notify.success(`已发布版本 ${published.version}，状态：${published.status}。`)
    ruleTarget.value = null
  } catch (e) { notify.error(e instanceof Error ? e.message : '规则发布失败') } finally { loading.value = false }
}
async function openDiagnostics(row: QueryEvent) {
  diagnosticTarget.value = row
  answerIPs.value = null
  answerIPsError.value = ''
  try {
    answerIPs.value = (await api.answerIPs(row.event_id)).answer_ips ?? []
  } catch (e) {
    answerIPsError.value = e instanceof Error ? e.message : '应答 IP 已不可用'
  }
}
onMounted(async () => {
  await Promise.all([load(), loadRuleLabels().catch(() => {})])
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
      <NSelect v-model:value="filters.qtype" :options="typeOptions" /><NSelect v-model:value="filters.rcode" :options="rcodeOptions" /><NSelect v-model:value="filters.route" :options="routeOptions" />
      <NSelect v-model:value="filters.cache_hit" :options="booleanOptions.map((item) => ({ ...item, label: item.value === '' ? '全部缓存' : `缓存${item.label}` }))" />
      <NSelect v-model:value="filters.has_error" :options="booleanOptions.map((item) => ({ ...item, label: item.value === '' ? '全部状态' : item.value === 'true' ? '仅错误' : '无错误' }))" />
      <NInput v-model:value="filters.route_source" placeholder="路由来源" clearable /><NInput v-model:value="filters.upstream_tag" placeholder="上游标签" clearable />
      <NSelect v-model:value="filters.protocol" :options="[{ label: '全部协议', value: '' }, { label: 'UDP', value: 'udp' }, { label: 'TCP', value: 'tcp' }]" />
      <NSelect v-model:value="pageSize" :options="[{ label: '每页 50 条', value: 50 }, { label: '每页 100 条', value: 100 }, { label: '每页 200 条', value: 200 }]" />
      <NButton type="primary" :loading="loading" @click="applyFilters">筛选</NButton>
    </div>
    <div class="inline-controls query-presets"><NSelect v-model:value="selectedSaved" :options="savedFilters.map((item) => ({ label: item.label, value: item.label }))" placeholder="保存的筛选" clearable @update:value="applySaved" /><NButton @click="saveFilter">保存筛选</NButton><NButton :disabled="!selectedSaved" @click="removeSaved">删除</NButton><span v-if="paused && pending.length" class="muted">已暂停 {{ pending.length }} 条新查询</span></div>
    <div v-show="hasHorizontalScroll" ref="topScroller" class="queries-top-scrollbar" aria-label="查询表格横向滚动" @scroll="syncTableScroll"><div ref="topScrollContent"></div></div>
    <div ref="tableScroller" class="table-wrap queries-table-wrap" @scroll="syncTopScroll"><table ref="queriesTable"><colgroup><col class="query-time-column"><col class="query-client-column"><col class="query-domain-column"><col class="query-result-column"><col class="query-route-column"><col class="query-cache-column"><col class="query-ttl-column"><col class="query-latency-column"><col class="query-rules-column"><col class="query-diagnostics-column"><col class="query-actions-column"></colgroup><thead><tr><th>时间</th><th>客户端</th><th>域名 / 类型</th><th>结果</th><th>路由</th><th>缓存</th><th>TTL</th><th>延迟</th><th>规则 / 版本</th><th>诊断</th><th></th></tr></thead><tbody>
      <tr v-for="row in rows" :key="row.event_id"><td>{{ formatTime(row.timestamp_unix_ms) }}</td><td><NTooltip :delay="300"><template #trigger><div class="query-cell-text">{{ formatClient(row) }}</div></template>{{ formatClient(row) }}</NTooltip></td><td><NTooltip :delay="300"><template #trigger><div class="query-cell-text mono">{{ row.qname }}</div></template>{{ row.qname }}</NTooltip><small>{{ row.protocol.toUpperCase() }} / {{ qtypeLabel(row.qtype) }} / {{ qclassLabel(row.qclass) }}</small></td><td :class="{ 'query-error': hasError(row) }">{{ rcodeLabel(row.rcode) }}</td><td class="route-upstream"><div class="route-cell"><span class="route-tag" :class="row.route">{{ routeLabel(row.route) }}</span><span class="upstream-tag mono" :title="row.upstream_tag || '未记录上游'">{{ row.upstream_tag || '-' }}</span></div><small>{{ routeSourceLabel(row) }}</small></td><td><span class="cache-status" :class="row.cache_hit ? 'hit' : 'miss'">{{ row.cache_hit ? '命中' : '未命中' }}</span></td><td>{{ formatTTL(row.answer_min_ttl_seconds) }}</td><td>{{ formatLatencyMs(row.latency_us) }}</td><td><div class="query-cell-text">访问 {{ row.access_rule_id || '-' }} / 路由 {{ row.route_rule_id || '-' }}</div><small>快照 {{ row.snapshot_version }}</small></td><td :title="errorText(row)"><div v-if="hasError(row)" class="query-cell-text">{{ errorText(row) }}</div><div v-if="row.answer_count"><NButton size="tiny" text @click="openDiagnostics(row)">{{ row.answer_count }} 条应答</NButton></div></td><td><NButton size="small" @click="ruleTarget = row">建规则</NButton></td></tr>
      <tr v-if="!loading && !rows.length"><td colspan="11" class="empty-cell">暂无查询日志</td></tr>
    </tbody></table></div>
    <footer class="table-footer"><span>已显示 {{ rows.length }} 条；查询使用 cursor 分页</span><NButton v-if="cursor" :loading="loading" @click="load(cursor)">加载下一页</NButton></footer>
    <NModal :show="Boolean(ruleTarget)" preset="card" title="从查询创建规则" class="rule-modal" @update:show="(show) => { if (!show) ruleTarget = null }"><p class="mono">{{ ruleTarget?.qname }}</p><NSelect v-model:value="ruleAction" :options="actionOptions" /><div class="modal-actions"><NButton @click="ruleTarget = null">取消</NButton><NButton type="primary" :loading="loading" @click="createRule">保存并发布</NButton></div></NModal>
    <NModal :show="Boolean(diagnosticTarget)" preset="card" title="应答 IP" class="rule-modal" @update:show="(show) => { if (!show) diagnosticTarget = null }"><p class="mono">{{ diagnosticTarget?.qname }}</p><NAlert v-if="answerIPsError" type="warning">{{ answerIPsError }}</NAlert><p v-else-if="answerIPs === null" class="muted">正在读取内存缓存...</p><p v-else-if="!answerIPs.length" class="muted">该应答不包含 A 或 AAAA 地址。</p><div v-else class="mono">{{ answerIPs.join('\n') }}</div></NModal>
  </section>
</template>
