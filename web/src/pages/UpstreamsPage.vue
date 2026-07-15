<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NFormItem, NInput, NInputNumber, NSelect } from 'naive-ui'
import { api, type UpstreamSnapshot } from '@/lib/api'

const local = ref<UpstreamSnapshot | null>(null)
const remote = ref<UpstreamSnapshot | null>(null)
const loading = ref(false)
const saving = ref<'local_dns' | 'remote_dns' | ''>('')
const error = ref('')
const message = ref('')

function clone(snapshot: UpstreamSnapshot): UpstreamSnapshot { return { ...snapshot, upstreams: snapshot.upstreams.map((item) => ({ ...item })) } }
async function load() { loading.value = true; error.value = ''; try { const data = await api.upstreams(); local.value = clone(data.local); remote.value = clone(data.remote) } catch (e) { error.value = e instanceof Error ? e.message : '无法读取上游配置' } finally { loading.value = false } }
function add(snapshot: UpstreamSnapshot) { snapshot.upstreams.push({ tag: `upstream_${snapshot.upstreams.length + 1}`, addr: 'https://', priority: 100, weight: 1 }) }
function remove(snapshot: UpstreamSnapshot, index: number) { if (snapshot.upstreams.length > 1) snapshot.upstreams.splice(index, 1) }
async function save(group: 'local_dns' | 'remote_dns', snapshot: UpstreamSnapshot) { saving.value = group; error.value = ''; message.value = ''; try { const updated = await api.updateUpstream(group, { ...clone(snapshot), version: snapshot.version + 1, expected_current_version: snapshot.version }); if (group === 'local_dns') local.value = clone(updated); else remote.value = clone(updated); message.value = `${group === 'local_dns' ? '本地' : '远程'}上游已热加载，对应缓存已清空` } catch (e) { error.value = e instanceof Error ? e.message : '保存上游配置失败' } finally { saving.value = '' } }
onMounted(load)
</script>

<template>
  <section class="page"><header class="page-heading"><div><p class="eyebrow">解析路径</p><h1>上游 DNS</h1></div><NButton :loading="loading" @click="load">刷新</NButton></header>
    <NAlert v-if="message" type="success" class="form-alert">{{ message }}</NAlert><NAlert v-if="error" type="error" class="form-alert">{{ error }}</NAlert>
    <NAlert type="info" class="form-alert">竞速模式随机选择端点并采用最快有效响应；加权模式按权重决定入选概率；主备模式按优先级从小到大处理，同优先级内竞速，连接错误、超时或 SERVFAIL 才切换到下一优先级。标签仅用于识别和查询审计。</NAlert>
    <div class="upstream-grid">
      <NCard v-for="item in [{ group: 'local_dns' as const, title: '本地上游', snapshot: local }, { group: 'remote_dns' as const, title: '远程上游', snapshot: remote }]" :key="item.group" :title="item.title" size="small">
        <template v-if="item.snapshot"><NFormItem label="调度模式"><NSelect v-model:value="item.snapshot.mode" :options="[{ label: '竞速', value: 'race' }, { label: '加权选择', value: 'weighted' }, { label: '主备优先级', value: 'failover' }]" /></NFormItem><NFormItem label="并发查询"><NInputNumber v-model:value="item.snapshot.concurrent" :min="1" :max="3" /></NFormItem><NFormItem label="SOCKS5 代理（可选）"><NInput v-model:value="item.snapshot.socks5" placeholder="host:port" /></NFormItem>
          <div v-for="(upstream, index) in item.snapshot.upstreams" :key="index" class="upstream-row"><NFormItem label="标签"><NInput v-model:value="upstream.tag" /></NFormItem><NFormItem label="DNS 地址"><NInput v-model:value="upstream.addr" placeholder="https://dns.example/dns-query" /></NFormItem><NFormItem v-if="item.snapshot.mode === 'failover'" label="优先级"><NInputNumber v-model:value="upstream.priority" :min="1" :max="1000" /></NFormItem><NFormItem v-if="item.snapshot.mode === 'weighted'" label="权重"><NInputNumber v-model:value="upstream.weight" :min="1" :max="100" /></NFormItem><NButton type="error" secondary :disabled="item.snapshot.upstreams.length === 1" @click="remove(item.snapshot, index)">移除</NButton></div>
          <div class="inline-controls"><NButton @click="add(item.snapshot)">添加上游</NButton><NButton type="primary" :loading="saving === item.group" @click="save(item.group, item.snapshot)">保存并热加载</NButton></div></template>
        <p v-else class="muted">{{ loading ? '正在读取配置...' : '配置不可用' }}</p>
      </NCard>
    </div>
  </section>
</template>
