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

export function isFreshLiveSession(row: ApiRecord) {
  return row.stale !== true
}

export function hasStaleLiveSessions(value: unknown) {
  return Array.isArray(value) && value.some((row) => Boolean(row) && typeof row === 'object' && !Array.isArray(row)
    && ((row as ApiRecord).stale === true || (row as ApiRecord).resource_stale === true))
}

export function summarizeLiveUsers(rows: ApiRecord[]): ApiRecord {
  const freshRows = rows.filter(isFreshLiveSession)
  const userKeys = freshRows.map((row) => {
    const username = asText(pick(row, 'username', 'user_name'), '')
    if (!username) return ''
    const hub = asText(pick(row, 'hub_id', 'hub', 'hub_name'), '')
    return hub ? `${hub}\u0000${username}` : username
  }).filter(Boolean)
  const cpuRows = freshRows.filter((row) => pick(row, 'cpu_cores', 'cpu_usage', 'cpu') !== undefined && pick(row, 'cpu_cores', 'cpu_usage', 'cpu') !== null)
  const memoryRows = freshRows.filter((row) => pick(row, 'memory_bytes', 'memory_usage', 'memory') !== undefined && pick(row, 'memory_bytes', 'memory_usage', 'memory') !== null)
  const gpuRows = freshRows.filter((row) => pick(row, 'gpu_count', 'gpu') !== undefined && pick(row, 'gpu_count', 'gpu') !== null)
  const vramRows = freshRows.filter((row) => pick(row, 'vram_bytes', 'vram') !== undefined && pick(row, 'vram_bytes', 'vram') !== null)
  return {
    active_users: new Set(userKeys).size,
    running_servers: freshRows.length,
    ...(cpuRows.length ? { cpu_cores: cpuRows.reduce((sum, row) => sum + asNumber(pick(row, 'cpu_cores', 'cpu_usage', 'cpu')), 0) } : {}),
    ...(memoryRows.length ? { memory_bytes: memoryRows.reduce((sum, row) => sum + asNumber(pick(row, 'memory_bytes', 'memory_usage', 'memory')), 0) } : {}),
    ...(gpuRows.length ? { gpu_count: gpuRows.reduce((sum, row) => sum + asNumber(pick(row, 'gpu_count', 'gpu')), 0) } : {}),
    ...(vramRows.length ? { vram_bytes: vramRows.reduce((sum, row) => sum + asNumber(pick(row, 'vram_bytes', 'vram')), 0) } : {}),
    idle_sessions: freshRows.filter((row) => row.idle_candidate === true).length,
    long_running_sessions: freshRows.filter((row) => asNumber(row.runtime_seconds) >= 86400).length,
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

export function formatGpuResource(row: ApiRecord) {
  const percent = pick(row, 'gpu_percent', 'gpu_utilization')
  if (percent !== undefined) return formatPercent(percent)
  const count = pick(row, 'gpu_count', 'gpu')
  return count === undefined ? '—' : `${asNumber(count).toLocaleString('ko-KR', { maximumFractionDigits: 2 })}장`
}

export function formatVramResource(row: ApiRecord) {
  const percent = pick(row, 'vram_percent', 'vram_utilization')
  if (percent !== undefined) return formatPercent(percent)
  const bytes = pick(row, 'vram_bytes', 'vram')
  return bytes === undefined ? '—' : formatBytes(bytes)
}
