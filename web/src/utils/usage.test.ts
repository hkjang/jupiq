import { describe, expect, it } from 'vitest'
import { buildUsageOverlay } from './usage'

describe('buildUsageOverlay', () => {
  it('aggregates usage metrics by bucket', () => {
    const overlay = buildUsageOverlay({
      dau: 3, wau: 7, mau: 12,
      trend: [
        { bucket: '2026-09-02T10:00:00Z', group: 'u1', metric: 'login_count', average: 1 },
        { bucket: '2026-09-02T10:00:00Z', group: 'u2', metric: 'login_count', average: 2 },
        { bucket: '2026-09-02T10:00:00Z', group: 'u1', metric: 'cpu_cores', average: 1.5 },
      ],
    }, { range: 'day' })
    expect(overlay.usage_trend).toEqual([{ timestamp: '2026-09-02T10:00:00Z', label: '2026-09-02T10:00:00Z', login_count: 3, cpu_cores: 1.5 }])
  })

  it('honors the selected historical dimension', () => {
    const overlay = buildUsageOverlay({ trend: [
      { bucket: '2026-09-02T00:00:00Z', group: '업무망', metric: 'server_starts', average: 4 },
      { bucket: '2026-09-02T00:00:00Z', group: '개발망', metric: 'server_starts', average: 9 },
    ] }, { range: 'week', network: '업무망' })
    expect(overlay.usage_trend).toEqual([{ timestamp: '2026-09-02T00:00:00Z', label: '2026-09-02T00:00:00Z', server_starts: 4 }])
  })
})
