import { formatDuration } from './format'

export const managedUserFields: Record<string, string[]> = {
  roles: ['roles', 'role'],
  serverStatus: ['server_status'],
  runningServers: ['running_server_count', 'server_count'],
  runtime: ['runtime_seconds'],
  cpu: ['cpu_cores'],
  memory: ['memory_bytes'],
  lastActivity: ['last_activity_at'],
}

export function formatManagedUserRuntime(value: unknown): string {
  return formatDuration(value)
}
