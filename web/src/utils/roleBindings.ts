import type { ApiRecord } from '../types'

export type RoleScopeMode = 'global' | 'restricted'
export type RoleScopeType = 'hub' | 'department'

export interface RoleScopeClause {
  type: RoleScopeType
  value: string
}

export interface RoleBindingDraft {
  role_id?: number
  scope_mode?: RoleScopeMode
  hub_ids?: string[]
  departments?: string[]
}

export interface RoleBindingsFormValue {
  bindings?: RoleBindingDraft[]
}

export interface RoleBindingPayload {
  role_id: number
  scope_mode: RoleScopeMode
  scopes: RoleScopeClause[]
}

function positiveInteger(value: unknown) {
  const parsed = typeof value === 'number' ? value : Number(String(value ?? '').trim())
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0
}

function cleanStrings(value: unknown) {
  if (!Array.isArray(value)) return []
  return [...new Set(value.map((item) => String(item).trim()).filter(Boolean))]
}

/** Convert the API representation into the tag-oriented form representation. */
export function roleBindingsFromUser(user: ApiRecord, roles: ApiRecord[]): RoleBindingDraft[] {
  if (Array.isArray(user.role_bindings)) {
    return user.role_bindings.flatMap((raw): RoleBindingDraft[] => {
      if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
      const binding = raw as ApiRecord
      const roleID = positiveInteger(binding.role_id)
      if (!roleID) return []
      const scopes = Array.isArray(binding.scopes) ? binding.scopes : []
      const hubs: string[] = []
      const departments: string[] = []
      for (const rawScope of scopes) {
        if (!rawScope || typeof rawScope !== 'object' || Array.isArray(rawScope)) continue
        const scope = rawScope as ApiRecord
        const value = String(scope.value ?? '').trim()
        if (!value) continue
        if (scope.type === 'hub') hubs.push(value)
        if (scope.type === 'department') departments.push(value)
      }
      return [{
        role_id: roleID,
        scope_mode: binding.scope_mode === 'restricted' ? 'restricted' : 'global',
        hub_ids: cleanStrings(hubs),
        departments: cleanStrings(departments),
      }]
    })
  }

  // Before scoped bindings existed, user roles were always global. This fallback is
  // intentionally read-only compatibility; saves always use the bindings contract.
  const legacyRoleKeys = cleanStrings(user.roles)
  return roles.flatMap((role): RoleBindingDraft[] => {
    const roleID = positiveInteger(role.id)
    return roleID && legacyRoleKeys.includes(String(role.key))
      ? [{ role_id: roleID, scope_mode: 'global', hub_ids: [], departments: [] }]
      : []
  })
}

/** Validate and serialize form values to the scope-aware API contract. */
export function buildRoleBindingPayload(bindings: RoleBindingDraft[] = []): RoleBindingPayload[] {
  const seenRoleIDs = new Set<number>()
  return bindings.map((binding) => {
    const roleID = positiveInteger(binding.role_id)
    if (!roleID) throw new Error('각 할당의 역할을 선택해 주세요.')
    if (seenRoleIDs.has(roleID)) throw new Error('같은 역할은 한 사용자에게 한 번만 할당할 수 있습니다.')
    seenRoleIDs.add(roleID)

    const scopeMode = binding.scope_mode
    if (scopeMode !== 'global' && scopeMode !== 'restricted') throw new Error('역할 적용 범위를 선택해 주세요.')
    if (scopeMode === 'global') return { role_id: roleID, scope_mode: scopeMode, scopes: [] }

    const hubIDs = cleanStrings(binding.hub_ids)
    if (hubIDs.some((value) => !/^[1-9]\d*$/.test(value) || !Number.isSafeInteger(Number(value)))) {
      throw new Error('Hub ID는 1 이상의 숫자로 입력해 주세요.')
    }
    const normalizedHubIDs = [...new Set(hubIDs.map((value) => String(Number(value))))]
    const departments = cleanStrings(binding.departments)
    if (normalizedHubIDs.length === 0 && departments.length === 0) {
      throw new Error('제한 범위에는 Hub ID 또는 부서를 하나 이상 입력해 주세요.')
    }
    return {
      role_id: roleID,
      scope_mode: scopeMode,
      scopes: [
        ...normalizedHubIDs.map((value): RoleScopeClause => ({ type: 'hub', value })),
        ...departments.map((value): RoleScopeClause => ({ type: 'department', value })),
      ],
    }
  })
}
