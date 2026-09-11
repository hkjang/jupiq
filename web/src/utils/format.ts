import type { ApiRecord } from '../types'

export function asText(value: unknown, fallback = '—'): string {
  if (value === null || value === undefined || value === '') return fallback
  if (typeof value === 'boolean') return value ? '사용' : '사용 안 함'
  if (Array.isArray(value)) return value.map((item) => asText(item, '')).filter(Boolean).join(', ') || fallback
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

export function asNumber(value: unknown, fallback = 0): number {
  const number = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(number) ? number : fallback
}

export function pick(record: ApiRecord, ...keys: string[]): unknown {
  for (const key of keys) {
    if (record[key] !== null && record[key] !== undefined && record[key] !== '') return record[key]
  }
  return undefined
}

export function formatDate(value: unknown): string {
  if (!value) return '—'
  const date = new Date(String(value))
  if (Number.isNaN(date.getTime())) return asText(value)
  return new Intl.DateTimeFormat('ko-KR', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

export function formatPercent(value: unknown): string {
  const number = asNumber(value)
  return `${number.toLocaleString('ko-KR', { maximumFractionDigits: 1 })}%`
}

// KPI cards hand numbers straight to antd Statistic, which prints every digit
// a summed float carries (0.004166666666667 Core). Two fraction digits cover
// ordinary values; a value that would round away to 0 keeps two significant
// digits instead so a small-but-real reading still shows as 0.0042 rather than 0.
export function formatMetricNumber(value: unknown): string {
  const number = asNumber(value, Number.NaN)
  if (!Number.isFinite(number)) return asText(value)
  const rounded = number.toLocaleString('ko-KR', { maximumFractionDigits: 2 })
  if (number !== 0 && Number(rounded.replace(/,/g, '')) === 0) {
    return number.toLocaleString('ko-KR', { maximumSignificantDigits: 2 })
  }
  return rounded
}

// Consumption is a quantity, not a rate: core-hours and GB-hours answer how
// much was used, where an average only says how hard the pods worked while they
// happened to be running.
export function formatCoreHours(value: unknown): string {
  const hours = asNumber(value, Number.NaN)
  if (!Number.isFinite(hours)) return '수집 불가'
  return `${hours.toLocaleString('ko-KR', { maximumFractionDigits: hours < 10 ? 2 : 0 })} Core·h`
}

export function formatGbHours(value: unknown): string {
  const hours = asNumber(value, Number.NaN)
  if (!Number.isFinite(hours)) return '수집 불가'
  return `${hours.toLocaleString('ko-KR', { maximumFractionDigits: hours < 10 ? 2 : 0 })} GB·h`
}

// A bucket built while collection was down describes less than it appears to,
// so the share of runtime that was actually sampled is shown next to the total.
export function formatObservedRatio(value: unknown): string {
  const ratio = asNumber(value, Number.NaN)
  if (!Number.isFinite(ratio)) return '—'
  return `${(ratio * 100).toLocaleString('ko-KR', { maximumFractionDigits: 0 })}%`
}

export function normalizePercentValue(value: unknown): number {
  const number = asNumber(value)
  return Math.abs(number) <= 1 ? number * 100 : number
}

export function formatBytes(value: unknown): string {
  const bytes = asNumber(value)
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / 1024 ** exponent).toLocaleString('ko-KR', { maximumFractionDigits: 1 })} ${units[exponent]}`
}

export function formatDuration(value: unknown): string {
  const seconds = asNumber(value)
  if (seconds <= 0) return '—'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return [days ? `${days}일` : '', hours ? `${hours}시간` : '', minutes || (!days && !hours) ? `${minutes}분` : ''].filter(Boolean).join(' ')
}

export function statusTone(value: unknown): 'success' | 'processing' | 'warning' | 'error' | 'default' {
  const status = String(value || '').toLowerCase()
  if (/(running|active|normal|healthy|approved|success|online|사용)/.test(status)) return 'success'
  if (/(pending|starting|sync|processing|review|대기)/.test(status)) return 'processing'
  if (/(warning|degraded|idle|주의)/.test(status)) return 'warning'
  if (/(error|failed|critical|stopped|rejected|offline|장애|실패)/.test(status)) return 'error'
  return 'default'
}
