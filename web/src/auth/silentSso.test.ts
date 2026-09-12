import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  beginSilentSso,
  clearSilentSsoState,
  isSilentSsoExcludedPath,
  markSignedOut,
  safeReturnTo,
  shouldAttemptSilentSso,
} from './silentSso'

const enabled = { enabled: true, auto_login: true }

describe('silent SSO rules', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
  })

  afterEach(() => {
    window.sessionStorage.clear()
  })

  it('auto_login이 꺼진 기본 설치에서는 시도하지 않는다', () => {
    expect(shouldAttemptSilentSso({ enabled: true }, '/dashboard', '')).toBe(false)
    expect(shouldAttemptSilentSso({ enabled: true, auto_login: false }, '/dashboard', '')).toBe(false)
    expect(shouldAttemptSilentSso({ enabled: false, auto_login: true }, '/dashboard', '')).toBe(false)
  })

  it('켜져 있고 아직 시도하지 않았으면 한 번 시도한다', () => {
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '')).toBe(true)
  })

  it('한 탭 세션에서는 한 번만 시도한다', () => {
    const navigate = vi.fn()
    beginSilentSso('/hubs', navigate)
    expect(navigate).toHaveBeenCalledWith('/api/v1/auth/oidc/login?prompt=none&return_to=%2Fhubs')
    expect(shouldAttemptSilentSso(enabled, '/hubs', '')).toBe(false)
  })

  it('콜백이 남긴 주소 표시가 있으면 저장소가 비어 있어도 다시 시도하지 않는다', () => {
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '?sso=none')).toBe(false)
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '?return_to=%2Fhubs&sso=error')).toBe(false)
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '?sso=other')).toBe(true)
  })

  it('스스로 로그아웃했으면 억제하고, 다시 세션이 생기면 억제를 푼다', () => {
    markSignedOut()
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '')).toBe(false)
    clearSilentSsoState()
    expect(shouldAttemptSilentSso(enabled, '/dashboard', '')).toBe(true)
  })

  it('저장소를 읽지 못하면 이미 시도한 것으로 친다', () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    try {
      expect(shouldAttemptSilentSso(enabled, '/dashboard', '')).toBe(false)
    } finally {
      getItem.mockRestore()
    }
  })

  it('로그인·콜백·API·MCP·probe 경로에서는 시도하지 않는다', () => {
    for (const pathname of ['/login', '/login/', '/api/v1/auth/oidc/callback', '/api/v1/hubs', '/mcp', '/healthz', '/readyz', '/momento/tracker.js']) {
      expect(isSilentSsoExcludedPath(pathname), pathname).toBe(true)
      expect(shouldAttemptSilentSso(enabled, pathname, ''), pathname).toBe(false)
    }
    for (const pathname of ['/', '/dashboard', '/users/user01', '/admin/settings']) {
      expect(isSilentSsoExcludedPath(pathname), pathname).toBe(false)
    }
  })

  it('깊은 링크는 같은 출처의 절대 경로만 들고 간다', () => {
    expect(safeReturnTo('/users/user01?tab=servers')).toBe('/users/user01?tab=servers')
    for (const unsafe of ['//evil.example', '/\\evil.example', 'https://evil.example/', 'dashboard', '']) {
      expect(safeReturnTo(unsafe), unsafe).toBe('/')
    }
    const navigate = vi.fn()
    beginSilentSso('//evil.example', navigate)
    expect(navigate).toHaveBeenCalledWith('/api/v1/auth/oidc/login?prompt=none&return_to=%2F')
  })
})
