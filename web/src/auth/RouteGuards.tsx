import { Button, Result, Spin } from 'antd'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from './AuthContext'
import { firstAccessiblePath } from '../utils/navigation'

export function RequireAuth() {
  const { user, loading, authError } = useAuth()
  const location = useLocation()
  if (loading) return <div className="full-screen-state"><div><Spin size="large" /><span className="loading-label">인증 정보를 확인하고 있습니다…</span></div></div>
  if (!user && authError) return <Result status="500" title="인증 서비스를 확인할 수 없습니다" subTitle={authError} extra={<Button type="primary" onClick={() => window.location.reload()}>다시 시도</Button>} />
  if (!user) return <Navigate to="/login" replace state={{ from: `${location.pathname}${location.search}` }} />
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
