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
    Alert: container,
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
