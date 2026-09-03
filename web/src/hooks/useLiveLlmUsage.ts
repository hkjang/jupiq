import { useCallback, useEffect, useRef, useState } from 'react'
import { apiUrl, request } from '../api/client'
import type { ApiRecord } from '../types'

export type LlmUsageRange = 'day' | 'week' | 'month'
export type LlmUsageConnection = '연결 중' | '실시간' | '주기 조회' | '연결 끊김'

export const LLM_USAGE_POLL_MS = 15_000
export const LLM_USAGE_RECONNECT_MS = 30_000
export const LLM_USAGE_STALE_MS = 20_000

export function llmUsagePath(range: LlmUsageRange, live = false) {
  const params = new URLSearchParams({ range, group_by: 'detail' })
  return `/llm-usage${live ? '/live' : ''}?${params.toString()}`
}

export function normalizeLlmUsagePayload(payload: unknown): ApiRecord | null {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null
  const record = payload as ApiRecord
  if (record.data && typeof record.data === 'object' && !Array.isArray(record.data) && Object.keys(record).length === 1) {
    return record.data as ApiRecord
  }
  return record
}

function eventError(event: Event) {
  const fallback = '실시간 연결이 끊어져 주기 조회로 전환했습니다.'
  if (!('data' in event) || typeof event.data !== 'string' || !event.data.trim()) return new Error(fallback)
  try {
    const payload = JSON.parse(event.data) as { message?: unknown; error?: unknown }
    const message = typeof payload.message === 'string' ? payload.message : typeof payload.error === 'string' ? payload.error : fallback
    return new Error(message)
  } catch {
    return new Error(event.data)
  }
}

export function useLiveLlmUsage(range: LlmUsageRange) {
  const [data, setData] = useState<ApiRecord | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<Error | null>(null)
  const [streamError, setStreamError] = useState<Error | null>(null)
  const [connection, setConnection] = useState<LlmUsageConnection>('연결 중')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [now, setNow] = useState(Date.now())
  const generation = useRef(0)
  const requestSequence = useRef(0)
  const dataRef = useRef<ApiRecord | null>(null)

  const load = useCallback(async (quiet = false) => {
    const requestID = ++requestSequence.current
    if (!quiet) setLoading(true)
    try {
      const next = await request<ApiRecord>(llmUsagePath(range))
      if (requestID !== requestSequence.current) return false
      dataRef.current = next
      setData(next)
      setError(null)
      setLastUpdated(new Date())
      setLoading(false)
      return true
    } catch (caught) {
      if (requestID !== requestSequence.current) return false
      setError(caught instanceof Error ? caught : new Error('LLM 사용량을 불러오지 못했습니다.'))
      setLoading(false)
      return false
    }
  }, [range])

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 5_000)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    generation.current += 1
    const current = generation.current
    let eventSource: EventSource | null = null
    let pollTimer: number | null = null
    let retryTimer: number | null = null
    let watchdogTimer: number | null = null
    let pollingStarted = false
    let streamActivityAt = Date.now()

    requestSequence.current += 1
    dataRef.current = null
    setData(null)
    setLoading(true)
    setError(null)
    setStreamError(null)
    setConnection('연결 중')
    setLastUpdated(null)

    const stopPolling = () => {
      if (pollTimer !== null) window.clearInterval(pollTimer)
      pollTimer = null
      pollingStarted = false
    }

    const poll = async () => {
      if (current !== generation.current) return
      const ok = await load(Boolean(dataRef.current))
      if (current === generation.current && eventSource === null) setConnection(ok ? '주기 조회' : '연결 끊김')
    }

    const startPolling = () => {
      if (pollingStarted || current !== generation.current) return
      pollingStarted = true
      void poll()
      pollTimer = window.setInterval(() => void poll(), LLM_USAGE_POLL_MS)
    }

    const scheduleReconnect = () => {
      if (retryTimer !== null || current !== generation.current) return
      retryTimer = window.setTimeout(() => {
        retryTimer = null
        if (current !== generation.current) return
        setConnection('연결 중')
        connect()
      }, LLM_USAGE_RECONNECT_MS)
    }

    const handleStreamFailure = (source: EventSource | null, nextError: Error) => {
      if (current !== generation.current || (source && source !== eventSource)) return
      eventSource?.close()
      eventSource = null
      setStreamError(nextError)
      setConnection('연결 끊김')
      startPolling()
      scheduleReconnect()
    }

    function connect() {
      if (current !== generation.current) return
      if (typeof EventSource === 'undefined') {
        startPolling()
        return
      }
      try {
        const source = new EventSource(apiUrl(llmUsagePath(range, true)), { withCredentials: true })
        eventSource = source
        streamActivityAt = Date.now()
        source.onopen = () => {
          if (current !== generation.current || source !== eventSource) return
          streamActivityAt = Date.now()
          stopPolling()
          setConnection('실시간')
        }
        source.onmessage = (event) => {
          if (current !== generation.current || source !== eventSource) return
          try {
            const next = normalizeLlmUsagePayload(JSON.parse(event.data))
            if (!next) throw new Error('실시간 데이터 형식이 올바르지 않습니다.')
            requestSequence.current += 1
            dataRef.current = next
            streamActivityAt = Date.now()
            setData(next)
            setError(null)
            setStreamError(null)
            setLastUpdated(new Date())
            setLoading(false)
            setConnection('실시간')
          } catch (caught) {
            handleStreamFailure(source, caught instanceof Error ? caught : new Error('실시간 데이터 형식이 올바르지 않습니다.'))
          }
        }
        source.onerror = (event) => handleStreamFailure(source, eventError(event))
      } catch (caught) {
        handleStreamFailure(null, caught instanceof Error ? caught : new Error('실시간 연결을 시작하지 못했습니다.'))
      }
    }

    if (typeof EventSource === 'undefined') startPolling()
    else {
      void load(true)
      connect()
    }
    watchdogTimer = window.setInterval(() => {
      if (current === generation.current && eventSource && Date.now() - streamActivityAt > LLM_USAGE_STALE_MS) {
        handleStreamFailure(eventSource, new Error('실시간 데이터 수신이 지연되어 주기 조회로 전환했습니다.'))
      }
    }, 5_000)

    return () => {
      generation.current += 1
      requestSequence.current += 1
      eventSource?.close()
      stopPolling()
      if (retryTimer !== null) window.clearTimeout(retryTimer)
      if (watchdogTimer !== null) window.clearInterval(watchdogTimer)
    }
  }, [range, load])

  const transportStale = Boolean(lastUpdated && now - lastUpdated.getTime() > LLM_USAGE_STALE_MS)
  const sourceStale = Boolean(data?.stale)
  return {
    data,
    loading,
    error,
    streamError,
    connection,
    lastUpdated,
    stale: transportStale || sourceStale,
    transportStale,
    sourceStale,
    reload: () => load(false),
  }
}
