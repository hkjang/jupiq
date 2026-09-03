import { describe, expect, it } from 'vitest'
import { hasAnyGrantedPermission, hasGrantedPermission, hasPermissionForTargetMode } from './permissions'

describe('backend permission matching', () => {
  it('정확한 권한과 namespace wildcard를 허용한다', () => {
    expect(hasGrantedPermission(['hubs:read'], 'hubs:read')).toBe(true)
    expect(hasGrantedPermission(['servers:*'], 'servers:operate')).toBe(true)
    expect(hasGrantedPermission(['servers:read'], 'servers:operate')).toBe(false)
  })

  it('전체 권한과 any-of 권한을 처리한다', () => {
    expect(hasGrantedPermission(['*'], 'settings:write')).toBe(true)
    expect(hasAnyGrantedPermission(['usage:read'], ['ai:chat', 'usage:read'])).toBe(true)
  })

  it('대상 범위형 작업에서만 합산 권한을 사용한다', () => {
    const effective = ['hubs:write']
    const global: string[] = []
    expect(hasPermissionForTargetMode(effective, global, 'hubs:write', true)).toBe(true)
    expect(hasPermissionForTargetMode(effective, global, 'hubs:write', false)).toBe(false)
  })
})
