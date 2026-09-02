import type { ApiRecord } from '../types'

export type UsagePeriod = 'day' | 'week' | 'month'

export interface UserDetailView {
  username: string
  user: ApiRecord | null
  hubs: ApiRecord[]
  currentServers: ApiRecord[]
  serverHistory: ApiRecord[]
  serverMeta: ApiRecord
  timeline: ApiRecord[]
  timelineMeta: ApiRecord
  usage: Record<UsagePeriod, ApiRecord>
  llmUsage: ApiRecord
  featureEnabled: ApiRecord
  dataPolicy: ApiRecord
}

export function record(value: unknown): ApiRecord {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as ApiRecord : {}
}

export function records(value: unknown): ApiRecord[] {
  return Array.isArray(value)
    ? value.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object' && !Array.isArray(item))
    : []
}

export function normalizeUserDetail(value: unknown): UserDetailView {
  const root = record(value)
  const servers = record(root.servers)
  const timeline = record(root.timeline)
  const usage = record(root.usage)
  const userValue = root.user

  return {
    username: String(root.username || record(userValue).username || ''),
    user: userValue && typeof userValue === 'object' && !Array.isArray(userValue) ? userValue as ApiRecord : null,
    hubs: records(root.hubs),
    currentServers: records(servers.current || root.current_servers),
    serverHistory: records(servers.history || root.server_history),
    serverMeta: record(servers.meta),
    timeline: records(timeline.items || root.events),
    timelineMeta: record(timeline.meta),
    usage: {
      day: record(usage.day || root.daily_usage),
      week: record(usage.week || root.weekly_usage),
      month: record(usage.month || root.monthly_usage),
    },
    llmUsage: record(root.llm_usage || root.llm),
    featureEnabled: record(root.feature_enabled),
    dataPolicy: record(root.data_policy),
  }
}

export function periodSummary(value: ApiRecord): ApiRecord {
  return { ...value, ...record(value.summary) }
}
