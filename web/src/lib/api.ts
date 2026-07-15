export interface ApiError extends Error { code?: string; status?: number }
interface Envelope<T> { data: T }

export interface Rule { id: number; category: string; action: string; match_type: string; pattern: string; priority: number; source: string; comment: string; enabled: boolean; updated_at_ms: number }
export interface Version { version: number; checksum: string; status: string; rule_count: number; created_at_ms: number; error_code?: string }
export interface QueryEvent { id: number; event_id: string; timestamp_unix_ms: number; client_ip: string; qname: string; qtype: number; rcode: number; route: string; route_source: string; cache_hit: boolean; latency_us: number; access_rule_id: number; route_rule_id: number }
export interface Device { id: number; ip: string; mac: string; hostname: string; display_name: string; note: string; source: string; first_seen_at_ms: number; last_seen_at_ms: number; query_count_24h: number }
export interface AuditLog { id: number; admin_username: string; action: string; resource_type: string; resource_id: string; result: string; error_code: string; created_at_ms: number }
export interface Upstream { tag: string; addr: string; priority: number; weight: number }
export interface UpstreamSnapshot { version: number; expected_current_version: number; mode: 'race' | 'weighted' | 'failover'; concurrent: number; socks5?: string; upstreams: Upstream[]; checksum?: string }

// CSRF token 只保留在当前浏览器会话，刷新后仍可继续操作已有服务端 session。
let csrfToken = sessionStorage.getItem('mosdns_csrf') ?? ''
export function setCSRF(token: string) { csrfToken = token; sessionStorage.setItem('mosdns_csrf', token) }
export function clearCSRF() { csrfToken = ''; sessionStorage.removeItem('mosdns_csrf') }

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body) headers.set('Content-Type', 'application/json')
  if (csrfToken && !['GET', 'HEAD'].includes(init.method ?? 'GET')) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  const body = await response.json().catch(() => ({})) as { data?: T; error?: { code?: string; message?: string } }
  if (!response.ok) {
    const error = new Error(body.error?.message ?? '请求失败') as ApiError
    error.code = body.error?.code
    error.status = response.status
    throw error
  }
  return body.data as T
}

export const api = {
  login: (username: string, password: string) => request<{ csrf_token: string }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  me: () => request<{ id: number; username: string }>('/auth/me'),
  logout: () => request<{ logged_out: boolean }>('/auth/logout', { method: 'POST' }),
  summary: () => request<{ query_count: number; average_latency_us: number }>('/stats/summary'),
  statistic: (kind: 'domains' | 'clients' | 'routes' | 'rcode') => request<{ items: Array<{ value: string | number; query_count: number }> }>(kind === 'domains' ? '/stats/top-domains' : kind === 'clients' ? '/stats/top-clients' : `/stats/${kind}`),
  latency: () => request<{ items: Array<{ hour_start_ms: number; average_latency_us: number }> }>('/stats/latency'),
  queries: (params: Record<string, string | number | undefined>) => request<{ items: QueryEvent[]; next_cursor?: string }>(`/queries?${new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== '') as Array<[string, string]>).toString()}`),
  rules: () => request<{ items: Rule[] }>('/rules'),
  createRule: (rule: Omit<Rule, 'id' | 'updated_at_ms'>) => request<Version>('/rules', { method: 'POST', body: JSON.stringify(rule) }),
  updateRule: (id: number, rule: Rule) => request<Version>(`/rules/${id}`, { method: 'PATCH', body: JSON.stringify(rule) }),
  deleteRule: (id: number) => request<Version>(`/rules/${id}`, { method: 'DELETE' }),
  ruleTest: (qname: string) => request<unknown>('/rules/test', { method: 'POST', body: JSON.stringify({ qname }) }),
  versions: () => request<{ items: Version[] }>('/rule-versions'),
  rollback: (version: number) => request<Version>(`/rule-versions/${version}/rollback`, { method: 'POST', body: '{}' }),
  reconcile: () => request<{ state: string }>('/rule-versions/reconcile', { method: 'POST', body: '{}' }),
  devices: () => request<{ items: Device[] }>('/devices'),
  updateDevice: (id: number, patch: { display_name?: string; note?: string }) => request<Device>(`/devices/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
  systemStatus: () => request<{ controller: Record<string, string>; database: { bytes: number; wal_bytes: number }; mosdns?: { state: string; snapshot_version: number; checksum: string }; mosdns_error?: string; ingest_queue_depth: number; last_successful_ingest_at?: string; last_retention_at?: string }>('/system/status'),
  flushCaches: () => request<{ flushed: boolean }>('/system/cache/flush', { method: 'POST', body: '{}' }),
  upstreams: () => request<{ local: UpstreamSnapshot; remote: UpstreamSnapshot }>('/upstreams'),
  updateUpstream: (group: 'local_dns' | 'remote_dns', snapshot: UpstreamSnapshot) => request<UpstreamSnapshot>(`/upstreams/${group}`, { method: 'PUT', body: JSON.stringify(snapshot) }),
  auditLogs: () => request<{ items: AuditLog[] }>('/audit-logs'),
}

export function eventStream() { return new EventSource('/api/v1/queries/stream', { withCredentials: true }) }
