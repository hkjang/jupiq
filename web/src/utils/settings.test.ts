import { describe, expect, it } from 'vitest'
import { addAllowedHost, mailEventLabel, mailSummaryBadges, resolveOidcSettings, splitAllowedHosts } from './settings'

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

describe('mail delivery display', () => {
  it('서버의 이벤트 이름을 화면 표기로 바꾸고 모르는 이름은 그대로 둔다', () => {
    expect(mailEventLabel('approval.requested')).toBe('검토·승인 요청')
    expect(mailEventLabel('key.expiring')).toBe('API 키 만료 임박')
    expect(mailEventLabel('something.new')).toBe('something.new')
  })

  it('집계 배지는 성공·실패·대기 순이고 0건은 만들지 않는다', () => {
    expect(mailSummaryBadges({ queued: 1, sent: 4 })).toEqual([{ key: 'sent', label: '성공', count: 4 }, { key: 'queued', label: '대기', count: 1 }])
    expect(mailSummaryBadges(undefined)).toEqual([])
    expect(mailSummaryBadges({ failed: 0 })).toEqual([])
  })
})
