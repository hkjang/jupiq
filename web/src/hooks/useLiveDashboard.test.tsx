import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { request } from '../api/client'
import { useLiveDashboard, type DashboardFilters } from './useLiveDashboard'

vi.mock('../api/client', () => ({
  apiUrl: (path: string) => `/api/v1${path}`,
  request: vi.fn(),
}))

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({ hasGlobalPermission: () => false }),
}))

class MockEventSource {
  static instances: MockEventSource[] = []
  readonly url: string
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  close = vi.fn()

  constructor(url: string) {
    this.url = url
    MockEventSource.instances.push(this)
  }
}

const requestMock = vi.mocked(request)

beforeEach(() => {
  MockEventSource.instances = []
  requestMock.mockReset()
  vi.stubGlobal('EventSource', MockEventSource)
})

afterEach(() => { vi.unstubAllGlobals() })

describe('useLiveDashboard', () => {
  it('필터를 바꿔도 이전 데이터를 화면에 유지한 채 새 조회만 표시한다', async () => {
    requestMock.mockResolvedValue({ summary: { active_users: 7 } })
    const { result, rerender, unmount } = renderHook(
      ({ filters }: { filters: DashboardFilters }) => useLiveDashboard(filters),
      { initialProps: { filters: { range: 'day' } as DashboardFilters } },
    )
    await waitFor(() => expect(result.current.data).not.toBeNull())
    expect(result.current.loading).toBe(false)

    let resolveNext: (value: unknown) => void = () => {}
    requestMock.mockImplementationOnce(() => new Promise((resolve) => { resolveNext = resolve }))
    rerender({ filters: { range: 'day', network: '업무망' } })

    // 이전 스냅샷이 그대로 남아 있어야 화면이 비었다가 다시 그려지지 않는다.
    expect(result.current.data).not.toBeNull()
    expect(result.current.loading).toBe(false)
    await waitFor(() => expect(result.current.refreshing).toBe(true))

    resolveNext({ summary: { active_users: 2 } })
    await waitFor(() => expect(result.current.data?.summary).toEqual({ active_users: 2 }))
    expect(MockEventSource.instances.at(-1)?.url).toContain('network=%EC%97%85%EB%AC%B4%EB%A7%9D')
    unmount()
  })

  it('첫 조회에서는 로딩 상태를 노출한다', async () => {
    requestMock.mockResolvedValue({ summary: {} })
    const { result, unmount } = renderHook(() => useLiveDashboard({ range: 'day' }))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    unmount()
  })
})
