export type Scalar = string | number | boolean | null | undefined
export type ApiRecord = Record<string, unknown>

export interface PageMeta {
  total?: number
  page?: number
  page_size?: number
}

export interface ListResult<T> {
  data: T[]
  meta: PageMeta
}

export interface User extends ApiRecord {
  id?: string | number
  username: string
  name?: string
  display_name?: string
  email?: string
  department?: string
  roles?: string[]
  permissions?: string[]
  global_permissions?: string[]
  scoped_permissions?: string[]
  role_bindings?: ApiRecord[]
}

export interface VersionInfo extends ApiRecord {
  version: string
  commit?: string
  build_time?: string
}

export interface OidcConfig extends ApiRecord {
  enabled: boolean
  // Published by the server only while the administrator turned auto_login on.
  auto_login?: boolean
  provider_name?: string
}

export interface DashboardData extends ApiRecord {
  hubs?: unknown[]
  metrics?: ApiRecord
  usage_trend?: unknown[]
  gpu_usage?: unknown[]
  recent_incidents?: unknown[]
}

export interface ApiKey extends ApiRecord {
  id: string | number
  name?: string
  prefix?: string
  permissions?: string[]
  created_at?: string
  last_used_at?: string
  expires_at?: string
  status?: string
}
