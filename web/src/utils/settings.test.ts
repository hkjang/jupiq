import { describe, expect, it } from 'vitest'
import { resolveOidcSettings } from './settings'

describe('settings form aliases', () => {
  it('저장소의 auth.oidc 설정을 OIDC 폼 값으로 사용한다', () => {
    expect(resolveOidcSettings({ 'auth.oidc': { issuer_url: 'https://sso.internal/realms/jupiq' } }))
      .toEqual({ issuer_url: 'https://sso.internal/realms/jupiq' })
  })

  it('API가 제공한 oidc 별칭이 있으면 이를 우선한다', () => {
    expect(resolveOidcSettings({
      oidc: { client_id: 'current' },
      'auth.oidc': { client_id: 'legacy' },
    })).toEqual({ client_id: 'current' })
  })
})
