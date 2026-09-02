import { describe, expect, it } from 'vitest'
import { isFeatureMenuVisible, selectedNavigationPath, serviceVersionLabel } from './navigation'

describe('navigation state', () => {
  it('새로고침된 하위 URL에서 해당 상위 메뉴를 선택한다', () => {
    expect(selectedNavigationPath('/servers/123?tab=metrics')).toBe('/servers')
    expect(selectedNavigationPath('/users/user01')).toBe('/users')
    expect(selectedNavigationPath('/admin/settings/integrations')).toBe('/admin/settings')
  })

  it('기본 OFF 선택 기능의 메뉴를 숨긴다', () => {
    const features = { gpuMonitoring: false, approvalWorkflow: false }
    expect(isFeatureMenuVisible('/gpus', features)).toBe(false)
    expect(isFeatureMenuVisible('/approvals', features)).toBe(false)
    expect(isFeatureMenuVisible('/servers', features)).toBe(true)
  })

  it('프로필과 로그인 화면용 버전 라벨을 일관되게 만든다', () => {
    expect(serviceVersionLabel('1.0.0')).toBe('v1.0.0')
    expect(serviceVersionLabel()).toBe('v확인 불가')
  })
})
