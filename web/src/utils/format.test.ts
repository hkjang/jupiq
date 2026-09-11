import { describe, expect, it } from 'vitest'
import { formatCoreHours, formatGbHours, formatMetricNumber, formatObservedRatio, normalizePercentValue } from './format'

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

describe('formatMetricNumber', () => {
  it('KPI 숫자를 소수 두 자리로 정리한다', () => {
    expect(formatMetricNumber(0.004166666666667)).toBe('0.0042')
    expect(formatMetricNumber(1.23456)).toBe('1.23')
    expect(formatMetricNumber(1234.5678)).toBe('1,234.57')
    expect(formatMetricNumber(0.5)).toBe('0.5')
    expect(formatMetricNumber(12)).toBe('12')
    expect(formatMetricNumber(0)).toBe('0')
    expect(formatMetricNumber('7.891')).toBe('7.89')
    expect(formatMetricNumber('수집 불가')).toBe('수집 불가')
  })
})

describe('소비량 표기', () => {
  it('core-hours와 GB-hours를 읽기 쉬운 자릿수로 만든다', () => {
    expect(formatCoreHours(4)).toBe('4 Core·h')
    expect(formatCoreHours(0.0416666)).toBe('0.04 Core·h')
    expect(formatCoreHours(1234.56)).toBe('1,235 Core·h')
    expect(formatGbHours(2)).toBe('2 GB·h')
    expect(formatCoreHours('수집 불가')).toBe('수집 불가')
  })

  it('관측 비율을 백분율로 보여준다', () => {
    expect(formatObservedRatio(1)).toBe('100%')
    expect(formatObservedRatio(0.42)).toBe('42%')
    expect(formatObservedRatio(undefined)).toBe('—')
  })
})
