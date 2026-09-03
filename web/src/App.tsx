import { Navigate, Outlet, Route, Routes } from 'react-router-dom'
import { PermissionGuard, RequireAuth } from './auth/RouteGuards'
import { useAuth } from './auth/AuthContext'
import { AppShell } from './components/AppShell'
import { AiOpsPage } from './pages/AiOpsPage'
import { ApprovalsPage } from './pages/ApprovalsPage'
import { DashboardPage } from './pages/DashboardPage'
import { LoginPage } from './pages/LoginPage'
import { NotFoundPage } from './pages/NotFoundPage'
import { PersonalPage } from './pages/PersonalPage'
import {
  AuditPage,
  CostsPage,
  GpusPage,
  HubsPage,
  ImagesPage,
  IncidentsPage,
  NotificationsPage,
  PoliciesPage,
  ProfilesPage,
  ProjectsPage,
  ServersPage,
  UsersPage,
} from './pages/ResourcePages'
import { SettingsPage } from './pages/SettingsPage'
import { SearchPage } from './pages/SearchPage'
import { UserDetailPage } from './pages/UserDetailPage'
import { firstAccessiblePath, globalSearchPermissions } from './utils/navigation'

function ShellRoute() {
  return <AppShell><Outlet /></AppShell>
}

function DefaultRoute() {
  const { user, features } = useAuth()
  return <Navigate to={firstAccessiblePath(user?.permissions, features, user?.global_permissions)} replace />
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireAuth />}>
        <Route element={<ShellRoute />}>
          <Route index element={<DefaultRoute />} />
          <Route path="/dashboard" element={<PermissionGuard anyOf={['dashboard:read']}><DashboardPage /></PermissionGuard>} />
          <Route path="/hubs" element={<PermissionGuard anyOf={['hubs:read']} scopeAware><HubsPage /></PermissionGuard>} />
          <Route path="/users" element={<PermissionGuard anyOf={['users:read']} scopeAware><UsersPage /></PermissionGuard>} />
          <Route path="/users/:username" element={<PermissionGuard anyOf={['users:read']}><UserDetailPage /></PermissionGuard>} />
          <Route path="/servers" element={<PermissionGuard anyOf={['servers:read']} scopeAware><ServersPage /></PermissionGuard>} />
          <Route path="/gpus" element={<PermissionGuard anyOf={['gpu:read']}><GpusPage /></PermissionGuard>} />
          <Route path="/projects" element={<PermissionGuard anyOf={['project:read']}><ProjectsPage /></PermissionGuard>} />
          <Route path="/policies" element={<PermissionGuard anyOf={['policy:read']}><PoliciesPage /></PermissionGuard>} />
          <Route path="/profiles" element={<PermissionGuard anyOf={['profiles:read']}><ProfilesPage /></PermissionGuard>} />
          <Route path="/images" element={<PermissionGuard anyOf={['image:read']}><ImagesPage /></PermissionGuard>} />
          <Route path="/approvals" element={<PermissionGuard anyOf={['approval:read']}><ApprovalsPage /></PermissionGuard>} />
          <Route path="/incidents" element={<PermissionGuard anyOf={['incident:read']}><IncidentsPage /></PermissionGuard>} />
          <Route path="/audit" element={<PermissionGuard anyOf={['audit:read']}><AuditPage /></PermissionGuard>} />
          <Route path="/costs" element={<PermissionGuard anyOf={['cost:read']}><CostsPage /></PermissionGuard>} />
          <Route path="/ai-ops" element={<PermissionGuard anyOf={['ai:chat', 'usage:read']}><AiOpsPage /></PermissionGuard>} />
          <Route path="/notifications" element={<PermissionGuard anyOf={['notification:read']}><NotificationsPage /></PermissionGuard>} />
          <Route path="/personal" element={<PersonalPage />} />
          <Route path="/search" element={<PermissionGuard anyOf={globalSearchPermissions}><SearchPage /></PermissionGuard>} />
          <Route path="/admin/settings" element={<PermissionGuard anyOf={['settings:read', 'settings:write']}><SettingsPage /></PermissionGuard>} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Route>
    </Routes>
  )
}
