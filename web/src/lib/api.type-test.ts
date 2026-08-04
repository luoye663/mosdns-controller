import { api, type QueryEvent, type Settings, type SubscriptionBindingInput, type UpstreamGroup, type UpstreamGroupWrite, type VersionPrecondition } from './api'

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends (<T>() => T extends B ? 1 : 2) ? true : false
type Assert<T extends true> = T

type _GroupHasNoVersionPrecondition = Assert<Equal<Extract<keyof UpstreamGroup, 'expected_current_version'>, never>>
type _CreateInput = Assert<Equal<Parameters<typeof api.createUpstreamGroup>[0], UpstreamGroupWrite>>
type _DeleteInput = Assert<Equal<Parameters<typeof api.deleteUpstreamGroup>[1], VersionPrecondition>>
type _SettingsRegistryVersion = Assert<Equal<Settings['upstream_registry_version'], number>>
type _RegistrySchemaVersion = Assert<Equal<Awaited<ReturnType<typeof api.upstreamGroups>>['schema_version'], 1>>
type _BindingInput = Assert<Equal<Parameters<typeof api.updateRuleSubscriptionBinding>[1], SubscriptionBindingInput>>
type _QueryBindingID = Assert<Equal<QueryEvent['subscription_binding_id'], number>>

export {}
