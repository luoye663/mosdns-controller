<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import * as echarts from 'echarts'
import { NButton, NSpin } from 'naive-ui'
import { api, type DashboardSummary, type LatencyPoint, type SystemStatus } from '@/lib/api'
import { formatLatencyMs } from '@/lib/format'
import { notify } from '@/lib/notify'

type StatItem = { value: string | number; query_count: number }
const loading = ref(true)
const summary = ref<DashboardSummary>({ query_count: 0, last_hour_query_count: 0, average_latency_us: 0, p95_latency_us: 0, p95_sample_count: 0, max_latency_us: 0, error_count: 0, processing_error_count: 0, success_count: 0, negative_answer_count: 0, policy_block_count: 0, cache_hit_count: 0 })
const domains = ref<StatItem[]>([])
const clients = ref<StatItem[]>([])
const upstreamGroups = ref<StatItem[]>([])
const latency = ref<LatencyPoint[]>([])
const status = ref<SystemStatus | null>(null)
const trendElement = ref<HTMLElement | null>(null)
const distributionElement = ref<HTMLElement | null>(null)
let trendChart: echarts.ECharts | undefined
let distributionChart: echarts.ECharts | undefined

function percent(value: number, total = summary.value.query_count) { return total ? `${((value / total) * 100).toFixed(1)}%` : '0.0%' }
function chartLabel(timestamp: number) { return new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) }
function memory(bytes?: number) { return bytes && bytes > 0 ? `${(bytes / 1024 / 1024).toFixed(1)} MiB` : '-' }
function renderCharts() {
  if (!trendElement.value || !distributionElement.value) return
  trendChart ??= echarts.init(trendElement.value)
  distributionChart ??= echarts.init(distributionElement.value)
  const labels = latency.value.map((item) => chartLabel(item.hour_start_ms))
  trendChart.setOption({
    animationDuration: 250,
    color: ['#087e8b', '#e7793c'],
    grid: { top: 28, right: 16, bottom: 28, left: 42 },
    tooltip: { trigger: 'axis' },
    legend: { data: ['查询数', '平均延迟'], left: 'center', top: 0, textStyle: { fontSize: 12 } },
    xAxis: { type: 'category', data: labels, axisTick: { show: false }, axisLabel: { color: '#718084', fontSize: 11 } },
    yAxis: [{ type: 'value', name: '请求', minInterval: 1, splitLine: { lineStyle: { color: '#edf1f1' } }, axisLabel: { color: '#718084' } }, { type: 'value', name: 'ms', splitLine: { show: false }, axisLabel: { color: '#718084' } }],
    series: [
      { name: '查询数', type: 'bar', data: latency.value.map((item) => item.query_count), barMaxWidth: 20, itemStyle: { borderRadius: [3, 3, 0, 0] } },
      { name: '平均延迟', type: 'line', yAxisIndex: 1, data: latency.value.map((item) => Number((item.average_latency_us / 1000).toFixed(2))), smooth: true, symbol: 'none', lineStyle: { width: 2 } },
    ],
  }, true)
  distributionChart.setOption({
    animationDuration: 250,
    color: ['#087e8b', '#e7793c', '#4f6fad', '#d15252'],
    tooltip: { trigger: 'item', formatter: '{b}: {c} ({d}%)' },
    legend: { bottom: 0, icon: 'circle', textStyle: { fontSize: 12 } },
    series: [{ type: 'pie', radius: ['48%', '72%'], center: ['50%', '44%'], label: { show: false }, data: upstreamGroups.value.map((item) => ({ name: String(item.value), value: item.query_count })) }],
  }, true)
}
async function load() {
  loading.value = true
  try {
    const [nextSummary, nextDomains, nextClients, nextUpstreamGroups, nextLatency, nextStatus] = await Promise.all([api.summary(), api.statistic('domains'), api.statistic('clients'), api.statistic('upstream_groups'), api.latency(), api.systemStatus()])
    summary.value = nextSummary
    domains.value = nextDomains.items
    clients.value = nextClients.items
    upstreamGroups.value = nextUpstreamGroups.items
    latency.value = nextLatency.items
    status.value = nextStatus
    await nextTick()
    renderCharts()
  } catch (cause) {
    notify.error(cause instanceof Error ? cause.message : '无法加载概况数据')
  } finally {
    loading.value = false
  }
}
function resizeCharts() { trendChart?.resize(); distributionChart?.resize() }
onMounted(() => { window.addEventListener('resize', resizeCharts); load() })
onBeforeUnmount(() => { window.removeEventListener('resize', resizeCharts); trendChart?.dispose(); distributionChart?.dispose() })
</script>

