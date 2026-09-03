import { describe, expect, it } from 'vitest'
import { formatManagedUserRuntime, managedUserFields } from './managedUsers'

describe('통합 사용자 필드 계약', () => {
  it('서버 집계와 최근 활동 API 필드를 그대로 사용한다', () => {
    expect(managedUserFields).toEqual({
      roles: ['roles', 'role'],
      serverStatus: ['server_status'],
      runningServers: ['running_server_count', 'server_count'],
      runtime: ['runtime_seconds'],
      cpu: ['cpu_cores'],
      memory: ['memory_bytes'],
      lastActivity: ['last_activity_at'],
    })
    expect(formatManagedUserRuntime(3660)).toBe('1시간 1분')
  })
})
