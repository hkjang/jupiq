import { describe, expect, it } from 'vitest'
import { filterLiveUsers, formatCpuResource, formatGpuResource, formatMemoryResource, formatVramResource, hasStaleLiveSessions, summarizeLiveUsers } from './dashboard'

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

  it('Hub가 보고한 어떤 key로도 같은 선택값을 찾아낸다', () => {
    const mixed = [
      { username: 'user01', hub: 3, hub_name: '업무망 Hub', project: 'p-1', project_name: 'RAG 파일럿' },
      { username: 'user02', hub: 4, hub_name: '개발망 Hub' },
    ]
    // 드롭다운은 hub_name으로 만들고 세션은 숫자 hub도 함께 들고 있다.
    expect(filterLiveUsers(mixed, { hub: '업무망 Hub' }).map((row) => row.username)).toEqual(['user01'])
    expect(filterLiveUsers(mixed, { hub: '3' }).map((row) => row.username)).toEqual(['user01'])
    expect(filterLiveUsers(mixed, { project: 'RAG 파일럿' }).map((row) => row.username)).toEqual(['user01'])
    expect(filterLiveUsers(mixed, { hub: '없는 Hub' })).toEqual([])
  })

  it('backend core와 byte 값을 퍼센트로 오표시하지 않는다', () => {
    expect(formatCpuResource({ cpu_usage: 2.5 })).toBe('2.5 Core')
    expect(formatMemoryResource({ memory_usage: 1073741824 })).toBe('1 GB')
    expect(formatCpuResource({ cpu_percent: 42 })).toBe('42%')
  })

  it('수집되지 않은 자원 값을 실제 사용량 0으로 만들지 않는다', () => {
    const summary = summarizeLiveUsers([{ username: 'user01' }])
    expect(summary).not.toHaveProperty('cpu_cores')
    expect(summary).not.toHaveProperty('memory_bytes')
    expect(summary).not.toHaveProperty('gpu_count')
    expect(summary).not.toHaveProperty('vram_bytes')
  })

  it('오래된 세션은 필터 KPI 집계에서 제외한다', () => {
    const summary = summarizeLiveUsers([
      { hub_id: 1, username: 'user01', cpu_cores: 1, memory_bytes: 1024, runtime_seconds: 3600, stale: false },
      { hub_id: 2, username: 'user01', cpu_cores: 2, memory_bytes: 2048, idle_candidate: true, stale: false },
      { hub_id: 3, username: 'old-user', cpu_cores: 99, memory_bytes: 99999, gpu_count: 8, vram_bytes: 1234, runtime_seconds: 200000, idle_candidate: true, stale: true },
    ])
    expect(summary).toMatchObject({ active_users: 2, running_servers: 2, cpu_cores: 3, memory_bytes: 3072, idle_sessions: 1, long_running_sessions: 0 })
    expect(summary).not.toHaveProperty('gpu_count')
    expect(summary).not.toHaveProperty('vram_bytes')
  })

  it('세션 또는 자원 지표 최신성 문제를 감지하고 결측 GPU를 0으로 표시하지 않는다', () => {
    expect(hasStaleLiveSessions([{ stale: true }])).toBe(true)
    expect(hasStaleLiveSessions([{ stale: false, resource_stale: true }])).toBe(true)
    expect(hasStaleLiveSessions([{ stale: false, resource_stale: false }])).toBe(false)
    expect(formatGpuResource({ gpu_count: null, gpu_utilization: null })).toBe('—')
    expect(formatVramResource({ vram_bytes: null, vram_utilization: null })).toBe('—')
  })
})
