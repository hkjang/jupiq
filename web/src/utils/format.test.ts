import { describe, expect, it } from 'vitest'
import { normalizePercentValue } from './format'

describe('normalizePercentValue', () => {
  it('0..1 비율을 퍼센트 값으로 변환한다', () => {
    expect(normalizePercentValue(0.9)).toBe(90)
    expect(normalizePercentValue(1)).toBe(100)
  })

  it('이미 퍼센트인 값은 유지한다', () => {
    expect(normalizePercentValue(97.5)).toBe(97.5)
    expect(normalizePercentValue(0)).toBe(0)
  })
})
