import { useCallback, useEffect, useRef, useState } from 'react'
import { requestList } from '../api/client'
import type { ApiRecord, PageMeta } from '../types'

export function useList<T extends ApiRecord>(path: string) {
  const [data, setData] = useState<T[]>([])
  const [meta, setMeta] = useState<PageMeta>({ total: 0 })
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const requestSequence = useRef(0)
  const loaded = useRef(false)

  const reload = useCallback(async () => {
    const requestID = ++requestSequence.current
    if (!path) {
      if (requestID !== requestSequence.current) return
      loaded.current = false
      setData([])
      setMeta({ total: 0 })
      setError(null)
      setLoading(false)
      setRefreshing(false)
      return
    }
    // A reload keeps the current rows on screen and only marks the table as
    // refreshing. Swapping a populated table for a skeleton changes the page
    // height and makes the view jump on every search, action or refresh.
    if (loaded.current) setRefreshing(true)
    else setLoading(true)
    setError(null)
    try {
      const result = await requestList<T>(path)
      if (requestID !== requestSequence.current) return
      loaded.current = true
      setData(result.data)
      setMeta(result.meta)
    } catch (caught) {
      if (requestID !== requestSequence.current) return
      setError(caught instanceof Error ? caught : new Error('목록을 불러오지 못했습니다.'))
    } finally {
      if (requestID === requestSequence.current) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [path])

  useEffect(() => {
    void reload()
    return () => { requestSequence.current += 1 }
  }, [reload])
  return { data, meta, loading, refreshing, error, reload, setData }
}
