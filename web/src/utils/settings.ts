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

// 메일 발송 기록의 이벤트 이름을 화면 표기로 바꾼다. 서버의 mail 패키지가 정한
// 이벤트 이름과 짝이며, 모르는 이름은 그대로 보여 준다.
const mailEventLabels: Record<string, string> = {
  'approval.requested': '검토·승인 요청',
  'approval.decided': '승인 결과',
  'hub.health': 'Hub 상태',
  'key.expiring': 'API 키 만료 임박',
  test: '시험 발송',
}

export function mailEventLabel(event: string): string {
  return mailEventLabels[event] || event
}

// 발송 기록의 상태별 집계를 배지 순서(성공·실패·대기)로 편다. 0건인 상태는
// 배지를 만들지 않아 조용한 설치에서 빈 배지가 늘어서지 않는다.
export function mailSummaryBadges(summary: Record<string, number> | undefined): { key: string; label: string; count: number }[] {
  const order: [string, string][] = [['sent', '성공'], ['failed', '실패'], ['queued', '대기']]
  return order.filter(([key]) => (summary?.[key] || 0) > 0).map(([key, label]) => ({ key, label, count: summary?.[key] || 0 }))
}
