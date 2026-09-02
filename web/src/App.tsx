import { Navigate, Outlet, Route, Routes } from 'react-router-dom'
import { PermissionGuard, RequireAdmin, RequireAuth } from './auth/RouteGuards'
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
import { UserDetailPage } from './pages/UserDetailPage'

function ShellRoute() {
  return <AppShell><Outlet /></AppShell>
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route element={<RequireAuth />}>
        <Route element={<ShellRoute />}>
          <Route index element={<Navigate to="/dashboard" replace />} />
          <Route path="/dashboard" element={<PermissionGuard anyOf={['dashboard:read']}><DashboardPage /></PermissionGuard>} />
          <Route path="/hubs" element={<PermissionGuard anyOf={['hubs:read']}><HubsPage /></PermissionGuard>} />
          <Route path="/users" element={<PermissionGuard anyOf={['users:read']}><UsersPage /></PermissionGuard>} />
          <Route path="/users/:username" element={<PermissionGuard anyOf={['users:read']}><UserDetailPage /></PermissionGuard>} />
          <Route path="/servers" element={<PermissionGuard anyOf={['servers:read']}><ServersPage /></PermissionGuard>} />
          <Route path="/gpus" element={<PermissionGuard anyOf={['gpu:read']}><GpusPage /></PermissionGuard>} />
          <Route path="/projects" element={<PermissionGuard anyOf={['project:read']}><ProjectsPage /></PermissionGuard>} />
          <Route path="/policies" element={<PermissionGuard anyOf={['policy:read']}><PoliciesPage /></PermissionGuard>} />
          <Route path="/profiles" element={<PermissionGuard anyOf={['profile:read']}><ProfilesPage /></PermissionGuard>} />
          <Route path="/images" element={<PermissionGuard anyOf={['image:read']}><ImagesPage /></PermissionGuard>} />
          <Route path="/approvals" element={<PermissionGuard anyOf={['approval:read']}><ApprovalsPage /></PermissionGuard>} />
          <Route path="/incidents" element={<PermissionGuard anyOf={['incident:read']}><IncidentsPage /></PermissionGuard>} />
          <Route path="/audit" element={<PermissionGuard anyOf={['audit:read']}><AuditPage /></PermissionGuard>} />
          <Route path="/costs" element={<PermissionGuard anyOf={['cost:read']}><CostsPage /></PermissionGuard>} />
          <Route path="/ai-ops" element={<PermissionGuard anyOf={['ai:chat', 'usage:read']}><AiOpsPage /></PermissionGuard>} />
          <Route path="/notifications" element={<PermissionGuard anyOf={['notification:read']}><NotificationsPage /></PermissionGuard>} />
          <Route path="/personal" element={<PersonalPage />} />
          <Route element={<RequireAdmin />}>
            <Route path="/admin/settings" element={<SettingsPage />} />
          </Route>
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Route>
    </Routes>
  )
}
