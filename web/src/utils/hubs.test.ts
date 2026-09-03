import { describe, expect, it } from 'vitest'
import { hubPreflightPayload } from './hubs'

describe('hubPreflightPayload', () => {
  it('새 Hub의 현재 URL, TLS, token을 저장과 분리된 테스트 payload로 만든다', () => {
    expect(hubPreflightPayload({ base_url: ' https://hub.internal/ ', verify_tls: false, api_token: ' token-now ' })).toEqual({
      type: 'jupyterhub',
      config: { base_url: 'https://hub.internal/', verify_tls: false },
      secret: 'token-now',
    })
  })

  it('편집 중 token을 비우면 hub_id로 저장된 Hub token을 요청한다', () => {
    expect(hubPreflightPayload({ base_url: 'https://candidate.internal', verify_tls: true, api_token: '' }, 17)).toEqual({
      type: 'jupyterhub',
      config: { base_url: 'https://candidate.internal', verify_tls: true, hub_id: 17 },
    })
  })
})
