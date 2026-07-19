<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton, NTabPane, NTabs } from 'naive-ui'
import { api, type AuditLog } from '@/lib/api'
import { notify } from '@/lib/notify'
const status=ref<Awaited<ReturnType<typeof api.systemStatus>>|null>(null);const logs=ref<AuditLog[]>([]);const cursor=ref('');const loading=ref(false)
function bytes(value:number){return value<1024*1024?`${value} B`:`${(value/1024/1024).toFixed(1)} MiB`}function time(ms:number){return new Date(ms).toLocaleString()}
async function load(nextCursor=''){loading.value=true;try{const [nextStatus,nextLogs]=await Promise.all([nextCursor?Promise.resolve(status.value):api.systemStatus(),api.auditLogs({limit:100,cursor:nextCursor})]);status.value=nextStatus;const items=nextLogs.items??[];logs.value=nextCursor?[...logs.value,...items.filter((item)=>!logs.value.some((log)=>log.id===item.id))]:items;cursor.value=nextLogs.next_cursor??''}catch(e){notify.error(e instanceof Error?e.message:'无法读取系统状态')}finally{loading.value=false}}
async function flush(){if(!window.confirm('确认清空本地和远程 DNS 缓存？'))return;loading.value=true;try{await api.flushCaches();notify.success('本地和远程缓存已请求清空')}catch(e){notify.error(e instanceof Error?e.message:'清空缓存失败')}finally{loading.value=false}}
onMounted(load)
</script>
<template>
  <section class="page">
    <header class="page-heading"><div><p class="eyebrow">运行维护</p><h1>系统状态</h1></div><div class="inline-controls"><NButton @click="() => load()" :loading="loading">刷新</NButton><NButton type="warning" @click="flush" :loading="loading">清空缓存</NButton></div></header>
    <NTabs type="line" animated>
      <NTabPane name="status" tab="运行状态">
        <div v-if="status" class="summary-grid"><section><h2>Controller</h2><dl class="status-list"><dt>版本</dt><dd>{{ status.controller.version }}</dd><dt>数据库</dt><dd>{{ bytes(status.database.bytes) }}</dd><dt>WAL</dt><dd>{{ bytes(status.database.wal_bytes) }}</dd><dt>Ingest 队列</dt><dd>{{ status.ingest_queue_depth }}</dd></dl></section><section><h2>mosdns</h2><dl v-if="status.mosdns" class="status-list"><dt>状态</dt><dd>{{ status.mosdns.state }}</dd><dt>规则版本</dt><dd>{{ status.mosdns.snapshot_version }}</dd><dt>校验和</dt><dd class="mono">{{ status.mosdns.checksum }}</dd></dl><p v-else class="muted">{{ status.mosdns_error || '运行时状态未知' }}</p></section><section><h2>维护记录</h2><dl class="status-list"><dt>最近 ingest</dt><dd>{{ status.last_successful_ingest_at || '-' }}</dd><dt>最近清理</dt><dd>{{ status.last_retention_at || '-' }}</dd></dl></section></div>
      </NTabPane>
      <NTabPane name="operations" tab="系统操作日志">
        <div class="table-wrap"><table><thead><tr><th>时间</th><th>管理员</th><th>操作</th><th>资源</th><th>结果</th></tr></thead><tbody><tr v-for="log in logs" :key="log.id"><td>{{ time(log.created_at_ms) }}</td><td>{{ log.admin_username || '-' }}</td><td>{{ log.action }}</td><td>{{ log.resource_type }} {{ log.resource_id }}</td><td><span class="status" :class="log.result">{{ log.result }}</span></td></tr><tr v-if="!logs.length"><td colspan="5" class="empty-cell">暂无系统操作记录</td></tr></tbody></table></div><footer class="table-footer"><span>日志清理、缓存清空和配置变更等操作会记录在此处。已显示 {{ logs.length }} 条</span><NButton v-if="cursor" :loading="loading" @click="() => load(cursor)">加载下一页</NButton></footer>
      </NTabPane>
    </NTabs>
  </section>
</template>
