import type { ApiRecord } from '../types'

export function resolveOidcSettings(settings: ApiRecord): ApiRecord {
  const value = settings.oidc ?? settings['auth.oidc']
  return value && typeof value === 'object' && !Array.isArray(value) ? value as ApiRecord : {}
}
