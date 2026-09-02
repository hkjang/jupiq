import type { ApiRecord } from '../types'
import { asNumber, asText, formatBytes, formatPercent, pick } from './format'

export interface DashboardDimensionFilters {
  network?: string
  hub?: string
  department?: string
  project?: string
}

export function filterLiveUsers(rows: ApiRecord[], filters: DashboardDimensionFilters) {
  return rows.filter((row) => {
    const matches = (selected: string | undefined, ...keys: string[]) => !selected || String(pick(row, ...keys) || '') === selected
    return matches(filters.network, 'network', 'network_name')
      && matches(filters.hub, 'hub', 'hub_name')
      && matches(filters.department, 'department', 'department_name')
      && matches(filters.project, 'project', 'project_name')
  })
}

export function summarizeLiveUsers(rows: ApiRecord[]): ApiRecord {
  const usernames = rows.map((row) => asText(pick(row, 'username', 'user_name'), '')).filter(Boolean)
  return {
    active_users: new Set(usernames).size,
    running_servers: rows.length,
    cpu_cores: rows.reduce((sum, row) => sum + asNumber(pick(row, 'cpu_cores', 'cpu_usage', 'cpu')), 0),
    memory_bytes: rows.reduce((sum, row) => sum + asNumber(pick(row, 'memory_bytes', 'memory_usage', 'memory')), 0),
    gpu_count: rows.reduce((sum, row) => sum + asNumber(pick(row, 'gpu_count', 'gpu')), 0),
    vram_bytes: rows.reduce((sum, row) => sum + asNumber(pick(row, 'vram_bytes', 'vram')), 0),
    idle_sessions: rows.filter((row) => row.idle_candidate === true).length,
    long_running_sessions: rows.filter((row) => asNumber(row.runtime_seconds) >= 86400).length,
  }
}

export function formatCpuResource(row: ApiRecord) {
  const percent = pick(row, 'cpu_percent', 'cpu_utilization')
  if (percent !== undefined) return formatPercent(percent)
  const cores = pick(row, 'cpu_cores', 'cpu_usage', 'cpu')
  return cores === undefined ? '—' : `${asNumber(cores).toLocaleString('ko-KR', { maximumFractionDigits: 2 })} Core`
}

export function formatMemoryResource(row: ApiRecord) {
  const percent = pick(row, 'memory_percent', 'memory_utilization')
  if (percent !== undefined) return formatPercent(percent)
  const bytes = pick(row, 'memory_bytes', 'memory_usage', 'memory')
  return bytes === undefined ? '—' : formatBytes(bytes)
}
