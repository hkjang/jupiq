import { useCallback, useEffect, useState } from 'react'
import { request } from '../api/client'

export function useApi<T>(path: string) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<Error | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setData(await request<T>(path))
    } catch (caught) {
      setError(caught instanceof Error ? caught : new Error('데이터를 불러오지 못했습니다.'))
    } finally {
      setLoading(false)
    }
  }, [path])

  useEffect(() => { void reload() }, [reload])
  return { data, loading, error, reload, setData }
}
