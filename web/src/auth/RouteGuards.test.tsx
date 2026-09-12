import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { OidcConfig, User } from '../types'
import { RequireAuth } from './RouteGuards'

vi.mock('antd', async () => {
  const React = await import('react')
  const container = ({ children }: React.PropsWithChildren) => React.createElement('div', null, children)
  return { Button: container, Result: ({ title }: { title: string }) => React.createElement('div', null, title), Spin: () => null }
})

const authState: { user: User | null; loading: boolean; authError: string; oidc: OidcConfig } = {
  user: null, loading: false, authError: '', oidc: { enabled: true, auto_login: true },
}

vi.mock('./AuthContext', () => ({ useAuth: () => authState }))

const navigate = vi.fn()
vi.mock('./silentSso', async () => {
  const actual = await vi.importActual<typeof import('./silentSso')>('./silentSso')
  return { ...actual, beginSilentSso: (returnTo: string) => actual.beginSilentSso(returnTo, navigate) }
})

function renderGuard(initialPath: string) {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route path="/login" element={<div>로그인 화면</div>} />
        <Route element={<RequireAuth />}>
          <Route path="*" element={<div>본 화면</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

describe('RequireAuth silent SSO', () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    navigate.mockReset()
    authState.user = null
    authState.loading = false
    authState.authError = ''
    authState.oidc = { enabled: true, auto_login: true }
  })

  afterEach(() => {
    window.sessionStorage.clear()
  })

  it('세션이 없고 auto_login이 켜져 있으면 로그인 화면 대신 제공자로 한 번 보낸다', () => {
    renderGuard('/users/user01?tab=servers')
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith('/api/v1/auth/oidc/login?prompt=none&return_to=%2Fusers%2Fuser01%3Ftab%3Dservers')
    expect(screen.queryByText('로그인 화면')).toBeNull()
    expect(screen.getByText('SSO 세션을 확인하고 있습니다…')).toBeInTheDocument()
  })

  it('거절 표시가 붙은 주소에서는 다시 시도하지 않고 로그인 화면으로 간다', () => {
    renderGuard('/dashboard?sso=none')
    expect(navigate).not.toHaveBeenCalled()
    expect(screen.getByText('로그인 화면')).toBeInTheDocument()
  })

  it('이미 시도한 탭에서는 새로고침해도 로그인 화면으로 간다', () => {
    renderGuard('/dashboard')
    expect(navigate).toHaveBeenCalledTimes(1)
    const again = renderGuard('/dashboard')
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(again.getByText('로그인 화면')).toBeInTheDocument()
  })

  it('auto_login이 꺼진 기본 설치에서는 바로 로그인 화면으로 간다', () => {
    authState.oidc = { enabled: true }
    renderGuard('/dashboard')
    expect(navigate).not.toHaveBeenCalled()
    expect(screen.getByText('로그인 화면')).toBeInTheDocument()
  })

  it('세션 확인이 끝나기 전에는 제공자로 보내지 않는다', () => {
    authState.loading = true
    renderGuard('/dashboard')
    expect(navigate).not.toHaveBeenCalled()
    expect(screen.getByText('인증 정보를 확인하고 있습니다…')).toBeInTheDocument()
  })

  it('세션이 있으면 시도하지 않고 본 화면을 그린다', () => {
    authState.user = { id: 1, username: 'admin', roles: [], permissions: [], global_permissions: [], scoped_permissions: [] } as unknown as User
    renderGuard('/dashboard')
    expect(navigate).not.toHaveBeenCalled()
    expect(screen.getByText('본 화면')).toBeInTheDocument()
  })
})
