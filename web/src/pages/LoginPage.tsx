import { LockOutlined, SafetyCertificateOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, Button, Card, Divider, Flex, Form, Input, Space, Tag, Typography } from 'antd'
import { useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { serviceVersionLabel } from '../utils/navigation'

interface LoginForm { username: string; password: string }

export function LoginPage() {
  const { user, login, oidc, version, loading } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const from = (location.state as { from?: string } | null)?.from || '/'

  if (!loading && user) return <Navigate to={from} replace />

  const submit = async (values: LoginForm) => {
    setSubmitting(true)
    setError('')
    try {
      await login(values.username, values.password)
      navigate(from, { replace: true })
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : '로그인하지 못했습니다.')
    } finally {
      setSubmitting(false)
    }
  }

  const startOidc = () => {
    const returnTo = encodeURIComponent(from)
    window.location.assign(`/api/v1/auth/oidc/login?return_to=${returnTo}`)
  }

  return (
    <main id="main-content" className="login-page" tabIndex={-1}>
      <section className="login-story" aria-label="서비스 소개">
        <img src="/logo.svg" alt="jupiq" className="login-logo" />
        <Tag color="blue">AI WORKSPACE CONTROL PLANE</Tag>
        <Typography.Title>분리된 AI 워크스페이스를<br />하나의 운영 시야로.</Typography.Title>
        <Typography.Paragraph>
          JupyterHub, GPU, 사용자, 정책과 비용을 안전하게 연결하고 운영 흐름을 한곳에서 관리합니다.
        </Typography.Paragraph>
        <div className="login-feature-grid">
          <div><strong>통합 관제</strong><span>망별 상태와 자원 흐름</span></div>
          <div><strong>안전한 운영</strong><span>승인, RBAC, 감사 추적</span></div>
          <div><strong>효율 분석</strong><span>실시간 이용량과 자원 효율</span></div>
        </div>
      </section>
      <section className="login-panel" aria-label="로그인">
        <Card className="login-card" variant="borderless">
          <Space direction="vertical" size={4} className="login-title">
            <Typography.Text type="secondary">관리 포털</Typography.Text>
            <Typography.Title level={2}>jupiq에 로그인</Typography.Title>
          </Space>
          {error && <Alert type="error" showIcon message="로그인 실패" description={error} closable onClose={() => setError('')} />}
          {oidc.enabled && (
            <>
              <Button size="large" block type="primary" icon={<SafetyCertificateOutlined />} onClick={startOidc}>
                {oidc.provider_name || 'Keycloak'} SSO로 로그인
              </Button>
              <Divider plain>또는 비상 관리자 계정</Divider>
            </>
          )}
          <Form<LoginForm> layout="vertical" size="large" onFinish={submit} requiredMark={false}>
            <Form.Item label="사용자 ID" name="username" rules={[{ required: true, message: '사용자 ID를 입력해 주세요.' }]}>
              <Input prefix={<UserOutlined />} autoComplete="username" placeholder="사용자 ID" autoFocus />
            </Form.Item>
            <Form.Item label="비밀번호" name="password" rules={[{ required: true, message: '비밀번호를 입력해 주세요.' }]}>
              <Input.Password prefix={<LockOutlined />} autoComplete="current-password" placeholder="비밀번호" />
            </Form.Item>
            <Button block type="primary" htmlType="submit" loading={submitting}>로그인</Button>
          </Form>
          <Flex className="login-version" justify="center" gap={8} wrap>
            <span>jupiq {serviceVersionLabel(version.version)}</span>
            {version.commit && version.commit !== 'unknown' && <span>· {version.commit.slice(0, 8)}</span>}
          </Flex>
        </Card>
      </section>
    </main>
  )
}
