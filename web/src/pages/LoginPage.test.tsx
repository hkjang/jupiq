import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { LoginPage } from './LoginPage'

vi.mock('@ant-design/icons', () => ({
  LockOutlined: () => null,
  SafetyCertificateOutlined: () => null,
  UserOutlined: () => null,
}))

vi.mock('antd', async () => {
  const React = await import('react')
  const container = ({ children }: React.PropsWithChildren) => React.createElement('div', null, children)
  const Form = Object.assign(
    ({ children }: React.PropsWithChildren) => React.createElement('form', null, children),
    { Item: container },
  )
  const Input = Object.assign(
    () => React.createElement('input'),
    { Password: () => React.createElement('input', { type: 'password' }) },
  )
  return {
    Alert: ({ message, description }: { message?: React.ReactNode; description?: React.ReactNode }) => React.createElement('div', { role: 'alert' }, React.createElement('strong', null, message), description),
    Button: ({ children, htmlType }: React.PropsWithChildren<{ htmlType?: 'button' | 'submit' | 'reset' }>) => React.createElement('button', { type: htmlType }, children),
    Card: container,
    Divider: container,
    Flex: container,
    Form,
    Input,
    Space: container,
    Tag: container,
    Typography: { Text: container, Title: container, Paragraph: container },
  }
})

vi.mock('../auth/AuthContext', () => ({
  useAuth: () => ({
    user: null,
    login: vi.fn(),
    oidc: { enabled: false },
    version: { version: '1.1.0' },
    loading: false,
  }),
}))

describe('LoginPage accessibility', () => {
  it('본문 건너뛰기 링크가 가리키는 포커스 가능한 main을 제공한다', () => {
    render(<MemoryRouter><LoginPage /></MemoryRouter>)
    const main = screen.getByRole('main')
    expect(main).toHaveAttribute('id', 'main-content')
    expect(main).toHaveAttribute('tabindex', '-1')
    main.focus()
    expect(main).toHaveFocus()
  })
})

describe('LoginPage SSO 표시', () => {
  it('sso=limited이면 요청 상한 안내를 띄운다', () => {
    render(<MemoryRouter initialEntries={['/login?sso=limited&return_to=%2Fhubs']}><LoginPage /></MemoryRouter>)
    expect(screen.getByText('SSO 로그인 요청이 너무 많습니다')).toBeInTheDocument()
    expect(screen.queryByText('SSO 로그인이 완료되지 않았습니다')).not.toBeInTheDocument()
  })

  it('sso=none은 정상 결과이므로 아무 안내도 띄우지 않는다', () => {
    render(<MemoryRouter initialEntries={['/login?sso=none']}><LoginPage /></MemoryRouter>)
    expect(screen.queryByText('SSO 로그인 요청이 너무 많습니다')).not.toBeInTheDocument()
    expect(screen.queryByText('SSO 로그인이 완료되지 않았습니다')).not.toBeInTheDocument()
  })
})
