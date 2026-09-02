import { describe, expect, it } from 'vitest'
import { filterLiveUsers, formatCpuResource, formatMemoryResource, summarizeLiveUsers } from './dashboard'

const sessions = [
  { username: 'user01', network: '업무망', hub: 'hub-a', department: 'AI', project: 'rag', cpu_cores: 1.5, memory_bytes: 1073741824, runtime_seconds: 90000, idle_candidate: true },
  { username: 'user02', network: '개발망', hub: 'hub-b', department: '개발', project: 'vision', cpu_cores: 2, memory_bytes: 2147483648, runtime_seconds: 3600 },
]

describe('dashboard live resource view', () => {
  it('선택 필터를 사용자 집합과 KPI 집계에 동일하게 적용한다', () => {
    const filtered = filterLiveUsers(sessions, { network: '업무망', project: 'rag' })
    expect(filtered.map((row) => row.username)).toEqual(['user01'])
    expect(summarizeLiveUsers(filtered)).toMatchObject({ active_users: 1, running_servers: 1, cpu_cores: 1.5, memory_bytes: 1073741824, idle_sessions: 1, long_running_sessions: 1 })
  })

  it('backend core와 byte 값을 퍼센트로 오표시하지 않는다', () => {
    expect(formatCpuResource({ cpu_usage: 2.5 })).toBe('2.5 Core')
    expect(formatMemoryResource({ memory_usage: 1073741824 })).toBe('1 GB')
    expect(formatCpuResource({ cpu_percent: 42 })).toBe('42%')
  })
})
