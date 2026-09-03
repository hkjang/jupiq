import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { request } from '../api/client'
import type { ApiRecord } from '../types'
import {
  LLM_USAGE_RECONNECT_MS,
  llmUsagePath,
  normalizeLlmUsagePayload,
  useLiveLlmUsage,
  type LlmUsageRange,
} from './useLiveLlmUsage'

vi.mock('../api/client', () => ({
  apiUrl: (path: string) => `/api/v1${path}`,
  request: vi.fn(),
}))

class MockEventSource {
  static instances: MockEventSource[] = []
  readonly url: string
  readonly withCredentials: boolean
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent<string>) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  close = vi.fn()

  constructor(url: string, options?: EventSourceInit) {
    this.url = url
    this.withCredentials = Boolean(options?.withCredentials)
    MockEventSource.instances.push(this)
  }

  open() { this.onopen?.(new Event('open')) }
  message(payload: unknown) { this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(payload) })) }
  fail(message = '') { this.onerror?.(new MessageEvent('error', { data: message })) }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const requestMock = vi.mocked(request)

beforeEach(() => {
  MockEventSource.instances = []
  requestMock.mockReset()
  vi.stubGlobal('EventSource', MockEventSource)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useLiveLlmUsage', () => {
  it('REST 초기값 뒤 같은 계약의 SSE payload로 즉시 갱신한다', async () => {
    requestMock.mockResolvedValue({ summary: { calls: 1 }, data: [] })
    const { result, unmount } = renderHook(() => useLiveLlmUsage('day'))

    await waitFor(() => expect(result.current.data?.summary).toEqual({ calls: 1 }))
    const source = MockEventSource.instances[0]
    expect(source.url).toBe('/api/v1/llm-usage/live?range=day&group_by=detail')
    expect(source.withCredentials).toBe(true)

    act(() => {
      source.open()
      source.message({ summary: { calls: 2 }, data: [{ username: 'user01', calls: 2 }] })
    })
    expect(result.current.connection).toBe('실시간')
    expect(result.current.data?.summary).toEqual({ calls: 2 })
    expect(result.current.error).toBeNull()

    unmount()
    expect(source.close).toHaveBeenCalled()
  })

  it('기간 변경 시 이전 stream과 REST 응답을 무효화한다', async () => {
    const day = deferred<ApiRecord>()
    const week = deferred<ApiRecord>()
    requestMock.mockImplementationOnce(() => day.promise).mockImplementationOnce(() => week.promise)
    const { result, rerender } = renderHook(
      ({ range }: { range: LlmUsageRange }) => useLiveLlmUsage(range),
      { initialProps: { range: 'day' as LlmUsageRange } },
    )
    const daySource = MockEventSource.instances[0]

    rerender({ range: 'week' })
    expect(daySource.close).toHaveBeenCalled()
    expect(MockEventSource.instances[1].url).toBe('/api/v1/llm-usage/live?range=week&group_by=detail')

    await act(async () => { week.resolve({ summary: { calls: 7 } }); await Promise.resolve() })
    expect(result.current.data?.summary).toEqual({ calls: 7 })

    await act(async () => { day.resolve({ summary: { calls: 99 } }); await Promise.resolve() })
    act(() => daySource.message({ summary: { calls: 100 } }))
    expect(result.current.data?.summary).toEqual({ calls: 7 })
  })

  it('stream 오류 시 polling으로 전환하고 다시 연결한다', async () => {
    vi.useFakeTimers()
    requestMock.mockResolvedValue({ summary: { calls: 3 }, data: [] })
    const { result, unmount } = renderHook(() => useLiveLlmUsage('month'))
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    const source = MockEventSource.instances[0]

    await act(async () => {
      source.fail(JSON.stringify({ message: '수집기 오류' }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(source.close).toHaveBeenCalled()
    expect(result.current.connection).toBe('주기 조회')
    expect(result.current.streamError?.message).toBe('수집기 오류')
    expect(requestMock).toHaveBeenCalledWith(llmUsagePath('month'))

    await act(async () => {
      vi.advanceTimersByTime(LLM_USAGE_RECONNECT_MS)
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(MockEventSource.instances).toHaveLength(2)
    expect(MockEventSource.instances[1].url).toBe('/api/v1/llm-usage/live?range=month&group_by=detail')

    unmount()
  })
})

describe('LLM usage stream helpers', () => {
  it('REST envelope와 직접 SSE 결과를 구분한다', () => {
    expect(normalizeLlmUsagePayload({ data: { summary: { calls: 1 } } })).toEqual({ summary: { calls: 1 } })
    expect(normalizeLlmUsagePayload({ data: [], summary: { calls: 2 } })).toEqual({ data: [], summary: { calls: 2 } })
    expect(normalizeLlmUsagePayload(null)).toBeNull()
  })
})
