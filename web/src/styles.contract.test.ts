import { describe, expect, it } from 'vitest'
import css from './styles.css?raw'
const rule = (selector: string) => {
  const match = css.match(new RegExp(`(^|\\})\\s*${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]*)\\}`, 'm'))
  return match ? match[2] : ''
}

// antd renders every Dropdown, Select and Tooltip popup onto <body>. If the
// document can scroll, a popup opened near the bottom adds a scrollbar, the
// layout narrows by its width, the popup is realigned, it fits again and the
// scrollbar disappears - the page oscillates under the pointer and the action
// menu moves away before it can be clicked. A scrollable document also lets
// antd's scroll locker apply `width: calc(100% - <scrollbar>px)` to <body>,
// which shifts the whole shell sideways whenever a Modal or Drawer opens.
// Headless browsers use zero-width overlay scrollbars, so neither symptom shows
// up in the rendering tests - these invariants are the guard instead.
describe('앱 셸 스크롤 계약', () => {
  it('문서에는 스크롤바가 생기지 않는다', () => {
    expect(rule('html')).toMatch(/overflow:\s*hidden/)
    expect(rule('body')).toMatch(/overflow:\s*hidden/)
  })

  it('스크롤은 #root와 셸 본문이 담당한다', () => {
    expect(rule('#root')).toMatch(/overflow-y:\s*auto/)
    expect(rule('#root')).toMatch(/height:\s*100%/)
    expect(rule('.app-content')).toMatch(/overflow-y:\s*auto/)
  })

  it('antd App wrapper가 높이 사슬을 끊지 않는다', () => {
    // Without this the percentage chain stops at antd's <App> div and the shell
    // grows to its content instead of filling the viewport.
    expect(rule('#root > .ant-app')).toMatch(/height:\s*100%/)
    expect(rule('.app-layout')).toMatch(/height:\s*100%/)
  })
})
