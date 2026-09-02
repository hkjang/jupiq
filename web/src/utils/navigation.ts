export interface NavigationFeatures {
  gpuMonitoring: boolean
  approvalWorkflow: boolean
}

export const navigationPaths = [
  '/dashboard', '/hubs', '/users', '/servers', '/gpus', '/projects', '/policies', '/profiles',
  '/images', '/approvals', '/incidents', '/audit', '/costs', '/ai-ops', '/admin/settings', '/personal',
]

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
