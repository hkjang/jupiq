import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ApiError, jsonBody, request } from '../api/client'
import type { OidcConfig, User, VersionInfo } from '../types'
import { hasAnyGrantedPermission, hasGrantedPermission } from '../utils/permissions'

interface AuthContextValue {
  user: User | null
  version: VersionInfo
  oidc: OidcConfig
  loading: boolean
  authError: string
  isAdmin: boolean
  hasPermission: (permission: string) => boolean
  hasAnyPermission: (permissions: string[]) => boolean
  hasGlobalPermission: (permission: string) => boolean
  hasAnyGlobalPermission: (permissions: string[]) => boolean
  features: { gpuMonitoring: boolean; llmUsageMonitoring: boolean; approvalWorkflow: boolean }
  setGpuMonitoring: (enabled: boolean) => void
  setLlmUsageMonitoring: (enabled: boolean) => void
  setApprovalWorkflow: (enabled: boolean) => void
  refreshFeatures: () => Promise<void>
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshUser: () => Promise<void>
}

const defaultVersion: VersionInfo = { version: '확인 불가' }
const defaultOidc: OidcConfig = { enabled: false }
const AuthContext = createContext<AuthContextValue | null>(null)

export function normalizeUser(value: User): User {
  const roles = Array.isArray(value.roles)
    ? value.roles
    : typeof value.role === 'string'
      ? [value.role]
      : []
  return {
    ...value,
    username: value.username || String(value.email || value.id || '사용자'),
    roles,
    permissions: Array.isArray(value.permissions) ? value.permissions : [],
    // Older servers exposed only permissions. Keep that wire compatibility,
    // while treating an explicitly empty global_permissions array as scoped.
    global_permissions: Array.isArray(value.global_permissions) ? value.global_permissions : value.permissions || [],
    scoped_permissions: Array.isArray(value.scoped_permissions) ? value.scoped_permissions : [],
  }
}

function unwrapUser(payload: User | { user?: User }): User {
  if ('user' in payload && payload.user && typeof payload.user === 'object') return payload.user as User
  return payload as User
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [version, setVersion] = useState<VersionInfo>(defaultVersion)
  const [oidc, setOidc] = useState<OidcConfig>(defaultOidc)
  const [loading, setLoading] = useState(true)
  const [authError, setAuthError] = useState('')
  const [gpuMonitoring, setGpuMonitoring] = useState(false)
  const [llmUsageMonitoring, setLlmUsageMonitoring] = useState(false)
  const [approvalWorkflow, setApprovalWorkflow] = useState(false)

  const refreshUser = useCallback(async () => {
    try {
      const payload = await request<User | { user?: User }>('/auth/me')
      setUser(normalizeUser(unwrapUser(payload)))
      setAuthError('')
    } catch (error) {
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        setUser(null)
        setAuthError('')
        return
      }
      setAuthError(error instanceof Error ? error.message : '인증 서비스를 확인할 수 없습니다.')
      throw error
    }
  }, [])

  useEffect(() => {
    let active = true
    const initialize = async () => {
      const [versionResult, oidcResult] = await Promise.allSettled([
        request<VersionInfo>('/version'),
        request<OidcConfig>('/auth/oidc/config'),
      ])
      if (!active) return
      if (versionResult.status === 'fulfilled') setVersion({ ...versionResult.value, version: versionResult.value.version || '확인 불가' })
      if (oidcResult.status === 'fulfilled') setOidc({ ...oidcResult.value, enabled: Boolean(oidcResult.value.enabled) })
      try {
        await refreshUser()
      } catch { /* refreshUser가 미인증과 인증 서비스 장애를 구분해 기록합니다. */ } finally {
        if (active) setLoading(false)
      }
    }
    void initialize()
    return () => { active = false }
  }, [refreshUser])

  const refreshFeatures = useCallback(async () => {
    try {
      const features = await request<Record<string, unknown>>('/features')
      setGpuMonitoring(Boolean(features.gpu_monitoring))
      setLlmUsageMonitoring(Boolean(features.llm_usage_monitoring))
      setApprovalWorkflow(Boolean(features.approval_workflow))
    } catch {
      setGpuMonitoring(false)
      setLlmUsageMonitoring(false)
      setApprovalWorkflow(false)
    }
  }, [])

  useEffect(() => {
    if (user) void refreshFeatures()
  }, [user, refreshFeatures])

  const login = useCallback(async (username: string, password: string) => {
    const payload = await request<User | { user?: User }>('/auth/login', {
      method: 'POST',
      body: jsonBody({ username, password }),
    })
    const loggedIn = unwrapUser(payload)
    setAuthError('')
    if (loggedIn?.username || loggedIn?.id) setUser(normalizeUser(loggedIn))
    else await refreshUser()
  }, [refreshUser])

  const logout = useCallback(async () => {
    try {
      await request('/auth/logout', { method: 'POST' })
    } finally {
      setUser(null)
    }
  }, [])

  const hasPermission = useCallback((permission: string) => hasGrantedPermission(user?.permissions, permission), [user?.permissions])
  const hasAnyPermission = useCallback((permissions: string[]) => hasAnyGrantedPermission(user?.permissions, permissions), [user?.permissions])
  const hasGlobalPermission = useCallback((permission: string) => hasGrantedPermission(user?.global_permissions, permission), [user?.global_permissions])
  const hasAnyGlobalPermission = useCallback((permissions: string[]) => hasAnyGrantedPermission(user?.global_permissions, permissions), [user?.global_permissions])
  const isAdmin = useMemo(() => hasGlobalPermission('settings:write'), [hasGlobalPermission])

  const value = useMemo<AuthContextValue>(() => ({
    user, version, oidc, loading, authError, isAdmin, hasPermission, hasAnyPermission, hasGlobalPermission, hasAnyGlobalPermission,
    features: { gpuMonitoring, llmUsageMonitoring, approvalWorkflow }, setGpuMonitoring, setLlmUsageMonitoring, setApprovalWorkflow, refreshFeatures,
    login, logout, refreshUser,
  }), [user, version, oidc, loading, authError, isAdmin, hasPermission, hasAnyPermission, hasGlobalPermission, hasAnyGlobalPermission, gpuMonitoring, llmUsageMonitoring, approvalWorkflow, refreshFeatures, login, logout, refreshUser])
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (!context) throw new Error('useAuth는 AuthProvider 안에서 사용해야 합니다.')
  return context
}
