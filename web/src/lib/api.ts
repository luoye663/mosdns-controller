export interface ApiError extends Error { code?: string; status?: number }
interface Envelope<T> { data: T }

export interface Rule { id: number; category: string; action: string; match_type: string; pattern: string; priority: number; source: string; comment: string; enabled: boolean; updated_at_ms: number }
export type RuleInput = Omit<Rule, 'id' | 'updated_at_ms'>
export interface Version { version: number; checksum: string; status: string; rule_count: number; created_at_ms: number; error_code?: string }
export interface QueryEvent { id: number; event_id: string; timestamp_unix_ms: number; client_ip: string; device_name: string; protocol: string; qname: string; qtype: number; qclass: number; rcode: number; route: string; route_source: string; upstream_group: string; upstream_tag: string; cache_hit: boolean; snapshot_version: number; access_rule_id: number; route_rule_id: number; subscription_source_id: number; subscription_source_name: string; subscription_categories: string[]; answer_count: number; answer_min_ttl_seconds: number | null; latency_us: number; error_code: string; error_text: string }
export interface AnswerDiagnostics { answer_ips: string[]; answer_records: string[] }
export type QueryParams = Record<string, string | number | boolean | undefined>
export interface Device { id: number; ip: string; mac: string; hostname: string; display_name: string; note: string; source: string; first_seen_at_ms: number; last_seen_at_ms: number; query_count_24h: number }
export interface AuditLog { id: number; admin_username: string; action: string; resource_type: string; resource_id: string; result: string; error_code: string; created_at_ms: number }
export interface Upstream { tag: string; addr: string; priority: number; weight: number }
export interface UpstreamSnapshot { version: number; expected_current_version: number; mode: 'race' | 'weighted' | 'failover'; concurrent: number; socks5?: string; upstreams: Upstream[]; checksum?: string }
export interface ECSSnapshot { version: number; expected_current_version: number; mode: 'off' | 'client_subnet' | 'fixed_subnet'; mask4: number; mask6: number; preset4?: string; preset6?: string }
export interface Settings { cache_enabled: boolean; cache_ttl: number; query_retention_days: number }
export interface RuleSubscription { id: number; category: string; action: string; kind: 'url' | 'upload'; name: string; source_url: string; refresh_interval_seconds: number; enabled: boolean; rule_count: number; last_checked_at_ms: number; last_success_at_ms: number; last_error: string; created_at_ms: number; updated_at_ms: number }
export interface RuleSubscriptionInput { category: string; action: string; name: string; source_url: string; refresh_interval_seconds: number; enabled: boolean }
export interface DashboardSummary { query_count: number; last_hour_query_count: number; average_latency_us: number; p95_latency_us: number; p95_sample_count: number; max_latency_us: number; error_count: number; cache_hit_count: number }
export interface LatencyPoint { hour_start_ms: number; query_count: number; average_latency_us: number; max_latency_us: number }
export interface SystemStatus { controller: Record<string, string>; controller_memory_rss_bytes: number; database: { bytes: number; wal_bytes: number }; mosdns?: { state: string; snapshot_version: number; checksum: string; memory_rss_bytes: number }; mosdns_error?: string; audit?: { queue_depth: number; queue_capacity: number; dropped_events: number }; audit_error?: string; ingest_queue_depth: number; last_successful_ingest_at?: string; last_retention_at?: string }

// CSRF token 只保留在当前浏览器会话，刷新后仍可继续操作已有服务端 session。
let csrfToken = sessionStorage.getItem('mosdns_csrf') ?? ''
export function setCSRF(token: string) { csrfToken = token; sessionStorage.setItem('mosdns_csrf', token) }
export function clearCSRF() { csrfToken = ''; sessionStorage.removeItem('mosdns_csrf') }
let csrfRefresh: Promise<void> | undefined

export function refreshCSRF() {
  if (!csrfRefresh) {
    csrfRefresh = request<{ csrf_token: string }>('/auth/csrf').then((result) => setCSRF(result.csrf_token)).finally(() => { csrfRefresh = undefined })
  }
  return csrfRefresh
}

async function request<T>(path: string, init: RequestInit = {}, retryCSRF = true): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  if (csrfToken && !['GET', 'HEAD'].includes(init.method ?? 'GET')) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  const body = await response.json().catch(() => ({})) as { data?: T; error?: { code?: string; message?: string } }
  if (!response.ok) {
    const error = new Error(body.error?.message ?? '请求失败') as ApiError
    error.code = body.error?.code
    error.status = response.status
    if (retryCSRF && response.status === 403 && error.code === 'FORBIDDEN' && !['GET', 'HEAD'].includes(init.method ?? 'GET')) {
      await refreshCSRF()
      return request<T>(path, init, false)
    }
    throw error
  }
  return body.data as T
}

