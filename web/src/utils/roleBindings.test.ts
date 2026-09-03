import { describe, expect, it } from 'vitest'
import { buildRoleBindingPayload, roleBindingsFromUser } from './roleBindings'

describe('roleBindingsFromUser', () => {
  it('범위형 역할 바인딩을 폼 구조로 변환한다', () => {
    expect(roleBindingsFromUser({
      role_bindings: [{
        role_id: 7,
        scope_mode: 'restricted',
        scopes: [
          { type: 'hub', value: '12' },
          { type: 'department', value: 'AI 플랫폼' },
        ],
      }],
    }, [])).toEqual([{
      role_id: 7,
      scope_mode: 'restricted',
      hub_ids: ['12'],
      departments: ['AI 플랫폼'],
    }])
  })

  it('구형 roles 응답은 전역 바인딩으로만 읽는다', () => {
    expect(roleBindingsFromUser(
      { roles: ['operator'] },
      [{ id: 3, key: 'operator' }, { id: 4, key: 'auditor' }],
    )).toEqual([{ role_id: 3, scope_mode: 'global', hub_ids: [], departments: [] }])
  })
})

describe('buildRoleBindingPayload', () => {
  it('태그를 정리하고 API scopes 계약으로 직렬화한다', () => {
    expect(buildRoleBindingPayload([{
      role_id: 5,
      scope_mode: 'restricted',
      hub_ids: ['2', '2', ' 8 '],
      departments: [' AI ', 'AI', '데이터'],
    }])).toEqual([{
      role_id: 5,
      scope_mode: 'restricted',
      scopes: [
        { type: 'hub', value: '2' },
        { type: 'hub', value: '8' },
        { type: 'department', value: 'AI' },
        { type: 'department', value: '데이터' },
      ],
    }])
  })

  it('전역 할당에서는 남아 있는 범위 입력을 전송하지 않는다', () => {
    expect(buildRoleBindingPayload([{
      role_id: 1,
      scope_mode: 'global',
      hub_ids: ['2'],
      departments: ['AI'],
    }])).toEqual([{ role_id: 1, scope_mode: 'global', scopes: [] }])
  })

  it('빈 제한 범위, 잘못된 Hub ID, 중복 역할을 거부한다', () => {
    expect(() => buildRoleBindingPayload([{ role_id: 1, scope_mode: 'restricted' }]))
      .toThrow('제한 범위에는 Hub ID 또는 부서를 하나 이상 입력해 주세요.')
    expect(() => buildRoleBindingPayload([{ role_id: 1, scope_mode: 'restricted', hub_ids: ['hub-a'] }]))
      .toThrow('Hub ID는 1 이상의 숫자로 입력해 주세요.')
    expect(() => buildRoleBindingPayload([
      { role_id: 1, scope_mode: 'global' },
      { role_id: 1, scope_mode: 'restricted', departments: ['AI'] },
    ])).toThrow('같은 역할은 한 사용자에게 한 번만 할당할 수 있습니다.')
  })
})
