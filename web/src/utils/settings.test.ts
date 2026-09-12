import { describe, expect, it } from 'vitest'
import { addAllowedHost, resolveOidcSettings, splitAllowedHosts } from './settings'

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

describe('analytics allowed hosts', () => {
  it('빈 목록에는 출처만 넣고 끝의 슬래시를 뗀다', () => {
    expect(addAllowedHost('', 'https://momento.corp.example/')).toBe('https://momento.corp.example')
  })

  it('이미 있는 출처는 대소문자와 무관하게 다시 넣지 않는다', () => {
    expect(addAllowedHost('https://a.example, https://b.example', 'HTTPS://A.EXAMPLE')).toBe('https://a.example, https://b.example')
  })

  it('새 출처는 쉼표로 이어 붙이고 빈 값은 무시한다', () => {
    expect(addAllowedHost('https://a.example', 'https://b.example')).toBe('https://a.example, https://b.example')
    expect(addAllowedHost('https://a.example', '   ')).toBe('https://a.example')
  })

  it('쉼표·공백·줄바꿈으로 나뉜 목록을 항목으로 나눈다', () => {
    expect(splitAllowedHosts('https://a.example,https://b.example\n https://c.example ')).toEqual(['https://a.example', 'https://b.example', 'https://c.example'])
  })
})
