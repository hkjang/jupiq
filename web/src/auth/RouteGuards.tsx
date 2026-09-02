import { Result, Spin } from 'antd'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from './AuthContext'

export function RequireAuth() {
  const { user, loading } = useAuth()
  const location = useLocation()
  if (loading) return <div className="full-screen-state"><div><Spin size="large" /><span className="loading-label">인증 정보를 확인하고 있습니다…</span></div></div>
  if (!user) return <Navigate to="/login" replace state={{ from: `${location.pathname}${location.search}` }} />
  return <Outlet />
}

export function RequireAdmin() {
  const { hasPermission } = useAuth()
  if (!hasPermission('settings:write')) return <Result status="403" title="접근 권한이 없습니다" subTitle="관리자 설정 권한(settings:write)이 필요합니다." />
  return <Outlet />
}

export function PermissionGuard({ anyOf, children }: { anyOf: string[]; children: ReactNode }) {
  const { hasAnyPermission } = useAuth()
  if (!hasAnyPermission(anyOf)) {
    return <Result status="403" title="접근 권한이 없습니다" subTitle={`필요 권한: ${anyOf.join(' 또는 ')}`} />
  }
  return <>{children}</>
}
