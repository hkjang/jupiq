import { describe, expect, it } from 'vitest'
import { canAccessNavigationPath, canOpenUserDetail, canUseGlobalSearch, firstAccessiblePath, isFeatureMenuVisible, selectedNavigationPath, serviceVersionLabel } from './navigation'

describe('navigation state', () => {
  it('새로고침된 하위 URL에서 해당 상위 메뉴를 선택한다', () => {
    expect(selectedNavigationPath('/servers/123?tab=metrics')).toBe('/servers')
    expect(selectedNavigationPath('/users/user01')).toBe('/users')
    expect(selectedNavigationPath('/admin/settings/integrations')).toBe('/admin/settings')
  })

  it('기본 OFF 선택 기능의 메뉴를 숨긴다', () => {
    const features = { gpuMonitoring: false, llmUsageMonitoring: false, approvalWorkflow: false }
    expect(isFeatureMenuVisible('/gpus', features)).toBe(false)
    expect(isFeatureMenuVisible('/approvals', features)).toBe(false)
    expect(isFeatureMenuVisible('/servers', features)).toBe(true)
  })

  it('프로필과 로그인 화면용 버전 라벨을 일관되게 만든다', () => {
    expect(serviceVersionLabel('1.0.0')).toBe('v1.0.0')
    expect(serviceVersionLabel()).toBe('v확인 불가')
  })

  it('역할이 접근할 수 있는 첫 화면을 선택한다', () => {
    const features = { gpuMonitoring: false, llmUsageMonitoring: false, approvalWorkflow: false }
    expect(firstAccessiblePath(['servers:read'], features)).toBe('/servers')
    expect(firstAccessiblePath(['settings:read'], features)).toBe('/admin/settings')
    expect(firstAccessiblePath(['notification:read'], features)).toBe('/notifications')
    expect(firstAccessiblePath(['profiles:read'], features)).toBe('/profiles')
    expect(firstAccessiblePath(['profile:read'], features)).toBe('/personal')
    expect(firstAccessiblePath([], features)).toBe('/personal')
  })

  it('개인화 전용 사용자는 관리자 통합 검색을 노출하지 않는다', () => {
    expect(canUseGlobalSearch(['profile:read', 'profile:keys'])).toBe(false)
    expect(canUseGlobalSearch(['hubs:read'])).toBe(true)
    expect(canUseGlobalSearch(['servers:*'])).toBe(true)
  })

  it('범위형 권한은 Hub·사용자·서버 목록에만 사용하고 전역 화면으로 승격하지 않는다', () => {
    const features = { gpuMonitoring: true, llmUsageMonitoring: true, approvalWorkflow: true }
    const effective = ['hubs:read', 'users:read', 'servers:*', 'settings:read', 'dashboard:read', 'gpu:read']
    const global: string[] = []
    expect(canAccessNavigationPath('/hubs', effective, features, global)).toBe(true)
    expect(canAccessNavigationPath('/users', effective, features, global)).toBe(true)
    expect(canAccessNavigationPath('/servers', effective, features, global)).toBe(true)
    expect(canAccessNavigationPath('/dashboard', effective, features, global)).toBe(false)
    expect(canAccessNavigationPath('/gpus', effective, features, global)).toBe(false)
    expect(canAccessNavigationPath('/admin/settings', effective, features, global)).toBe(false)
    expect(firstAccessiblePath(effective, features, global)).toBe('/hubs')
    expect(canUseGlobalSearch(global)).toBe(false)
    expect(canOpenUserDetail(global)).toBe(false)
    expect(canOpenUserDetail(['users:*'])).toBe(true)
  })

  it('AI 채팅 또는 활성화된 LLM 사용량 권한이 있을 때만 AI 운영 메뉴에 접근한다', () => {
    const off = { gpuMonitoring: false, llmUsageMonitoring: false, approvalWorkflow: false }
    const on = { ...off, llmUsageMonitoring: true }
    expect(canAccessNavigationPath('/ai-ops', ['ai:chat'], off)).toBe(true)
    expect(canAccessNavigationPath('/ai-ops', ['usage:read'], off)).toBe(false)
    expect(firstAccessiblePath(['usage:read'], off)).toBe('/personal')
    expect(canAccessNavigationPath('/ai-ops', ['usage:read'], on)).toBe(true)
    expect(firstAccessiblePath(['usage:read'], on)).toBe('/ai-ops')
  })
})
