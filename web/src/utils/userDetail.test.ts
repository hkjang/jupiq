import { describe, expect, it } from 'vitest'
import { normalizeUserDetail, periodSummary } from './userDetail'

describe('normalizeUserDetail', () => {
  it('사용자 360 API의 중첩 목록과 기간 데이터를 정규화한다', () => {
    const view = normalizeUserDetail({
      username: 'user01',
      user: { display_name: '사용자 01' },
      hubs: [{ hub_name: '업무망' }],
      servers: { current: [{ id: 1 }], history: [{ id: 2 }], meta: { total: 2 } },
      timeline: { items: [{ action: 'server.start' }], meta: { total: 1 } },
      usage: { day: { runtime_seconds: 60 }, week: {}, month: {} },
      llm_usage: { included: true, summary: { calls: 4 } },
      feature_enabled: { llm_usage_monitoring: true },
      data_policy: { metadata_only: true },
    })

    expect(view.username).toBe('user01')
    expect(view.currentServers).toHaveLength(1)
    expect(view.serverHistory[0].id).toBe(2)
    expect(view.timeline).toHaveLength(1)
    expect(view.usage.day.runtime_seconds).toBe(60)
    expect(view.featureEnabled.llm_usage_monitoring).toBe(true)
  })

  it('누락된 선택 데이터는 안전한 빈값으로 만든다', () => {
    const view = normalizeUserDetail({ username: 'hub-only-user', user: null })
    expect(view.user).toBeNull()
    expect(view.hubs).toEqual([])
    expect(view.currentServers).toEqual([])
    expect(periodSummary({ summary: { calls: 3 }, stale: true })).toEqual({ summary: { calls: 3 }, stale: true, calls: 3 })
  })
})
