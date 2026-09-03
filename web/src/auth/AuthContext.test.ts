import { describe, expect, it } from 'vitest'
import { normalizeUser } from './AuthContext'

describe('current user permission contract', () => {
  it('keeps an explicit empty global grant distinct from scoped permissions', () => {
    const user = normalizeUser({
      username: 'scoped-operator',
      permissions: ['hubs:read', 'servers:operate'],
      global_permissions: [],
      scoped_permissions: ['hubs:read', 'servers:operate'],
    })
    expect(user.permissions).toEqual(['hubs:read', 'servers:operate'])
    expect(user.global_permissions).toEqual([])
    expect(user.scoped_permissions).toEqual(['hubs:read', 'servers:operate'])
  })

  it('supports the pre-scope payload as a global compatibility contract', () => {
    const user = normalizeUser({ username: 'legacy', permissions: ['dashboard:read'] })
    expect(user.global_permissions).toEqual(['dashboard:read'])
    expect(user.scoped_permissions).toEqual([])
  })
})
