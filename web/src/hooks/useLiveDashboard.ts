import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { apiUrl, request } from '../api/client'
import type { DashboardData } from '../types'
import { buildUsageOverlay, usageQuery } from '../utils/usage'

export type LiveConnection = '연결 중' | '실시간' | '주기 조회' | '연결 끊김'

export interface DashboardFilters {
  range: 'day' | 'week' | 'month'
  network?: string
  hub?: string
  department?: string
  project?: string
}

function makePath(filters: DashboardFilters, live = false) {
  const params = new URLSearchParams()
  Object.entries(filters).forEach(([key, value]) => { if (value) params.set(key, value) })
  return `/dashboard${live ? '/live' : ''}?${params.toString()}`
}

function normalize(payload: unknown): DashboardData | null {
  if (!payload || typeof payload !== 'object') return null
  if ('data' in payload && payload.data && typeof payload.data === 'object') return payload.data as DashboardData
  return payload as DashboardData
}

export function useLiveDashboard(filters: DashboardFilters) {
  const [data, setData] = useState<DashboardData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<Error | null>(null)
  const [connection, setConnection] = useState<LiveConnection>('연결 중')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [now, setNow] = useState(Date.now())
  const generation = useRef(0)
  const usageOverlay = useRef<Record<string, unknown>>({})
  const filterKey = useMemo(() => JSON.stringify(filters), [filters])

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true)
    try {
      const [next, usage] = await Promise.all([
        request<DashboardData>(makePath(filters)),
        request<Record<string, unknown>>(usageQuery(filters).path),
      ])
      usageOverlay.current = buildUsageOverlay(usage, filters)
      setData({ ...next, ...usageOverlay.current })
      setError(null)
      setLastUpdated(new Date())
      return true
    } catch (caught) {
      setError(caught instanceof Error ? caught : new Error('대시보드를 불러오지 못했습니다.'))
      return false
    } finally {
      if (!quiet) setLoading(false)
    }
  }, [filterKey]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 5000)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    generation.current += 1
    const current = generation.current
    let eventSource: EventSource | null = null
    let pollTimer: number | null = null
    let retryTimer: number | null = null
    let pollingStarted = false
    setConnection('연결 중')
    setLoading(true)
    setError(null)
    usageOverlay.current = {}

    const poll = async () => {
      if (current !== generation.current) return
      const ok = await load(Boolean(data))
      if (current === generation.current) setConnection(ok ? '주기 조회' : '연결 끊김')
    }
    const startPolling = () => {
      if (pollingStarted || current !== generation.current) return
      pollingStarted = true
      void poll()
      pollTimer = window.setInterval(() => void poll(), 15000)
    }
    const connect = () => {
      if (typeof EventSource === 'undefined') { startPolling(); return }
      eventSource = new EventSource(apiUrl(makePath(filters, true)), { withCredentials: true })
      eventSource.onopen = () => {
        if (current !== generation.current) return
        setConnection('실시간')
        setError(null)
        setLoading(false)
      }
      eventSource.onmessage = (event) => {
        if (current !== generation.current) return
        try {
          const next = normalize(JSON.parse(event.data))
          if (next) setData({ ...next, ...usageOverlay.current })
          setLastUpdated(new Date())
          setConnection('실시간')
          setError(null)
          setLoading(false)
        } catch {
          setError(new Error('실시간 데이터 형식이 올바르지 않습니다.'))
        }
      }
      eventSource.onerror = () => {
        eventSource?.close()
        eventSource = null
        startPolling()
        retryTimer = window.setTimeout(() => {
          if (current !== generation.current) return
          if (pollTimer) window.clearInterval(pollTimer)
          pollTimer = null
          pollingStarted = false
          setConnection('연결 중')
          connect()
        }, 60000)
      }
    }
    void load(true)
    connect()
    return () => {
      eventSource?.close()
      if (pollTimer) window.clearInterval(pollTimer)
      if (retryTimer) window.clearTimeout(retryTimer)
    }
  }, [filterKey, load]) // eslint-disable-line react-hooks/exhaustive-deps

  const transportStale = Boolean(lastUpdated && now - lastUpdated.getTime() > 45000)
  const sourceStale = Boolean(data?.stale)
  return {
    data,
    loading,
    error,
    connection,
    lastUpdated,
    stale: transportStale || sourceStale,
    transportStale,
    sourceStale,
    reload: () => load(false),
  }
}