export const api = {
  login: (username: string, password: string) => request<{ csrf_token: string }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  bootstrapStatus: () => request<{ required: boolean }>('/auth/bootstrap'),
  bootstrap: (username: string, password: string) => request<{ csrf_token: string }>('/auth/bootstrap', { method: 'POST', body: JSON.stringify({ username, password }) }),
  me: () => request<{ id: number; username: string }>('/auth/me'),
  csrf: () => refreshCSRF(),
  logout: () => request<{ logged_out: boolean }>('/auth/logout', { method: 'POST' }),
  changePassword: (currentPassword: string, newPassword: string) => request<{ changed: boolean }>('/auth/change-password', { method: 'POST', body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }) }),
  summary: () => request<DashboardSummary>('/stats/summary'),
  statistic: (kind: 'domains' | 'clients' | 'routes' | 'rcode') => request<{ items: Array<{ value: string | number; query_count: number }> }>(kind === 'domains' ? '/stats/top-domains' : kind === 'clients' ? '/stats/top-clients' : `/stats/${kind}`),
  latency: () => request<{ items: LatencyPoint[] }>('/stats/latency'),
  queries: (params: QueryParams) => request<{ items: QueryEvent[]; next_cursor?: string }>(`/queries?${new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== '') as Array<[string, string]>).toString()}`),
  answerDiagnostics: (eventID: string) => request<AnswerDiagnostics>(`/queries/${encodeURIComponent(eventID)}/answer-ips`),
  rules: () => request<{ items: Rule[] }>('/rules'),
  createRule: (rule: RuleInput) => request<Version>('/rules', { method: 'POST', body: JSON.stringify(rule) }),
  updateRule: (id: number, rule: Rule) => request<Version>(`/rules/${id}`, { method: 'PATCH', body: JSON.stringify(rule) }),
  deleteRule: (id: number) => request<Version>(`/rules/${id}`, { method: 'DELETE' }),
  previewRuleImport: (rules: RuleInput[]) => request<{ rules: RuleInput[]; count: number }>('/rules/import/preview', { method: 'POST', body: JSON.stringify({ rules }) }),
  importRules: (rules: RuleInput[]) => request<Version>('/rules/import/apply', { method: 'POST', body: JSON.stringify({ rules }) }),
  ruleTest: (qname: string) => request<unknown>('/rules/test', { method: 'POST', body: JSON.stringify({ qname }) }),
  ruleSubscriptions: (category?: string, action?: string) => request<{ items: RuleSubscription[] }>(`/rule-subscriptions?${new URLSearchParams(Object.entries({ category, action }).filter(([, value]) => value) as Array<[string, string]>).toString()}`),
  createRuleSubscription: (input: RuleSubscriptionInput) => request<{ subscription: RuleSubscription; version: Version }>('/rule-subscriptions', { method: 'POST', body: JSON.stringify(input) }),
  uploadRuleSubscription: (input: Omit<RuleSubscriptionInput, 'source_url'>, file: File) => { const body = new FormData(); body.append('category', input.category); body.append('action', input.action); body.append('name', input.name); body.append('refresh_interval_seconds', String(input.refresh_interval_seconds)); body.append('enabled', String(input.enabled)); body.append('file', file); return request<{ subscription: RuleSubscription; version: Version }>('/rule-subscriptions/upload', { method: 'POST', body }) },
  updateRuleSubscription: (id: number, enabled: boolean) => request<{ subscription: RuleSubscription; version: Version }>(`/rule-subscriptions/${id}`, { method: 'PATCH', body: JSON.stringify({ enabled }) }),
  refreshRuleSubscription: (id: number) => request<{ subscription: RuleSubscription; version?: Version }>(`/rule-subscriptions/${id}/refresh`, { method: 'POST', body: '{}' }),
  deleteRuleSubscription: (id: number) => request<Version>(`/rule-subscriptions/${id}`, { method: 'DELETE' }),
  versions: () => request<{ items: Version[] }>('/rule-versions'),
  rollback: (version: number) => request<Version>(`/rule-versions/${version}/rollback`, { method: 'POST', body: '{}' }),
  reconcile: () => request<{ state: string }>('/rule-versions/reconcile', { method: 'POST', body: '{}' }),
  devices: () => request<{ items: Device[] }>('/devices'),
  updateDevice: (id: number, patch: { display_name?: string; note?: string }) => request<Device>(`/devices/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
  systemStatus: () => request<SystemStatus>('/system/status'),
  flushCaches: () => request<{ flushed: boolean }>('/system/cache/flush', { method: 'POST', body: '{}' }),
  upstreams: () => request<{ local: UpstreamSnapshot; remote: UpstreamSnapshot; local_ecs: ECSSnapshot; remote_ecs: ECSSnapshot }>('/upstreams'),
  updateUpstream: (group: 'local_dns' | 'remote_dns', snapshot: UpstreamSnapshot) => request<UpstreamSnapshot>(`/upstreams/${group}`, { method: 'PUT', body: JSON.stringify(snapshot) }),
  updateECS: (group: 'local_dns' | 'remote_dns', snapshot: ECSSnapshot) => request<ECSSnapshot>(`/upstreams/${group}/ecs`, { method: 'PUT', body: JSON.stringify(snapshot) }),
  settings: () => request<Settings>('/settings'),
  updateSettings: (settings: Settings) => request<Settings>('/settings', { method: 'PUT', body: JSON.stringify(settings) }),
  auditLogs: () => request<{ items: AuditLog[] }>('/audit-logs'),
}

export function eventStream(params: QueryParams = {}) { const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== '') as Array<[string, string]>); return new EventSource(`/api/v1/queries/stream?${query.toString()}`, { withCredentials: true }) }
