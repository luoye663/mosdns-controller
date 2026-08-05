import { api, type QueryEvent, type Settings, type SubscriptionBindingInput, type UpstreamGroup, type UpstreamGroupWrite, type UpstreamProtection, type VersionPrecondition } from './api'

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends (<T>() => T extends B ? 1 : 2) ? true : false
type Assert<T extends true> = T

type _GroupHasNoVersionPrecondition = Assert<Equal<Extract<keyof UpstreamGroup, 'expected_current_version'>, never>>
type _CreateInput = Assert<Equal<Parameters<typeof api.createUpstreamGroup>[0], UpstreamGroupWrite>>
type _DeleteInput = Assert<Equal<Parameters<typeof api.deleteUpstreamGroup>[1], VersionPrecondition>>
type _SettingsRegistryVersion = Assert<Equal<Settings['upstream_registry_version'], number>>
type _RegistrySchemaVersion = Assert<Equal<Awaited<ReturnType<typeof api.upstreamGroups>>['schema_version'], 2>>
type _BindingInput = Assert<Equal<Parameters<typeof api.updateRuleSubscriptionBinding>[1], SubscriptionBindingInput>>
type _QueryBindingID = Assert<Equal<QueryEvent['subscription_binding_id'], number>>
type _UpstreamTimeout = Assert<Equal<UpstreamGroup['upstreams'][number]['timeout_ms'], number>>
type _GroupMaxInFlight = Assert<Equal<UpstreamGroup['max_in_flight'], number | null | undefined>>
type _GroupQueryTimeout = Assert<Equal<UpstreamGroup['query_timeout_ms'], number | null | undefined>>
type _GroupBootstrap = Assert<Equal<UpstreamGroup['bootstrap'], string | undefined>>
type _GroupBootstrapVersion = Assert<Equal<UpstreamGroup['bootstrap_version'], 4 | 6>>
type _RegistryProtection = Assert<Equal<Awaited<ReturnType<typeof api.upstreamGroups>>['protection'], UpstreamProtection>>
type _OverloadAction = Assert<Equal<Settings['overload_action'], 'servfail' | 'refused' | 'drop'>>

export {}
