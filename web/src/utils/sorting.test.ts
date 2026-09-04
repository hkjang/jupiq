import { describe, expect, it } from 'vitest'
import { compareValues, sortKindForFormat, sorterFor } from './sorting'

describe('컬럼 정렬', () => {
  it('숫자·날짜·문자·불리언을 종류에 맞게 비교한다', () => {
    expect(compareValues(2, 10, 'number')).toBeLessThan(0)
    expect(compareValues('2', '10', 'number')).toBeLessThan(0)
    expect(compareValues('2026-09-04T00:00:00Z', '2026-09-03T00:00:00Z', 'date')).toBeGreaterThan(0)
    expect(compareValues('user2', 'user10', 'text')).toBeLessThan(0)
    expect(compareValues(true, false, 'boolean')).toBeGreaterThan(0)
  })

  it('값이 없는 행은 방향과 무관하게 뒤로 보낸다', () => {
    const rows = [{ v: undefined }, { v: 3 }, { v: null }, { v: 1 }]
    const sorter = sorterFor(['v'], 'number')
    expect([...rows].sort((a, b) => sorter(a, b, 'ascend')).map((r) => r.v)).toEqual([1, 3, undefined, null])
    // antd calls sorter(a, b, 'descend') and negates the result.
    expect([...rows].sort((a, b) => -sorter(a, b, 'descend')).map((r) => r.v)).toEqual([3, 1, undefined, null])
  })

  it('컬럼 format에서 정렬 종류를 고른다', () => {
    expect(sortKindForFormat('bytes')).toBe('number')
    expect(sortKindForFormat('percent')).toBe('number')
    expect(sortKindForFormat('date')).toBe('date')
    expect(sortKindForFormat('status')).toBe('text')
    expect(sortKindForFormat(undefined)).toBe('text')
  })
})
