import type { ApiRecord } from '../types'
import { asNumber, asText, pick } from './format'

export type SortKind = 'text' | 'number' | 'date' | 'boolean'

// Column sorting for every list screen. The backend list endpoints page by
// page_size and accept no sort parameter, so sorting is applied to the rows on
// screen - the current page - which is what a table header sort is expected to
// do here. Missing values always sort last regardless of direction.
function rank(value: unknown, kind: SortKind): number | string | null {
  if (value === null || value === undefined || value === '') return null
  if (kind === 'number') { const n = asNumber(value, Number.NaN); return Number.isFinite(n) ? n : null }
  if (kind === 'date') { const t = new Date(String(value)).getTime(); return Number.isFinite(t) ? t : null }
  if (kind === 'boolean') return value === true || value === 'true' ? 1 : 0
  return asText(value, '').toLowerCase()
}

export type SortOrder = 'ascend' | 'descend' | null | undefined

// antd negates the comparator for a descending sort, so "missing last" has to
// be expressed relative to the requested order: for 'descend' a missing value
// is reported as smaller so that the negation still places it last.
export function compareValues(left: unknown, right: unknown, kind: SortKind = 'text', order: SortOrder = 'ascend'): number {
  const a = rank(left, kind)
  const b = rank(right, kind)
  const last = order === 'descend' ? -1 : 1
  if (a === null && b === null) return 0
  if (a === null) return last
  if (b === null) return -last
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), 'ko-KR', { numeric: true, sensitivity: 'base' })
}

export function sorterFor(keys: string[], kind: SortKind = 'text') {
  return (left: ApiRecord, right: ApiRecord, order?: SortOrder) => compareValues(pick(left, ...keys), pick(right, ...keys), kind, order)
}

// For columns whose cell value is derived rather than read from a key.
export function sorterBy<T>(getter: (row: T) => unknown, kind: SortKind = 'text') {
  return (left: T, right: T, order?: SortOrder) => compareValues(getter(left), getter(right), kind, order)
}

export function sortKindForFormat(format?: string): SortKind {
  if (format === 'number' || format === 'percent' || format === 'bytes') return 'number'
  if (format === 'date') return 'date'
  if (format === 'boolean') return 'boolean'
  return 'text'
}
