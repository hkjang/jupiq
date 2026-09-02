import { useCallback, useEffect, useState } from 'react'
import { requestList } from '../api/client'
import type { ApiRecord, PageMeta } from '../types'

export function useList<T extends ApiRecord>(path: string) {
  const [data, setData] = useState<T[]>([])
  const [meta, setMeta] = useState<PageMeta>({ total: 0 })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<Error | null>(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const result = await requestList<T>(path)
      setData(result.data)
      setMeta(result.meta)
    } catch (caught) {
      setError(caught instanceof Error ? caught : new Error('목록을 불러오지 못했습니다.'))
    } finally {
      setLoading(false)
    }
  }, [path])

  useEffect(() => { void reload() }, [reload])
  return { data, meta, loading, error, reload, setData }
}
