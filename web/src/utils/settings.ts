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

// MCP SSO(OAuth) 설정 문서의 키. 서버의 auth.MCPOAuthSettingKey와 같다.
export const MCP_OAUTH_KEY = 'mcp.oauth'

export const DEFAULT_MCP_OAUTH_SCOPES = ['mcp:use', 'dashboard:read', 'hubs:read', 'servers:read', 'usage:read']

// 리소스 식별자: 관리자가 적은 값이 있으면 그것, 없으면 현재 화면의 오리진 + /mcp.
// 서버가 요청 Host로 만드는 마지막 수단과 같은 모양이다.
export function mcpResourceFor(configured: string, origin: string): string {
  const value = configured.trim()
  if (value) return value
  return `${origin.replace(/\/+$/, '')}/mcp`
}

// RFC 9728의 경로 삽입 규칙: origin + /.well-known/oauth-protected-resource + 리소스 경로.
// 서버의 auth.MCPResourceMetadataURL과 같은 값을 낸다.
export function mcpMetadataUrlFor(resource: string): string {
  try {
    const url = new URL(resource)
    if (!url.host) return ''
    return `${url.protocol}//${url.host}/.well-known/oauth-protected-resource${url.pathname}`
  } catch {
    return ''
  }
}