<template>
  <section class="page overview-page">
    <header class="page-heading">
      <div><p class="eyebrow">运行概览</p><h1>DNS 查询状态</h1></div>
      <NButton @click="load" :loading="loading">刷新数据</NButton>
    </header>
    <NSpin :show="loading">
      <div class="health-strip">
        <div><span class="health-dot" :class="status?.mosdns?.state === 'ready' ? 'healthy' : 'unhealthy'"></span><b>mosdns</b><small>{{ status?.mosdns?.state ?? status?.mosdns_error ?? '状态未知' }}</small></div>
        <div><span class="health-dot healthy"></span><b>Controller</b><small>{{ status?.controller.version ?? '运行中' }}</small></div>
        <div><b>内存占用</b><strong>{{ memory(status?.mosdns?.memory_rss_bytes) }} / {{ memory(status?.controller_memory_rss_bytes) }}</strong><small>mosdns / Controller</small></div>
        <div><b>审计链路</b><strong>{{ status?.audit?.dropped_events ?? '-' }}</strong><small>累计丢弃事件</small></div>
      </div>

      <div class="metric-grid overview-metrics">
        <article><span>近 24 小时查询</span><strong>{{ summary.query_count.toLocaleString() }}</strong><small>最近 1 小时 {{ summary.last_hour_query_count.toLocaleString() }} 次</small></article>
        <article><span>缓存命中率</span><strong>{{ percent(summary.cache_hit_count) }}</strong><small>{{ summary.cache_hit_count.toLocaleString() }} 次命中</small></article>
        <article><span>处理错误率</span><strong>{{ percent(summary.processing_error_count) }}</strong><small>{{ summary.processing_error_count.toLocaleString() }} 次，负向应答不计错误</small></article>
        <article><span>P95 延迟</span><strong>{{ formatLatencyMs(summary.p95_latency_us) }}</strong><small>{{ summary.p95_sample_count < summary.query_count ? '采集期样本' : '完整窗口' }}，平均 {{ formatLatencyMs(summary.average_latency_us) }}</small></article>
      </div>

      <div class="dashboard-grid dashboard-primary">
        <section class="chart-panel wide"><header><div><h2>查询与延迟趋势</h2><p>最近 24 小时，按小时聚合</p></div></header><div ref="trendElement" class="chart-canvas"></div></section>
        <section class="chart-panel"><header><div><h2>上游组分布</h2><p>最近 24 小时的上游组查询占比</p></div></header><div ref="distributionElement" class="chart-canvas"></div></section>
      </div>

      <div class="dashboard-grid dashboard-secondary">
        <section class="data-panel"><header><h2>Top 域名</h2><span>24 小时</span></header><ol class="rank-list"><li v-for="item in domains" :key="String(item.value)"><span>{{ item.value }}</span><b>{{ item.query_count.toLocaleString() }}</b></li><li v-if="!domains.length" class="muted">暂无查询数据</li></ol></section>
        <section class="data-panel"><header><h2>Top 客户端</h2><span>24 小时</span></header><ol class="rank-list"><li v-for="item in clients" :key="String(item.value)"><span>{{ item.value }}</span><b>{{ item.query_count.toLocaleString() }}</b></li><li v-if="!clients.length" class="muted">暂无查询数据</li></ol></section>
        <section class="data-panel"><header><h2>结果分类</h2><span>24 小时</span></header><dl class="compact-stats"><div><dt>成功</dt><dd>{{ percent(summary.success_count) }}</dd></div><div><dt>负向应答</dt><dd>{{ percent(summary.negative_answer_count) }}</dd></div><div><dt>策略拦截</dt><dd>{{ percent(summary.policy_block_count) }}</dd></div><div><dt>处理错误</dt><dd>{{ percent(summary.processing_error_count) }}</dd></div></dl></section>
        <section class="data-panel"><header><h2>审计与存储</h2><span>运行时</span></header><dl class="compact-stats"><div><dt>审计队列</dt><dd>{{ status?.audit ? `${status.audit.queue_depth} / ${status.audit.queue_capacity}` : '-' }}</dd></div><div><dt>接收队列</dt><dd>{{ status?.ingest_queue_depth ?? '-' }}</dd></div><div><dt>审计丢弃</dt><dd>{{ status?.audit?.dropped_events ?? '-' }}</dd></div><div><dt>数据库</dt><dd>{{ status ? `${(status.database.bytes / 1024 / 1024).toFixed(1)} MiB` : '-' }}</dd></div></dl></section>
      </div>
    </NSpin>
  </section>
</template>
