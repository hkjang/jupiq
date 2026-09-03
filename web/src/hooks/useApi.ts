import { useCallback, useEffect, useRef, useState } from 'react'
import { request } from '../api/client'

export function useApi<T>(path: string) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const requestSequence = useRef(0)
  const loaded = useRef(false)

  const reload = useCallback(async () => {
    const requestID = ++requestSequence.current
    // Keep the rendered content mounted while refreshing so the layout does not
    // collapse into a skeleton and shift the page under the pointer.
    if (loaded.current) setRefreshing(true)
    else setLoading(true)
    setError(null)
    try {
      const next = await request<T>(path)
      if (requestID !== requestSequence.current) return
      loaded.current = true
      setData(next)
    } catch (caught) {
      if (requestID !== requestSequence.current) return
      setError(caught instanceof Error ? caught : new Error('데이터를 불러오지 못했습니다.'))
    } finally {
      if (requestID === requestSequence.current) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [path])

  useEffect(() => {
    // A different path is a different resource: drop the old payload so the page
    // never renders the previous record under the new heading.
    loaded.current = false
    setData(null)
    void reload()
    return () => { requestSequence.current += 1 }
  }, [reload])

  return { data, loading, refreshing, error, reload, setData }
}
