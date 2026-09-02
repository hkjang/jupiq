import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ApiError, jsonBody, request } from '../api/client'
import type { OidcConfig, User, VersionInfo } from '../types'
import { hasAnyGrantedPermission, hasGrantedPermission } from '../utils/permissions'

interface AuthContextValue {
  user: User | null
  version: VersionInfo
  oidc: OidcConfig
  loading: boolean
  isAdmin: boolean
  hasPermission: (permission: string) => boolean
  hasAnyPermission: (permissions: string[]) => boolean
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

function normalizeUser(value: User): User {
  const roles = Array.isArray(value.roles)
    ? value.roles
    : typeof value.role === 'string'
      ? [value.role]
      : []
  return {
    ...value,
    username: value.username || String(value.email || value.id || '사용자'),
    roles,
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
  const [gpuMonitoring, setGpuMonitoring] = useState(false)
  const [llmUsageMonitoring, setLlmUsageMonitoring] = useState(false)
  const [approvalWorkflow, setApprovalWorkflow] = useState(false)

  const refreshUser = useCallback(async () => {
    try {
      const payload = await request<User | { user?: User }>('/auth/me')
      setUser(normalizeUser(unwrapUser(payload)))
    } catch (error) {
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        setUser(null)
        return
      }
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
      } catch {
        setUser(null)
      } finally {
        if (active) setLoading(false)
      }
    }
    void initialize()
    return () => { active = false }
  }, [refreshUser])

  const refreshFeatures = useCallback(async () => {
    try {
      const payload = await request<Record<string, unknown>>('/settings')
      const settings = payload.settings && typeof payload.settings === 'object' ? payload.settings as Record<string, unknown> : payload
      const features = settings.features && typeof settings.features === 'object' ? settings.features as Record<string, unknown> : settings
      setGpuMonitoring(Boolean(features.gpu_monitoring ?? features.gpu_monitoring_enabled ?? false))
      setLlmUsageMonitoring(Boolean(features.llm_usage_monitoring ?? features.llm_usage_monitoring_enabled ?? false))
      const workflow = settings.workflow && typeof settings.workflow === 'object' ? settings.workflow as Record<string, unknown> : {}
      setApprovalWorkflow(Boolean(features.approval_workflow ?? workflow.approval_enabled ?? false))
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
  const isAdmin = useMemo(() => hasPermission('settings:write'), [hasPermission])

  const value = useMemo<AuthContextValue>(() => ({
    user, version, oidc, loading, isAdmin, hasPermission, hasAnyPermission,
    features: { gpuMonitoring, llmUsageMonitoring, approvalWorkflow }, setGpuMonitoring, setLlmUsageMonitoring, setApprovalWorkflow, refreshFeatures,
    login, logout, refreshUser,
  }), [user, version, oidc, loading, isAdmin, hasPermission, hasAnyPermission, gpuMonitoring, llmUsageMonitoring, approvalWorkflow, refreshFeatures, login, logout, refreshUser])
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (!context) throw new Error('useAuth는 AuthProvider 안에서 사용해야 합니다.')
  return context
}
