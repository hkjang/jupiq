import { hasAnyGrantedPermission } from './permissions'

export interface NavigationFeatures {
  gpuMonitoring: boolean
  llmUsageMonitoring: boolean
  approvalWorkflow: boolean
}

export const navigationPaths = [
  '/dashboard', '/hubs', '/users', '/servers', '/gpus', '/projects', '/policies', '/profiles',
  '/images', '/approvals', '/incidents', '/audit', '/costs', '/ai-ops', '/notifications', '/admin/settings', '/personal',
]

export const navigationPermissions: Record<string, string[]> = {
  '/dashboard': ['dashboard:read'], '/hubs': ['hubs:read'], '/users': ['users:read'], '/servers': ['servers:read'],
  '/gpus': ['gpu:read'], '/projects': ['project:read'], '/policies': ['policy:read'], '/profiles': ['profiles:read'],
  '/images': ['image:read'], '/approvals': ['approval:read'], '/incidents': ['incident:read'], '/audit': ['audit:read'],
  '/costs': ['cost:read'], '/ai-ops': ['ai:chat', 'usage:read'], '/notifications': ['notification:read'],
  '/admin/settings': ['settings:read', 'settings:write'],
}

export const globalSearchPermissions = ['users:read', 'hubs:read', 'servers:read', 'project:read']
const scopedNavigationPaths = new Set(['/hubs', '/users', '/servers'])

export function canUseGlobalSearch(permissions: string[] | undefined) {
  return hasAnyGrantedPermission(permissions, globalSearchPermissions)
}

export function canOpenUserDetail(globalPermissions: string[] | undefined) {
  return hasAnyGrantedPermission(globalPermissions, ['users:read'])
}

export function canAccessNavigationPath(path: string, permissions: string[] | undefined, features: NavigationFeatures, globalPermissions: string[] | undefined = permissions) {
  const required = navigationPermissions[path]
  const granted = scopedNavigationPaths.has(path) ? permissions : globalPermissions
  if (required && !hasAnyGrantedPermission(granted, required)) return false
  if (!isFeatureMenuVisible(path, features)) return false
  if (path === '/ai-ops') {
    return hasAnyGrantedPermission(globalPermissions, ['ai:chat'])
      || (features.llmUsageMonitoring && hasAnyGrantedPermission(globalPermissions, ['usage:read']))
  }
  return true
}

export function firstAccessiblePath(permissions: string[] | undefined, features: NavigationFeatures, globalPermissions: string[] | undefined = permissions) {
  const candidates = navigationPaths.filter((path) => path !== '/personal')
  return candidates.find((path) => canAccessNavigationPath(path, permissions, features, globalPermissions)) || '/personal'
}

export function selectedNavigationPath(pathname: string) {
  return navigationPaths
    .filter((path) => pathname === path || pathname.startsWith(`${path}/`))
    .sort((left, right) => right.length - left.length)[0] || pathname
}

export function isFeatureMenuVisible(key: string, features: NavigationFeatures) {
  if (key === '/gpus') return features.gpuMonitoring
  if (key === '/approvals') return features.approvalWorkflow
  return true
}

export function serviceVersionLabel(version?: string) {
  return `v${version?.trim() || '확인 불가'}`
}
