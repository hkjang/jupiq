import type { ApiRecord } from '../types'

export function resolveOidcSettings(settings: ApiRecord): ApiRecord {
  const value = settings.oidc ?? settings['auth.oidc']
  return value && typeof value === 'object' && !Array.isArray(value) ? value as ApiRecord : {}
}

// 방문 추적 스니펫이 정책에서 필요로 하는 출처 목록. 서버의 analytics.SplitHosts와
// 같은 구분 규칙(쉼표·공백·줄바꿈)을 쓴다.
export function splitAllowedHosts(list: string): string[] {
  return list.split(/[\s,]+/).map((host) => host.trim()).filter(Boolean)
}

// 차단된 출처 하나를 허용 목록에 더한다. 이미 있으면 목록을 바꾸지 않는다.
export function addAllowedHost(existing: string, origin: string): string {
  const normalized = origin.trim().replace(/\/+$/, '')
  if (!normalized) return existing
  if (splitAllowedHosts(existing).some((host) => host.toLowerCase() === normalized.toLowerCase())) return existing
  return existing.trim() ? `${existing.trim()}, ${normalized}` : normalized
}
