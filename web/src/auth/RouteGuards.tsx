import { Button, Result, Spin } from 'antd'
import { useEffect, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from './AuthContext'
import { beginSilentSso, shouldAttemptSilentSso } from './silentSso'
import { firstAccessiblePath } from '../utils/navigation'

function FullScreenSpinner({ label }: { label: string }) {
  return <div className="full-screen-state"><div><Spin size="large" /><span className="loading-label">{label}</span></div></div>
}

export function RequireAuth() {
  const { user, loading, authError, oidc } = useAuth()
  const location = useLocation()
  const returnTo = `${location.pathname}${location.search}`
  // With auto_login on, a visitor without a session is sent to the provider
  // once before the login screen is shown. The decision is made only after the
  // session check finished, so a signed-in user is never redirected.
  const silent = !loading && !user && !authError && shouldAttemptSilentSso(oidc, location.pathname, location.search)
  const [silentStarted, setSilentStarted] = useState(false)
  useEffect(() => {
    if (!silent || silentStarted) return
    setSilentStarted(true)
    beginSilentSso(returnTo)
  }, [silent, silentStarted, returnTo])

  if (loading) return <FullScreenSpinner label="인증 정보를 확인하고 있습니다…" />
  if (!user && authError) return <Result status="500" title="인증 서비스를 확인할 수 없습니다" subTitle={authError} extra={<Button type="primary" onClick={() => window.location.reload()}>다시 시도</Button>} />
  // Keep the spinner up while the tab is leaving for the provider; rendering
  // the login page for a frame would look like a flicker.
  if (!user && (silent || silentStarted)) return <FullScreenSpinner label="SSO 세션을 확인하고 있습니다…" />
  if (!user) return <Navigate to="/login" replace state={{ from: returnTo }} />
  return <Outlet />
}

export function RequireAdmin() {
  const { hasGlobalPermission } = useAuth()
  if (!hasGlobalPermission('settings:write')) return <Result status="403" title="접근 권한이 없습니다" subTitle="관리자 설정 권한(settings:write)이 필요합니다." />
  return <Outlet />
}

export function PermissionGuard({ anyOf, scopeAware = false, children }: { anyOf: string[]; scopeAware?: boolean; children: ReactNode }) {
  const { user, features, hasAnyPermission, hasAnyGlobalPermission } = useAuth()
  const allowed = scopeAware ? hasAnyPermission(anyOf) : hasAnyGlobalPermission(anyOf)
  if (!allowed) return <Navigate to={firstAccessiblePath(user?.permissions, features, user?.global_permissions)} replace />
  return <>{children}</>
}
