import type { ApiRecord } from '../types'

export function hubPreflightPayload(values: ApiRecord, hubID?: unknown): ApiRecord {
  const numericHubID = Number(hubID)
  const token = typeof values.api_token === 'string' ? values.api_token.trim() : ''
  const config: ApiRecord = {
    base_url: typeof values.base_url === 'string' ? values.base_url.trim() : '',
    verify_tls: values.verify_tls !== false,
  }
  if (Number.isSafeInteger(numericHubID) && numericHubID > 0) config.hub_id = numericHubID
  return {
    type: 'jupyterhub',
    config,
    ...(token ? { secret: token } : {}),
  }
}
