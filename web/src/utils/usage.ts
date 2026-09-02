import type { ApiRecord } from '../types'
import { asNumber, asText } from './format'
import type { DashboardFilters } from '../hooks/useLiveDashboard'

function rows(value: unknown): ApiRecord[] {
  return Array.isArray(value) ? value.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object') : []
}

export function usageQuery(filters: DashboardFilters) {
  const to = new Date()
  const days = filters.range === 'day' ? 1 : filters.range === 'week' ? 7 : 30
  const from = new Date(to.getTime() - days * 24 * 60 * 60 * 1000)
  const selectedDimension = (['project', 'department', 'hub', 'network'] as const).find((key) => Boolean(filters[key]))
  const params = new URLSearchParams({
    from: from.toISOString(),
    to: to.toISOString(),
    granularity: filters.range === 'day' ? 'hour' : 'day',
    group_by: selectedDimension || 'user',
  })
  return { path: `/usage?${params.toString()}`, selectedDimension }
}

export function buildUsageOverlay(value: ApiRecord, filters: DashboardFilters): ApiRecord {
  const { selectedDimension } = usageQuery(filters)
  const selectedValue = selectedDimension ? filters[selectedDimension] : undefined
  const buckets = new Map<string, ApiRecord>()
  for (const row of rows(value.trend)) {
    if (selectedValue && asText(row.group, '') !== selectedValue) continue
    const bucket = asText(row.bucket, '')
    if (!bucket) continue
    const current = buckets.get(bucket) || { timestamp: bucket, label: bucket }
    const metric = asText(row.metric, '')
    const amount = asNumber(row.average)
    if (metric === 'login_count') current.login_count = asNumber(current.login_count) + amount
    if (metric === 'server_starts') current.server_starts = asNumber(current.server_starts) + amount
    if (['cpu', 'cpu_cores', 'cpu_usage'].includes(metric)) current.cpu_cores = asNumber(current.cpu_cores) + amount
    buckets.set(bucket, current)
  }
  return {
    usage_stats: value,
    usage_trend: [...buckets.values()].sort((a, b) => asText(a.timestamp).localeCompare(asText(b.timestamp))),
    top_users: rows(value.top_users),
    usage_stale: Boolean(value.stale),
  }
}
