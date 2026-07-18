<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { NButton } from 'naive-ui'
import { api, type Version } from '@/lib/api'
import { notify } from '@/lib/notify'
const versions = ref<Version[]>([]); const loading = ref(false)
function time(ms: number) { return new Date(ms).toLocaleString() }
async function load() { loading.value = true; try { versions.value = (await api.versions()).items ?? [] } catch (e) { notify.error(e instanceof Error ? e.message : '无法加载版本') } finally { loading.value = false } }
async function rollback(version: Version) { if (!window.confirm(`确认将版本 ${version.version} 的规则重新发布为新版本？`)) return; loading.value = true; try { const result = await api.rollback(version.version); notify.success(`已创建并发布版本 ${result.version}`); await load() } catch (e) { notify.error(e instanceof Error ? e.message : '回滚失败') } finally { loading.value = false } }
async function reconcile() { loading.value = true; try { notify.success(`运行时状态：${(await api.reconcile()).state}`, '对账完成') } catch (e) { notify.error(e instanceof Error ? e.message : '对账失败') } finally { loading.value = false } }
onMounted(load)
</script>
<template><section class="page"><header class="page-heading"><div><p class="eyebrow">发布历史</p><h1>规则版本</h1></div><NButton @click="reconcile" :loading="loading">运行时对账</NButton></header><div class="table-wrap"><table><thead><tr><th>版本</th><th>状态</th><th>规则数</th><th>校验和</th><th>创建时间</th><th>失败原因</th><th></th></tr></thead><tbody><tr v-for="version in versions" :key="version.version"><td>{{ version.version }}</td><td><span class="status" :class="version.status">{{ version.status }}</span></td><td>{{ version.rule_count }}</td><td class="mono checksum">{{ version.checksum }}</td><td>{{ time(version.created_at_ms) }}</td><td>{{ version.error_code || '-' }}</td><td><NButton size="small" :disabled="version.status === 'active'" @click="rollback(version)">回滚为新版本</NButton></td></tr><tr v-if="!loading && !versions.length"><td colspan="7" class="empty-cell">尚未发布规则版本</td></tr></tbody></table></div></section></template>
