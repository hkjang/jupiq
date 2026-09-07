import { CopyOutlined, DeleteOutlined, KeyOutlined, ReloadOutlined, UserOutlined } from '@ant-design/icons'
import { Alert, App, Button, Card, Col, Descriptions, Flex, Form, Input, InputNumber, Modal, Row, Select, Space, Table, Tabs, Tag, type TableColumnsType } from 'antd'
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { jsonBody, request } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { AsyncState } from '../components/AsyncState'
import { PageHeader } from '../components/PageHeader'
import { useList } from '../hooks/useList'
import type { ApiKey, ApiRecord } from '../types'
import { asText, formatDate, pick, statusTone } from '../utils/format'
import { stableRowKey } from '../utils/rowKey'
import { sorterFor } from '../utils/sorting'

export function PersonalPage() {
  const { message, modal } = App.useApp()
  const { user, refreshUser, hasGlobalPermission } = useAuth()
  const canManageKeys = hasGlobalPermission('profile:keys')
  const { data: keys, loading, refreshing, error, reload } = useList<ApiKey>(canManageKeys ? '/keys' : '')
  const [params, setParams] = useSearchParams()
  const [profileForm] = Form.useForm<ApiRecord>()
  const [keyForm] = Form.useForm<ApiRecord>()
  const [passwordForm] = Form.useForm<ApiRecord>()
  const [savingProfile, setSavingProfile] = useState(false)
  const [savingPassword, setSavingPassword] = useState(false)
  const [keyModalOpen, setKeyModalOpen] = useState(false)
  const [creatingKey, setCreatingKey] = useState(false)
  const [issuedSecret, setIssuedSecret] = useState('')

  useEffect(() => {
    if (user) profileForm.setFieldsValue({ display_name: user.display_name || user.name, email: user.email, department: user.department })
  }, [user, profileForm])

  const saveProfile = async (values: ApiRecord) => {
    setSavingProfile(true)
    try {
      await request('/auth/me', { method: 'PATCH', body: jsonBody(values) })
      await refreshUser()
      message.success('개인 프로필을 저장했습니다.')
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '프로필을 저장하지 못했습니다.')
    } finally {
      setSavingProfile(false)
    }
  }

  const issueKey = async (values: ApiRecord) => {
    setCreatingKey(true)
    try {
      const result = await request<ApiRecord>('/keys', { method: 'POST', body: jsonBody(values) })
      const secret = asText(pick(result, 'secret', 'token', 'api_key'), '')
      setIssuedSecret(secret)
      keyForm.resetFields()
      await reload()
      if (!secret) {
        setKeyModalOpen(false)
        message.success('API 키를 발급했습니다.')
      }
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : 'API 키를 발급하지 못했습니다.')
    } finally {
      setCreatingKey(false)
    }
  }

  const changePassword = async (values: ApiRecord) => {
    setSavingPassword(true)
    try {
      await request('/auth/password', { method: 'POST', body: jsonBody({ current_password: values.current_password, new_password: values.new_password }) })
      passwordForm.resetFields()
      message.success('비밀번호를 변경하고 다른 로그인 세션을 종료했습니다.')
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '비밀번호를 변경하지 못했습니다.')
    } finally {
      setSavingPassword(false)
    }
  }

  const rotateKey = (key: ApiKey) => {
    modal.confirm({
      title: 'API 키 회전',
      content: '기존 키는 즉시 사용할 수 없게 됩니다. 새 키를 사용하는 시스템을 바로 갱신할 수 있을 때 진행하세요.',
      okText: '회전', cancelText: '취소',
      onOk: async () => {
        try {
          const result = await request<ApiRecord>(`/keys/${encodeURIComponent(String(key.id))}/rotate`, { method: 'POST' })
          setIssuedSecret(asText(pick(result, 'secret', 'token', 'api_key'), ''))
          setKeyModalOpen(true)
          await reload()
        } catch (caught) {
          message.error(caught instanceof Error ? caught.message : '키를 회전하지 못했습니다.')
        }
      },
    })
  }

  const revokeKey = (key: ApiKey) => {
    modal.confirm({
      title: 'API 키 폐기',
      content: '폐기한 키는 복구할 수 없습니다.',
      okText: '폐기', cancelText: '취소', okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await request(`/keys/${encodeURIComponent(String(key.id))}/revoke`, { method: 'POST' })
          message.success('API 키를 폐기했습니다.')
          await reload()
        } catch (caught) {
          message.error(caught instanceof Error ? caught.message : '키를 폐기하지 못했습니다.')
        }
      },
    })
  }

  const copySecret = async () => {
    try {
      await navigator.clipboard.writeText(issuedSecret)
      message.success('클립보드에 복사했습니다.')
    } catch {
      message.warning('자동 복사하지 못했습니다. 키를 직접 선택해 복사해 주세요.')
    }
  }

  const keyColumns: TableColumnsType<ApiKey> = [
    { title: '이름', dataIndex: 'name', key: 'name', width: 170, sorter: sorterFor(['name']), showSorterTooltip: false },
    { title: '키 식별자', key: 'prefix', width: 150, render: (_value, row) => <code>{asText(pick(row, 'prefix', 'key_prefix'))}••••</code> },
    { title: '권한', key: 'permissions', width: 260, render: (_value, row) => <Space wrap size={[0, 4]}>{(Array.isArray(row.permissions) ? row.permissions : Array.isArray(row.scopes) ? row.scopes as string[] : []).map((permission) => <Tag key={permission}>{permission}</Tag>)}</Space> },
    { title: '상태', key: 'status', width: 100, sorter: sorterFor(['status']), showSorterTooltip: false, render: (_value, row) => <Tag color={statusTone(row.status)}>{asText(row.status, '활성')}</Tag> },
    { title: '최근 사용', key: 'last_used_at', width: 180, sorter: sorterFor(['last_used_at'], 'date'), showSorterTooltip: false, render: (_value, row) => formatDate(row.last_used_at) },
    { title: '만료', key: 'expires_at', width: 180, sorter: sorterFor(['expires_at'], 'date'), showSorterTooltip: false, render: (_value, row) => formatDate(row.expires_at) },
    { title: '작업', key: 'actions', fixed: 'right', width: 150, render: (_value, row) => <Space><Button size="small" icon={<ReloadOutlined />} onClick={() => rotateKey(row)}>회전</Button><Button size="small" danger icon={<DeleteOutlined />} onClick={() => revokeKey(row)}>폐기</Button></Space> },
  ]

  const profileTab = (
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={15}><Card title={<Space><UserOutlined />개인 프로필</Space>}><Form form={profileForm} layout="vertical" onFinish={saveProfile}><Form.Item label="사용자 ID"><Input value={user?.username} disabled /></Form.Item><Form.Item name="display_name" label="표시 이름"><Input /></Form.Item><Form.Item name="email" label="이메일"><Input type="email" /></Form.Item><Form.Item name="department" label="부서"><Input disabled /></Form.Item><Button type="primary" htmlType="submit" loading={savingProfile}>프로필 저장</Button></Form></Card></Col>
      <Col xs={24} xl={9}><Space direction="vertical" size={16} style={{ width: '100%' }}><Card title="내 권한"><Descriptions column={1} size="small" items={[{ key: 'roles', label: '역할', children: <Space wrap>{(user?.roles || []).map((role) => <Tag color="blue" key={role}>{role}</Tag>)}</Space> }, { key: 'permissions', label: '세부 권한', children: <Space wrap>{(user?.permissions || []).map((permission) => <Tag key={permission}>{permission}</Tag>)}</Space> }]} /><Alert type="info" showIcon message="권한 변경은 서비스 관리자에게 요청하세요" /></Card>{user?.auth_source === 'local' ? <Card title="로컬 비밀번호 변경"><Form form={passwordForm} layout="vertical" onFinish={changePassword}><Form.Item name="current_password" label="현재 비밀번호" rules={[{ required: true, message: '현재 비밀번호를 입력해 주세요.' }]}><Input.Password autoComplete="current-password" /></Form.Item><Form.Item name="new_password" label="새 비밀번호" rules={[{ required: true, min: 12, message: '새 비밀번호는 12자 이상이어야 합니다.' }]}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name="confirm_password" label="새 비밀번호 확인" dependencies={['new_password']} rules={[{ required: true, message: '새 비밀번호를 다시 입력해 주세요.' }, ({ getFieldValue }) => ({ validator: (_rule, value) => !value || getFieldValue('new_password') === value ? Promise.resolve() : Promise.reject(new Error('새 비밀번호가 일치하지 않습니다.')) })]}><Input.Password autoComplete="new-password" /></Form.Item><Button htmlType="submit" loading={savingPassword}>비밀번호 변경</Button></Form></Card> : <Alert type="info" showIcon message="OIDC 비밀번호는 Keycloak에서 변경하세요" />}</Space></Col>
    </Row>
  )

  const keysTab = (
    <>
      <Alert className="data-note" type="warning" showIcon message="개인 API 키는 발급·회전 직후 한 번만 표시됩니다" description="키를 안전한 비밀 저장소에 보관하고, 용도별 최소 권한을 선택하세요." />
      <Flex justify="flex-end"><Button type="primary" icon={<KeyOutlined />} onClick={() => { setIssuedSecret(''); setKeyModalOpen(true) }}>API 키 발급</Button></Flex>
      <AsyncState loading={loading} refreshing={refreshing} error={error} onRetry={reload} empty={!loading && !error && keys.length === 0} emptyDescription="발급된 개인 API 키가 없습니다.">
        <Table<ApiKey> rowKey={(row) => stableRowKey(row, row.id)} loading={refreshing} columns={keyColumns} dataSource={keys} scroll={{ x: 1050 }} />
      </AsyncState>
    </>
  )

  return (
    <>
      <PageHeader title="개인화" description="내 프로필과 서비스 API 키를 서비스 관리자 설정과 분리해 관리합니다." />
      <Tabs activeKey={canManageKeys && params.get('tab') === 'keys' ? 'keys' : 'profile'} onChange={(tab) => setParams((current) => { const next = new URLSearchParams(current); next.set('tab', tab); return next }, { replace: true })} items={[{ key: 'profile', label: '내 프로필', children: profileTab }, ...(canManageKeys ? [{ key: 'keys', label: 'API 키·회전', children: keysTab }] : [])]} />
      <Modal title={issuedSecret ? '새 API 키가 발급되었습니다' : '개인 API 키 발급'} open={canManageKeys && keyModalOpen} onCancel={() => { setKeyModalOpen(false); setIssuedSecret('') }} footer={issuedSecret ? <Button type="primary" onClick={() => { setKeyModalOpen(false); setIssuedSecret('') }}>안전하게 보관했습니다</Button> : undefined} destroyOnHidden>
        {issuedSecret ? <Space direction="vertical" size={16} style={{ width: '100%' }}><Alert type="warning" showIcon message="이 키는 다시 확인할 수 없습니다" /><Input.TextArea value={issuedSecret} readOnly autoSize={{ minRows: 3, maxRows: 6 }} aria-label="새 API 키" /><Button block icon={<CopyOutlined />} onClick={copySecret}>키 복사</Button></Space>
          : <Form form={keyForm} layout="vertical" onFinish={issueKey}><Form.Item name="name" label="키 이름" rules={[{ required: true, message: '키 이름을 입력해 주세요.' }]}><Input placeholder="예: 운영 자동화" /></Form.Item><Form.Item name="permissions" label="권한" rules={[{ required: true, message: '권한을 하나 이상 선택해 주세요.' }]}><Select virtual={false} mode="multiple" options={(user?.permissions || []).map((permission) => ({ label: permission, value: permission }))} placeholder="최소 권한 선택" /></Form.Item><Form.Item name="expires_in_days" label="유효기간(일)"><InputNumber min={1} max={3650} precision={0} style={{ width: '100%' }} /></Form.Item><Button type="primary" block htmlType="submit" loading={creatingKey}>키 발급</Button></Form>}
      </Modal>
    </>
  )
}
