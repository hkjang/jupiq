import { App } from 'antd'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ResourceListPage, type ResourceColumn, type ResourceField, type RowAction } from './ResourceListPage'
import type { ApiRecord } from '../types'

const request = vi.fn()
const requestList = vi.fn()

vi.mock('../api/client', () => ({
  request: (...args: unknown[]) => request(...args),
  requestList: (...args: unknown[]) => requestList(...args),
  jsonBody: (value: unknown) => JSON.stringify(value),
}))

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: { username: 'admin', permissions: ['*'], global_permissions: ['*'] },
    hasGlobalPermission: () => true,
  }),
}))

// Audit, cost and GPU rows arrive without any identifier field.
const identifierlessRows: ApiRecord[] = [
  { action: 'auth.login', actor: 'user01' },
  { action: 'server.stop', actor: 'user02' },
]

const columns: ResourceColumn[] = [
  { title: '작업', keys: ['action'] },
  { title: '행위자', keys: ['actor'] },
]

const fields: ResourceField[] = [{ name: 'action', label: '작업' }]

const rowActions: RowAction[] = [
  { key: 'retry', label: '다시 실행', path: () => '/audit/retry' },
]

function renderPage(props: Partial<Parameters<typeof ResourceListPage>[0]> = {}) {
  return render(
    <MemoryRouter>
      <App>
        <ResourceListPage
          title="감사 로그"
          description="테스트"
          endpoint="/audit"
          emptyDescription="감사 로그가 없습니다."
          columns={columns}
          rowActions={rowActions}
          fields={fields}
          {...props}
        />
      </App>
    </MemoryRouter>,
  )
}

// A full antd Table plus userEvent interaction takes a couple of seconds
// locally and more on a shared CI runner, so these get an explicit budget
// instead of the 5s default they were only just fitting inside.
const INTERACTION_TIMEOUT = 30_000

describe('ResourceListPage 작업 컬럼', () => {
  beforeEach(() => {
    request.mockReset()
    requestList.mockReset()
    requestList.mockResolvedValue({ data: identifierlessRows, meta: { total: identifierlessRows.length } })
  })

  it('식별자가 없는 행에서도 작업 메뉴가 로딩 상태로 잠기지 않는다', async () => {
    renderPage()
    const triggers = await screen.findAllByLabelText('작업 메뉴 열기')
    expect(triggers).toHaveLength(identifierlessRows.length)
    for (const trigger of triggers) {
      expect(trigger.className).not.toContain('ant-btn-loading')
      expect(trigger).toBeEnabled()
    }
  }, INTERACTION_TIMEOUT)

  it('작업 버튼을 누르면 조회·수정 메뉴가 열리고 수정 화면이 나타난다', async () => {
    const user = userEvent.setup({ delay: null })
    renderPage()
    const [trigger] = await screen.findAllByLabelText('작업 메뉴 열기')
    await user.click(trigger)
    await waitFor(() => expect(screen.getByText('수정')).toBeVisible())
    expect(screen.getByText('다시 실행')).toBeInTheDocument()
    await user.click(screen.getByText('수정'))
    await waitFor(() => expect(screen.getByText('감사 로그 수정')).toBeVisible())
  }, INTERACTION_TIMEOUT)

  it('행 작업 중에도 목록을 언마운트하지 않고 유지한다', async () => {
    const user = userEvent.setup({ delay: null })
    let resolveAction: (value: unknown) => void = () => {}
    request.mockImplementation(() => new Promise((resolve) => { resolveAction = resolve }))
    renderPage()
    const [trigger] = await screen.findAllByLabelText('작업 메뉴 열기')
    await user.click(trigger)
    await user.click(await screen.findByText('다시 실행'))
    expect(screen.getByText('auth.login')).toBeInTheDocument()
    resolveAction({ success: true })
    await waitFor(() => expect(screen.getByText('auth.login')).toBeInTheDocument())
  }, INTERACTION_TIMEOUT)
})
