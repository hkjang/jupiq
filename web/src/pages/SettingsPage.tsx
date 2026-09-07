import {
  ApiOutlined,
  CheckCircleOutlined,
  CodeOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  EditOutlined,
  ExperimentOutlined,
  GlobalOutlined,
  LinkOutlined,
  LockOutlined,
  PlusOutlined,
  RobotOutlined,
  SafetyCertificateOutlined,
  SettingOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Drawer,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Result,
  Row,
  Select,
  Skeleton,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd'
import { useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { jsonBody, request, requestList } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { useApi } from '../hooks/useApi'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatDate, pick, statusTone } from '../utils/format'
import {
  buildRoleBindingPayload,
  roleBindingsFromUser,
  type RoleBindingDraft,
  type RoleBindingsFormValue,
} from '../utils/roleBindings'
import { NativeSelect } from '../components/NativeSelect'
import { stableRowKey } from '../utils/rowKey'
import { sorterFor } from '../utils/sorting'
import { resolveOidcSettings } from '../utils/settings'
import { AsyncState } from '../components/AsyncState'
import { PageHeader } from '../components/PageHeader'

interface TestStep extends ApiRecord {
  name?: string
  status?: string
  message?: string
  latency_ms?: number
  success?: boolean
  detail?: string
}

interface TestResult extends ApiRecord {
  success: boolean
  type: string
  steps?: TestStep[]
  version?: string
  response_time_ms?: number
  checked_at?: string
  error?: string
  guidance?: string
  checks?: TestStep[]
  remote_version?: string
  latency_ms?: number
  remediation?: string
}

const secretPaths = [
  ['oidc', 'client_secret'], ['auth.oidc', 'client_secret'], ['prometheus', 'bearer_token'],
  ['kubernetes', 'bearer_token'], ['ai', 'api_key'], ['notifications', 'secret'],
]

function clearSecrets(settings: ApiRecord): ApiRecord {
  const clone = structuredClone(settings)
  for (const [section, key] of secretPaths) {
    const group = clone[section]
    if (group && typeof group === 'object' && !Array.isArray(group)) delete (group as ApiRecord)[key]
  }
  return clone
}

function pruneBlankSecrets(settings: ApiRecord): ApiRecord {
  const clone = structuredClone(settings)
  for (const [section, key] of secretPaths) {
    const group = clone[section]
    if (group && typeof group === 'object' && !Array.isArray(group) && !(group as ApiRecord)[key]) delete (group as ApiRecord)[key]
  }
  const security = clone.security
  if (security && typeof security === 'object' && !Array.isArray(security)) {
    delete (security as ApiRecord).key_roles
    delete (security as ApiRecord).break_glass_enabled
    delete (security as ApiRecord).break_glass_minutes
    delete (security as ApiRecord).dangerous_action_reason
  }
  const system = clone.system
  if (system && typeof system === 'object' && !Array.isArray(system)) {
    delete (system as ApiRecord).hourly_retention_days
    delete (system as ApiRecord).service_name
    delete (system as ApiRecord).locale
    delete (system as ApiRecord).timezone
    delete (system as ApiRecord).collection_interval_seconds
    delete (system as ApiRecord).page_size
  }
  const prometheus = clone.prometheus
  if (prometheus && typeof prometheus === 'object' && !Array.isArray(prometheus)) {
    delete (prometheus as ApiRecord).timeout_seconds
    parseJSONSetting(prometheus as ApiRecord, 'queries', 'Prometheus 쿼리')
  }
  const llm = clone.llm_usage
  if (llm && typeof llm === 'object' && !Array.isArray(llm)) {
    for (const [key, label] of [['label_mappings', 'Label 매핑'], ['promql', 'PromQL']] as const) {
      parseJSONSetting(llm as ApiRecord, key, label)
    }
  }
  return clone
}

function parseJSONSetting(section: ApiRecord, key: string, label: string) {
  const value = section[key]
  if (typeof value !== 'string') return
  try {
    const parsed = value.trim() ? JSON.parse(value) : {}
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error()
    section[key] = parsed
  } catch {
    throw new Error(`${label}은 JSON 객체 형식이어야 합니다.`)
  }
}

function jsonObjectText(value: unknown) {
  if (typeof value === 'string') return value
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '{}'
  return JSON.stringify(value, null, 2)
}

function canonicalTestConfig(type: string, config: ApiRecord): ApiRecord {
  const secret = pick(config, 'secret', 'client_secret', 'api_token', 'bearer_token', 'api_key', 'gateway_token')
  const normalized = type === 'llm_usage' ? pruneBlankSecrets({ llm_usage: config }).llm_usage as ApiRecord : config
  return {
    ...normalized,
    ...(type === 'oidc' ? { issuer_url: normalized.issuer_url } : type === 'llm_usage' ? {} : { base_url: pick(normalized, 'base_url', 'url', 'api_url', 'prometheus_url', 'gateway_url') }),
    ...(type === 'llm_usage' ? {} : { verify_tls: Boolean(pick(normalized, 'verify_tls', 'tls_verify') ?? true) }),
    ...(secret ? { secret } : {}),
  }
}

function IntegrationCard({ icon, title, description, testType, onTest, testing, children, testLabel = '연결 테스트', health }: {
  icon: ReactNode
  title: string
  description: string
  testType: string
  onTest: (type: string) => void
  testing: string
  children: ReactNode
  testLabel?: string
  health?: ApiRecord
}) {
  const effectiveHealth = health
  return (
    <Card className="integration-card" title={<Space>{icon}<span>{title}</span></Space>} extra={
      <Space wrap>{effectiveHealth && <Tag color={effectiveHealth.status === 'success' ? 'success' : 'error'}>{effectiveHealth.status === 'success' ? '최근 검증 성공' : `연속 실패 ${asNumber(effectiveHealth.consecutive_failures)}회`}</Tag>}<Button icon={<ApiOutlined />} onClick={() => onTest(testType)} loading={testing === testType} disabled={Boolean(testing) && testing !== testType}>{testLabel}</Button></Space>
    }>
      <Typography.Paragraph type="secondary">{description}</Typography.Paragraph>
      {effectiveHealth && <Typography.Paragraph type="secondary">마지막 검증 {formatDate(effectiveHealth.last_attempt_at)}{effectiveHealth.latency_ms !== undefined ? ` · ${asNumber(effectiveHealth.latency_ms)}ms` : ''}{effectiveHealth.version ? ` · v${asText(effectiveHealth.version)}` : ''}</Typography.Paragraph>}
      {Boolean(effectiveHealth?.last_error) && <Alert className="data-note" type="warning" showIcon message={asText(effectiveHealth?.last_error)} />}
      {children}
    </Card>
  )
}

const secretHelp = '저장된 비밀값은 다시 표시하지 않습니다. 변경할 때만 새 값을 입력하세요.'

function RoleManager({ canWrite, canAssign }: { canWrite: boolean; canAssign: boolean }) {
  const { message } = App.useApp()
  const { data: roles, loading, error, reload } = useApi<ApiRecord[]>('/roles')
  const [users, setUsers] = useState<ApiRecord[]>([])
  const [usersPage, setUsersPage] = useState(1)
  const [usersPageSize, setUsersPageSize] = useState(20)
  const [usersTotal, setUsersTotal] = useState(0)
  const [usersLoading, setUsersLoading] = useState(false)
  const [assigning, setAssigning] = useState<number | null>(null)
  const [bindingOpen, setBindingOpen] = useState(false)
  const [bindingUser, setBindingUser] = useState<ApiRecord | null>(null)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<ApiRecord | null>(null)
  const [saving, setSaving] = useState(false)
  const [roleForm] = Form.useForm<ApiRecord>()
  const [bindingForm] = Form.useForm<RoleBindingsFormValue>()

  const reloadUsers = async () => {
    if (!canAssign) return
    setUsersLoading(true)
    try {
      const result = await requestList<ApiRecord>(`/local-users?page=${usersPage}&page_size=${usersPageSize}`)
      setUsers(result.data)
      setUsersTotal(result.meta.total ?? result.data.length)
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '사용자 역할을 불러오지 못했습니다.')
    } finally {
      setUsersLoading(false)
    }
  }

  useEffect(() => { void reloadUsers() }, [canAssign, usersPage, usersPageSize]) // eslint-disable-line react-hooks/exhaustive-deps

  const showRole = (role?: ApiRecord) => {
    setEditing(role || null)
    roleForm.setFieldsValue(role ? { ...role, permissions: Array.isArray(role.permissions) ? role.permissions : [] } : { key: '', name: '', description: '', permissions: [] })
    setOpen(true)
  }

  const saveRole = async (values: ApiRecord) => {
    setSaving(true)
    try {
      const id = editing ? asNumber(editing.id) : 0
      await request(id ? `/roles/${id}` : '/roles', { method: id ? 'PUT' : 'POST', body: jsonBody(values) })
      message.success(id ? '역할을 변경했습니다.' : '역할을 만들었습니다.')
      setOpen(false)
      await reload()
      await reloadUsers()
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '역할을 저장하지 못했습니다.')
    } finally {
      setSaving(false)
    }
  }

  const deleteRole = async (role: ApiRecord) => {
    try {
      await request(`/roles/${asNumber(role.id)}`, { method: 'DELETE' })
      message.success('역할을 삭제했습니다.')
      await reload()
      await reloadUsers()
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '역할을 삭제하지 못했습니다.')
    }
  }

  const showRoleBindings = (user: ApiRecord) => {
    setBindingUser(user)
    bindingForm.setFieldsValue({ bindings: roleBindingsFromUser(user, roles || []) })
    setBindingOpen(true)
  }

  const closeRoleBindings = () => {
    if (assigning !== null) return
    setBindingOpen(false)
    setBindingUser(null)
    bindingForm.resetFields()
  }

  const assignRoles = async (values: RoleBindingsFormValue) => {
    if (!bindingUser) return
    const id = asNumber(bindingUser.id)
    setAssigning(id)
    try {
      const bindings = buildRoleBindingPayload(values.bindings)
      await request(`/local-users/${id}/roles`, { method: 'PUT', body: jsonBody({ bindings }) })
      message.success(`${asText(bindingUser.username)} 사용자의 역할과 범위를 변경했습니다.`)
      setBindingOpen(false)
      setBindingUser(null)
      bindingForm.resetFields()
      await reloadUsers()
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '사용자 역할과 범위를 변경하지 못했습니다.')
    } finally {
      setAssigning(null)
    }
  }

  const validateUniqueRole = (_rule: unknown, roleID: unknown) => {
    if (!roleID) return Promise.resolve()
    const bindings = (bindingForm.getFieldValue('bindings') || []) as RoleBindingDraft[]
    return bindings.filter((binding) => binding?.role_id === roleID).length > 1
      ? Promise.reject(new Error('같은 역할은 한 번만 추가할 수 있습니다.'))
      : Promise.resolve()
  }

  const validateRestrictedScope = (index: number) => {
    const binding = bindingForm.getFieldValue(['bindings', index]) as RoleBindingDraft | undefined
    if (binding?.scope_mode !== 'restricted') return Promise.resolve()
    const hubs = Array.isArray(binding.hub_ids) ? binding.hub_ids.filter((value) => String(value).trim()) : []
    const departments = Array.isArray(binding.departments) ? binding.departments.filter((value) => String(value).trim()) : []
    return hubs.length || departments.length
      ? Promise.resolve()
      : Promise.reject(new Error('Hub ID 또는 부서를 하나 이상 입력해 주세요.'))
  }

  const roleRows = roles || []
  return <>
    <Card title="역할·세부 권한 관리" extra={canWrite && <Button icon={<PlusOutlined />} onClick={() => showRole()}>역할 추가</Button>}>
      <Typography.Paragraph type="secondary">역할별 permission을 변경하면 다음 인증부터 적용됩니다. 시스템 역할은 삭제할 수 없고, 최고 관리자 전체 권한(*)과 마지막 최고 관리자 할당은 보호됩니다.</Typography.Paragraph>
      {error && <Alert type="error" showIcon message="역할을 불러오지 못했습니다" description={error.message} action={<Button onClick={() => void reload()}>다시 시도</Button>} />}
      <Table<ApiRecord> rowKey={(row) => stableRowKey(row, row.id)} loading={loading} dataSource={roleRows} pagination={false} scroll={{ x: 900 }} columns={[
        { title: '역할 키', dataIndex: 'key', width: 180, sorter: sorterFor(['key']), showSorterTooltip: false, render: (value, row) => <Space><strong>{asText(value)}</strong>{Boolean(row.system) && <Tag color="blue">시스템</Tag>}</Space> },
        { title: '표시 이름', dataIndex: 'name', width: 180, sorter: sorterFor(['name']), showSorterTooltip: false },
        { title: '설명', dataIndex: 'description', width: 240 },
        { title: '세부 권한', dataIndex: 'permissions', render: (value) => <Space wrap size={[0, 4]}>{(Array.isArray(value) ? value : []).map((permission) => <Tag key={String(permission)}>{String(permission)}</Tag>)}</Space> },
        ...(canWrite ? [{ title: '작업', key: 'actions', fixed: 'right' as const, width: 120, render: (_value: unknown, row: ApiRecord) => <Space><Button aria-label="역할 편집" icon={<EditOutlined />} onClick={() => showRole(row)} />{!row.system && <Popconfirm title="이 역할을 삭제할까요?" description="할당된 사용자의 해당 역할도 제거됩니다." okText="삭제" cancelText="취소" onConfirm={() => void deleteRole(row)}><Button aria-label="역할 삭제" danger icon={<DeleteOutlined />} /></Popconfirm>}</Space> }] : []),
      ]} />
    </Card>
    {canAssign && <Card title="사용자 역할 할당" style={{ marginTop: 16 }}>
      <Typography.Paragraph type="secondary">Bootstrap·OIDC 사용자를 포함한 서비스 계정에 전역 또는 Hub·부서 제한 역할을 할당합니다.</Typography.Paragraph>
      <Alert className="data-note" type="info" showIcon message="제한 범위는 Hub·사용자·서버 관리 API에 적용됩니다" description="설정, 역할, 로컬 사용자, 감사로그, 대시보드·통계·메트릭·GPU와 기타 리소스 API는 현재 전역 권한만 허용합니다." />
      <Table<ApiRecord> rowKey={(row) => stableRowKey(row, row.id)} loading={usersLoading} dataSource={users} pagination={{ current: usersPage, pageSize: usersPageSize, total: usersTotal, showSizeChanger: true, onChange: (page, size) => { setUsersPage(size !== usersPageSize ? 1 : page); setUsersPageSize(size) } }} scroll={{ x: 860 }} columns={[
        { title: '사용자', dataIndex: 'username', width: 180, sorter: sorterFor(['username']), showSorterTooltip: false, render: (value, row) => <Space direction="vertical" size={0}><strong>{asText(value)}</strong><Typography.Text type="secondary">{asText(row.auth_source)}</Typography.Text></Space> },
        { title: '이름', dataIndex: 'display_name', width: 180, sorter: sorterFor(['display_name']), showSorterTooltip: false },
        { title: '역할·적용 범위', key: 'roles', render: (_value, row) => {
          const bindings = roleBindingsFromUser(row, roleRows)
          return <Space direction="vertical" size={6} style={{ width: '100%' }}>
            <Flex wrap gap={4}>{bindings.length === 0
              ? <Typography.Text type="secondary">할당된 역할 없음</Typography.Text>
              : bindings.map((binding, index) => {
                const role = roleRows.find((candidate) => asNumber(candidate.id) === binding.role_id)
                const scopeText = binding.scope_mode === 'global'
                  ? '전역'
                  : [
                    ...(binding.hub_ids || []).map((value) => `Hub ${value}`),
                    ...(binding.departments || []).map((value) => `부서 ${value}`),
                  ].join(' · ')
                return <Space key={`${binding.role_id}-${index}`} size={0}><Tag color="blue">{role ? `${asText(role.name)} (${asText(role.key)})` : `역할 #${binding.role_id}`}</Tag><Tag color={binding.scope_mode === 'global' ? 'green' : 'gold'}>{scopeText || '제한 범위'}</Tag></Space>
              })}
            </Flex>
            <Button size="small" icon={<EditOutlined />} loading={assigning === asNumber(row.id)} disabled={assigning !== null} onClick={() => showRoleBindings(row)}>역할·범위 설정</Button>
          </Space>
        } },
      ]} />
    </Card>}
    <Modal title={editing ? '역할 편집' : '역할 추가'} open={open} onCancel={() => !saving && setOpen(false)} onOk={() => roleForm.submit()} confirmLoading={saving} okText="저장" cancelText="취소" destroyOnHidden>
      <Form form={roleForm} layout="vertical" onFinish={saveRole} preserve={false}>
        <Form.Item name="key" label="역할 키" rules={[{ required: true, message: '역할 키를 입력해 주세요.' }]}><Input disabled={Boolean(editing?.system)} placeholder="예: department_admin" /></Form.Item>
        <Form.Item name="name" label="표시 이름" rules={[{ required: true, message: '표시 이름을 입력해 주세요.' }]}><Input /></Form.Item>
        <Form.Item name="description" label="설명"><Input.TextArea rows={3} /></Form.Item>
        <Form.Item name="permissions" label="세부 권한" rules={[{ required: true, message: '권한을 하나 이상 입력해 주세요.' }]} extra={asText(editing?.key) === 'super_admin' ? '최고 관리자 전체 권한(*)은 서비스 잠금을 방지하기 위해 고정됩니다.' : 'resource:action 형식 또는 namespace:* 와일드카드를 사용합니다.'}><Select virtual={false} disabled={asText(editing?.key) === 'super_admin'} mode="tags" tokenSeparators={[',', ' ']} placeholder="예: hubs:read" open={false} /></Form.Item>
      </Form>
    </Modal>
    <Modal
      title={`${asText(bindingUser?.username, '사용자')} 역할·범위 설정`}
      width={760}
      open={bindingOpen}
      onCancel={closeRoleBindings}
      onOk={() => bindingForm.submit()}
      confirmLoading={assigning !== null}
      okText="저장"
      cancelText="취소"
      destroyOnHidden
    >
      <Alert
        className="data-note"
        type="info"
        showIcon
        message="전역과 제한 범위를 역할별로 선택하세요"
        description="제한 범위에는 Hub ID 또는 부서를 하나 이상 입력해야 합니다. 두 종류를 함께 입력하면 Hub와 부서 조건을 모두 만족하는 대상에만 적용됩니다. 현재 범위 권한은 Hub·사용자·서버 관리에만 지원되며 그 외 API는 전역 권한이 필요합니다."
      />
      <Form<RoleBindingsFormValue> form={bindingForm} layout="vertical" onFinish={assignRoles} preserve={false}>
        <Form.List name="bindings">
          {(fields, { add, remove }) => <Space direction="vertical" size={12} style={{ width: '100%' }}>
            {fields.map(({ key, ...field }, index) => <Card key={key} size="small" title={`역할 할당 ${index + 1}`} extra={<Button aria-label={`역할 할당 ${index + 1} 삭제`} danger type="text" icon={<DeleteOutlined />} onClick={() => remove(field.name)} /> }>
              <Row gutter={12}>
                <Col xs={24} md={14}>
                  <Form.Item {...field} name={[field.name, 'role_id']} label="역할" rules={[{ required: true, message: '역할을 선택해 주세요.' }, { validator: validateUniqueRole }]}>
                    <NativeSelect placeholder="역할 선택" options={roleRows.map((role) => ({ value: asNumber(role.id), label: `${asText(role.name)} (${asText(role.key)})` }))} />
                  </Form.Item>
                </Col>
                <Col xs={24} md={10}>
                  <Form.Item {...field} name={[field.name, 'scope_mode']} label="적용 범위" rules={[{ required: true, message: '적용 범위를 선택해 주세요.' }, { validator: () => validateRestrictedScope(field.name) }]}>
                    <NativeSelect placeholder="적용 범위 선택" options={[{ value: 'global', label: '전역' }, { value: 'restricted', label: 'Hub·부서 제한' }]} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item noStyle shouldUpdate>
                {() => bindingForm.getFieldValue(['bindings', field.name, 'scope_mode']) === 'restricted' && <Row gutter={12}>
                  <Col xs={24} md={12}>
                    <Form.Item {...field} name={[field.name, 'hub_ids']} label="Hub ID" extra="Hub 관리 화면에 표시되는 숫자 ID를 입력하세요." rules={[{
                      validator: (_rule, values: unknown) => !Array.isArray(values) || values.every((value) => /^[1-9]\d*$/.test(String(value).trim()) && Number.isSafeInteger(Number(value)))
                        ? Promise.resolve()
                        : Promise.reject(new Error('1 이상의 숫자 Hub ID만 입력할 수 있습니다.')),
                    }]}>
                      <Select virtual={false} mode="tags" tokenSeparators={[',', ' ']} placeholder="예: 1, 3" open={false} />
                    </Form.Item>
                  </Col>
                  <Col xs={24} md={12}>
                    <Form.Item {...field} name={[field.name, 'departments']} label="부서" extra="관리 대상 사용자의 부서 값과 정확히 일치해야 합니다.">
                      <Select virtual={false} mode="tags" tokenSeparators={[',']} placeholder="예: AI플랫폼팀" open={false} />
                    </Form.Item>
                  </Col>
                </Row>}
              </Form.Item>
            </Card>)}
            {fields.length === 0 && <Typography.Text type="secondary">할당된 역할이 없습니다. 아래 버튼으로 역할을 추가할 수 있습니다.</Typography.Text>}
            <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ scope_mode: 'global', hub_ids: [], departments: [] })}>역할 할당 추가</Button>
          </Space>}
        </Form.List>
      </Form>
    </Modal>
  </>
}

export function SettingsPage() {
  const { message } = App.useApp()
  const { refreshFeatures, hasGlobalPermission } = useAuth()
  const canWriteSettings = hasGlobalPermission('settings:write')
  const { data, loading, refreshing, error, reload } = useApi<ApiRecord>('/settings')
  const [params, setParams] = useSearchParams()
  const [form] = Form.useForm<ApiRecord>()
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [testing, setTesting] = useState('')
  const [testResult, setTestResult] = useState<TestResult | null>(null)
  const [testOpen, setTestOpen] = useState(false)

  useEffect(() => {
    if (!data) return
    const rawSettings = data.settings && typeof data.settings === 'object' ? data.settings as ApiRecord : data
    const safe = clearSecrets(rawSettings)
    form.setFieldsValue({
      ...safe,
      oidc: resolveOidcSettings(safe),
      features: { gpu_monitoring: false, llm_usage_monitoring: false, ...(safe.features as ApiRecord || {}) },
      workflow: { approval_enabled: false, ...(safe.workflow as ApiRecord || {}) },
      ai: { streaming: true, ...(safe.ai as ApiRecord || {}) },
      prometheus: {
        ...(safe.prometheus as ApiRecord || {}),
        queries: jsonObjectText((safe.prometheus as ApiRecord || {}).queries || {
          cpu_cores: 'sum(rate(container_cpu_usage_seconds_total{pod=~"jupyter-.*"}[5m])) by (pod)',
          memory_bytes: 'sum(container_memory_working_set_bytes{pod=~"jupyter-.*"}) by (pod)',
        }),
      },
      llm_usage: {
        ...(safe.llm_usage as ApiRecord || {}),
        source: 'prometheus',
        label_mappings: jsonObjectText((safe.llm_usage as ApiRecord || {}).label_mappings),
        promql: jsonObjectText((safe.llm_usage as ApiRecord || {}).promql),
      },
    })
    setDirty(false)
  }, [data, form])

  const save = async () => {
    if (!canWriteSettings) return
    try {
      const values = await form.validateFields()
      setSaving(true)
      await request('/settings', { method: 'PUT', body: jsonBody(pruneBlankSecrets(values)) })
      message.success('관리자 설정을 저장했습니다.')
      setDirty(false)
      await refreshFeatures()
      await reload()
    } catch (caught) {
      if (caught instanceof Error) message.error(caught.message)
    } finally {
      setSaving(false)
    }
  }

  const testIntegration = async (type: string) => {
    if (testing || !canWriteSettings) return
    const settingSection = type === 'webhook' ? 'notifications' : type
    const rawConfig = form.getFieldValue(settingSection) as ApiRecord || {}
    setTesting(type)
    setTestOpen(true)
    setTestResult(null)
    try {
      const config = canonicalTestConfig(type, rawConfig)
      const result = await request<TestResult>('/integrations/test', {
        method: 'POST', body: jsonBody({ type, config: { ...config, use_saved_secret: !secretPaths.some(([section, key]) => section === settingSection && Boolean(rawConfig[key])) } }),
      })
      setTestResult({
        ...result,
        success: Boolean(result.success),
        type,
        steps: result.steps || result.checks,
        version: result.version || result.remote_version,
        response_time_ms: result.response_time_ms ?? result.latency_ms,
        guidance: result.guidance || result.remediation,
      })
    } catch (caught) {
      setTestResult({
        success: false,
        type,
        checked_at: new Date().toISOString(),
        error: caught instanceof Error ? caught.message : '연결 테스트에 실패했습니다.',
        guidance: '주소, 망 경로, TLS 인증서와 인증 정보를 확인한 뒤 다시 시도해 주세요.',
      })
    } finally {
      setTesting('')
    }
  }

  const onValuesChange = (changed: ApiRecord) => {
    setDirty(true)
    void changed
  }

  const general = (
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={12}><Card title="데이터 보존"><Form.Item name={['system', 'raw_retention_days']} label="원시 메트릭 보존(일)" extra="CPU·메모리와 선택 기능 원시 메트릭 정리에 실제 적용됩니다."><InputNumber min={1} max={365} style={{ width: '100%' }} /></Form.Item></Card></Col>
      <Col xs={24} xl={12}><Card title="고정 동작과 개별 주기"><Typography.Paragraph type="secondary">서비스명은 jupiq, 기본 UI 언어는 한국어로 고정됩니다. 목록 표시 수는 각 화면에서 선택하고, JupyterHub 수집 주기는 Hub 등록·편집 Drawer에서 Hub별로 설정합니다.</Typography.Paragraph><Alert type="info" showIcon message="시간은 원본 시각과 브라우저 표시 시각을 구분합니다" description="서버 데이터는 UTC 기준 시각으로 보존하고 화면에서는 브라우저의 지역 시각으로 표시합니다." /></Card></Col>
    </Row>
  )

  const features = (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card className="feature-card"><Flex justify="space-between" gap={24} align="flex-start"><div><Space><ExperimentOutlined /><Typography.Title level={4}>GPU 모니터링</Typography.Title></Space><Typography.Paragraph type="secondary">DCGM/Prometheus에서 Pod별 할당 GPU 수·사용률·VRAM과 수집 최신성을 표시합니다. 기본값은 OFF이며, 끄면 관련 수집·API·메뉴·KPI가 비활성화됩니다.</Typography.Paragraph></div><Form.Item name={['features', 'gpu_monitoring']} valuePropName="checked" noStyle><Switch checkedChildren="사용" unCheckedChildren="사용 안 함" /></Form.Item></Flex></Card>
      <Card className="feature-card"><Flex justify="space-between" gap={24} align="flex-start"><div><Space><RobotOutlined /><Typography.Title level={4}>LLM API 사용량 모니터링</Typography.Title></Space><Typography.Paragraph type="secondary">사용자→Pod→모델별 호출, 지연, 토큰과 추정비용을 수집합니다. 프롬프트·응답 본문은 수집하지 않습니다. 기본값은 OFF입니다.</Typography.Paragraph></div><Form.Item name={['features', 'llm_usage_monitoring']} valuePropName="checked" noStyle><Switch checkedChildren="사용" unCheckedChildren="사용 안 함" /></Form.Item></Flex></Card>
      <Alert type="info" showIcon message="기능 스위치는 저장 후 즉시 반영됩니다" description="저장하기 전에는 메뉴와 수집기 동작이 변경되지 않습니다." />
    </Space>
  )

  const workflow = (
    <Card title="검토·승인 프로세스"><Alert className="data-note" type="info" showIcon message="승인 대상은 Notebook 서버 제어입니다" description="요청자는 자신의 요청을 검토·승인·반려할 수 없고, 팀장 선검토를 사용하면 검토자와 최종 승인자도 분리됩니다." /><Form.Item name={['workflow', 'approval_enabled']} label="검토·승인 사용" valuePropName="checked" extra="끄면 승인 메뉴와 승인·반려 단계가 제외되고 서버 작업이 즉시 처리됩니다."><Switch checkedChildren="사용" unCheckedChildren="사용 안 함" /></Form.Item><Form.Item name={['workflow', 'manager_review_enabled']} label="팀장 선검토" valuePropName="checked"><Switch checkedChildren="사용" unCheckedChildren="사용 안 함" /></Form.Item><Form.Item name={['workflow', 'require_reason']} label="승인·반려 사유 필수" valuePropName="checked"><Switch checkedChildren="필수" unCheckedChildren="선택" /></Form.Item><Form.Item name={['workflow', 'request_types']} label="승인 대상 요청"><Select virtual={false} mode="multiple" options={[{ label: '서버 시작·종료·재시작', value: 'server_action' }]} /></Form.Item></Card>
  )

  const integrations = (
    <Row gutter={[16, 16]}>
      <Col xs={24} xl={12}><IntegrationCard icon={<SafetyCertificateOutlined />} title="Keycloak OIDC" description="Issuer와 Client 정보만으로 SSO Discovery를 구성합니다." testType="oidc" onTest={testIntegration} testing={testing}><Form.Item name={['oidc', 'enabled']} label="SSO 사용" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['oidc', 'issuer_url']} label="Issuer URL" rules={[{ type: 'url', warningOnly: true }]}><Input placeholder="https://keycloak.internal/realms/company" /></Form.Item><Form.Item name={['oidc', 'client_id']} label="Client ID"><Input /></Form.Item><Form.Item name={['oidc', 'client_secret']} label="Client Secret" extra={secretHelp}><Input.Password autoComplete="new-password" placeholder="변경할 때만 입력" /></Form.Item><Form.Item name={['oidc', 'redirect_url']} label="Redirect URL" extra="비워 두면 현재 jupiq 주소에서 자동 계산합니다."><Input /></Form.Item><Form.Item name={['oidc', 'scopes']} label="Scopes"><Select virtual={false} mode="tags" placeholder="openid, profile, email" /></Form.Item><Form.Item name={['oidc', 'username_claim']} label="사용자 ID Claim"><Input placeholder="preferred_username" /></Form.Item><Form.Item name={['oidc', 'auto_create_users']} label="최초 로그인 사용자 자동 생성" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['oidc', 'verify_tls']} label="TLS 검증" valuePropName="checked"><Switch /></Form.Item></IntegrationCard></Col>
      <Col xs={24} xl={12}><IntegrationCard icon={<DatabaseOutlined />} title="Prometheus" description="메트릭 API 연결, 인증, 쿼리 권한과 응답시간을 검사합니다." testType="prometheus" onTest={testIntegration} testing={testing}><Form.Item name={['prometheus', 'enabled']} label="Prometheus 연계" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['prometheus', 'base_url']} label="Prometheus URL"><Input placeholder="https://prometheus.internal" /></Form.Item><Form.Item name={['prometheus', 'bearer_token']} label="Bearer Token" extra={secretHelp}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name={['prometheus', 'verify_tls']} label="TLS 검증" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['prometheus', 'queries']} label="자원 수집 PromQL(JSON)" extra="CPU·메모리 쿼리는 기본 제공하며 GPU 쿼리는 GPU 모니터링을 켠 경우에만 실행합니다."><Input.TextArea rows={7} /></Form.Item></IntegrationCard></Col>
      <Col xs={24} xl={12}><IntegrationCard icon={<GlobalOutlined />} title="Kubernetes" description="Cluster API 연결, 서비스 계정 인증과 조회 권한을 확인합니다." testType="kubernetes" onTest={testIntegration} testing={testing}><Form.Item name={['kubernetes', 'enabled']} label="Kubernetes 연계" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['kubernetes', 'base_url']} label="API Server URL"><Input /></Form.Item><Form.Item name={['kubernetes', 'namespace']} label="Namespace"><Input placeholder="jupyterhub" /></Form.Item><Form.Item name={['kubernetes', 'label_selector']} label="Pod Label Selector" extra="예: component=singleuser-server"><Input /></Form.Item><Form.Item name={['kubernetes', 'pod_username_regex']} label="Pod → username 정규식" extra="비우면 LLM 사용량 설정의 정규식을 사용합니다. 입력 시 (?P&lt;username&gt;...) 캡처가 필요합니다."><Input placeholder="^jupyter-(?P<username>.+)$" /></Form.Item><Form.Item name={['kubernetes', 'bearer_token']} label="Service Account Token" extra={secretHelp}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name={['kubernetes', 'verify_tls']} label="TLS 검증" valuePropName="checked"><Switch /></Form.Item></IntegrationCard></Col>
      <Col xs={24} xl={12}><IntegrationCard icon={<RobotOutlined />} title="AI API" description="OpenAI 호환 모델 목록 API로 주소·인증·모델 접근 권한을 검사합니다. 실제 채팅은 스트리밍으로 호출합니다." testType="ai" onTest={testIntegration} testing={testing}><Form.Item name={['ai', 'enabled']} label="AI 운영 분석" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['ai', 'base_url']} label="API Base URL"><Input placeholder="https://ai-api.internal/v1" /></Form.Item><Form.Item name={['ai', 'api_key']} label="API Key" extra={secretHelp}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name={['ai', 'model']} label="모델"><Input /></Form.Item><Form.Item name={['ai', 'max_tokens']} label="최대 토큰" extra="모델이 지원하는 범위에서 최대 262,144(256K)까지 설정할 수 있습니다."><InputNumber min={1} max={262144} style={{ width: '100%' }} /></Form.Item><Form.Item name={['ai', 'streaming']} label="스트리밍" valuePropName="checked"><Switch disabled checkedChildren="기본 사용" /></Form.Item><Form.Item name={['ai', 'verify_tls']} label="TLS 검증" valuePropName="checked"><Switch /></Form.Item></IntegrationCard></Col>
      <Col xs={24} xl={12}><IntegrationCard icon={<LinkOutlined />} title="Webhook" description="사내 메신저·자동화 수신 주소에 비식별 시험 이벤트를 보내고 2xx 응답을 검증합니다." testType="webhook" onTest={testIntegration} testing={testing}><Form.Item name={['notifications', 'webhook_enabled']} label="Webhook 사용" valuePropName="checked"><Switch /></Form.Item><Form.Item name={['notifications', 'base_url']} label="Webhook URL"><Input /></Form.Item><Form.Item name={['notifications', 'secret']} label="인증 Secret" extra={secretHelp}><Input.Password autoComplete="new-password" /></Form.Item><Form.Item name={['notifications', 'events']} label="전달 이벤트"><Select virtual={false} mode="tags" placeholder="incident.created" /></Form.Item></IntegrationCard></Col>
    </Row>
  )

  const llmUsage = (
    <IntegrationCard icon={<CodeOutlined />} title="LLM API 사용량 수집" description="정규식 샘플과 Prometheus의 pod/path label을 검사합니다. 실제 사용자 귀속은 현재 서버 인벤토리의 정확한 Pod 이름으로만 확정합니다." testType="llm_usage" testLabel="샘플 매핑·메트릭 검증" onTest={testIntegration} testing={testing}>
      <Alert type="warning" showIcon message="요청·응답 본문은 수집하지 않습니다" description="호출 수, 상태 코드, 지연, 토큰처럼 운영에 필요한 메타데이터만 집계합니다." />
      <Alert className="data-note" type="info" showIcon message="LLM 사용량 수집 소스는 Prometheus입니다" description="인증과 통신 설정은 위 Prometheus 연동을 함께 사용하며, 이 화면에서는 사용자 귀속과 Chat Completions 집계 규칙을 관리합니다." />
      <Row gutter={16}>
        <Col xs={24} lg={12}>
          <Form.Item name={['llm_usage', 'source']} label="수집 소스" extra="오프라인 운영에서 검증된 Prometheus 수집 방식으로 고정됩니다."><Select virtual={false} disabled options={[{ label: 'Prometheus', value: 'prometheus' }]} /></Form.Item>
          <Form.Item name={['llm_usage', 'pod_username_regex']} label="Pod → username 정규식" extra="반드시 (?P&lt;username&gt;...) named capture를 사용하세요. 정규식은 후보 필터이며 정확히 일치하는 등록 서버 Pod가 없으면 집계하지 않습니다."><Input placeholder="^jupyter-(?P<username>.+?)-" /></Form.Item>
          <Form.Item name={['llm_usage', 'sample_pod']} label="매핑 검증용 샘플 Pod"><Input placeholder="jupyter-user01-lab" /></Form.Item>
          <Form.Item name={['llm_usage', 'path_matcher']} label="Chat Completions 경로"><Input placeholder="/v1/chat/completions" /></Form.Item>
          <Form.Item name={['llm_usage', 'label_mappings']} label="Label 매핑(JSON)" extra="허용된 메타데이터 label만 연결합니다. 비표준 이름은 PromQL에서 pod/path/status/model/hub/network로 별칭 처리하세요."><Input.TextArea rows={6} placeholder={'{"pod":"kubernetes_pod_name","model":"served_model","status":"code","path":"route"}'} /></Form.Item>
        </Col>
        <Col xs={24} lg={12}>
          <Form.Item name={['llm_usage', 'promql']} label="수집 PromQL(JSON)" extra="calls는 필수이며 input_tokens, output_tokens, total_tokens, bytes, latency_p50_ms, latency_p95_ms는 선택입니다."><Input.TextArea rows={10} /></Form.Item>
          <Form.Item name={['llm_usage', 'input_cost_per_million']} label="Input 100만 토큰 단가"><InputNumber min={0} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name={['llm_usage', 'output_cost_per_million']} label="Output 100만 토큰 단가"><InputNumber min={0} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name={['llm_usage', 'stale_seconds']} label="stale 판정(초)"><InputNumber min={30} max={86400} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name={['llm_usage', 'retention_days']} label="LLM 사용량 보존(일)"><InputNumber min={1} max={365} style={{ width: '100%' }} /></Form.Item>
        </Col>
      </Row>
    </IntegrationCard>
  )

  const security = (
    <Row gutter={[16, 16]}><Col xs={24} xl={12}><Card title={<Space><LockOutlined />개인 API 키 정책</Space>}><Form.Item name={['security', 'key_rotation_days']} label="기본 회전 주기(일)"><InputNumber min={1} max={3650} style={{ width: '100%' }} /></Form.Item><Form.Item name={['security', 'key_max_lifetime_days']} label="최대 유효기간(일)"><InputNumber min={1} max={3650} style={{ width: '100%' }} /></Form.Item><Form.Item name={['security', 'key_permissions']} label="허용 권한"><Select virtual={false} mode="tags" placeholder="예: hubs:read" /></Form.Item></Card></Col><Col xs={24} xl={12}><Card title="비밀값 암호화"><Alert type="success" showIcon message="비밀값은 애플리케이션 암호화 후 저장됩니다" description="ENCRYPTION_KEY는 환경변수로만 주입되며 관리자 화면에서 조회하거나 변경할 수 없습니다." /></Card></Col>{hasGlobalPermission('roles:read') && <Col xs={24}><RoleManager canWrite={hasGlobalPermission('roles:write')} canAssign={hasGlobalPermission('roles:write') && hasGlobalPermission('users:read')} /></Col>}</Row>
  )

  const tabItems = [
    { key: 'general', label: '기본', icon: <SettingOutlined />, children: general },
    { key: 'features', label: '선택 기능', icon: <ExperimentOutlined />, children: features },
    { key: 'integrations', label: '외부 연동', icon: <ApiOutlined />, children: integrations },
    { key: 'llm-usage', label: 'LLM 사용량', icon: <RobotOutlined />, children: llmUsage },
    { key: 'workflow', label: '승인 프로세스', icon: <CheckCircleOutlined />, children: workflow },
    { key: 'security', label: '보안·키 권한', icon: <LockOutlined />, children: security },
  ]

  return (
    <>
      <PageHeader title="관리자 설정" description={canWriteSettings ? '서비스 정책과 모든 외부 연동을 한곳에서 안전하게 구성합니다.' : '현재 계정은 서비스 설정을 읽기 전용으로 조회할 수 있습니다.'} onRefresh={reload} refreshing={refreshing} extra={canWriteSettings && <Button type="primary" loading={saving} disabled={!dirty} onClick={save}>설정 저장</Button>} />
      {!canWriteSettings && <Alert className="data-note" type="info" showIcon message="읽기 전용 설정" description="설정 변경과 연결 테스트에는 settings:write 권한이 필요합니다." />}
      <AsyncState loading={loading && !data} refreshing={refreshing} error={error && !data ? error : null} onRetry={reload} empty={false}>
        <Form form={form} layout="vertical" requiredMark="optional" disabled={!canWriteSettings} onValuesChange={onValuesChange}>
          <Tabs activeKey={params.get('tab') || 'general'} onChange={(tab) => setParams((current) => { const next = new URLSearchParams(current); next.set('tab', tab); return next }, { replace: true })} items={tabItems} />
        </Form>
      </AsyncState>
      <Drawer title="연결 테스트 결과" width="min(560px, 100vw)" open={testOpen} onClose={() => !testing && setTestOpen(false)} closable={!testing} maskClosable={!testing}>
        {testing && <div className="connection-testing"><Skeleton active paragraph={{ rows: 5 }} /><Typography.Text>주소, TLS와 연동별 조회·전달 권한을 확인하고 있습니다…</Typography.Text></div>}
        {!testing && testResult && (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            <Result status={testResult.success ? 'success' : 'error'} icon={testResult.success ? <CheckCircleOutlined /> : <WarningOutlined />} title={testResult.success ? '연결 테스트를 통과했습니다' : '연결 테스트에 실패했습니다'} subTitle={testResult.error || (testResult.success ? '현재 폼 값으로 정상 연결되었습니다.' : '연결 정보를 확인해 주세요.')} />
            <Card size="small"><Flex wrap gap={12}><Tag>유형 {testResult.type}</Tag>{testResult.version && <Tag>버전 {testResult.version}</Tag>}{testResult.response_time_ms !== undefined && <Tag>응답 {asNumber(testResult.response_time_ms)}ms</Tag>}<Tag>검사 {formatDate(testResult.checked_at || new Date().toISOString())}</Tag></Flex></Card>
            {(testResult.steps || []).map((step, index) => <Card key={`${step.name}-${index}`} size="small"><Flex justify="space-between" align="start" gap={12}><div><strong>{asText(pick(step, 'name', 'stage'), `검사 ${index + 1}`)}</strong><Typography.Paragraph type="secondary">{asText(pick(step, 'message', 'detail'), '검사 완료')}</Typography.Paragraph></div><Tag color={statusTone(step.status || (step.success === false ? 'error' : 'success'))}>{asText(step.status, step.success === false ? '실패' : '성공')}</Tag></Flex>{step.latency_ms !== undefined && <Typography.Text type="secondary">응답시간 {step.latency_ms}ms</Typography.Text>}</Card>)}
            {!testResult.success && <Alert type="warning" showIcon message="조치 안내" description={testResult.guidance || '망 연결, 주소, TLS 인증서, 인증 정보와 조회 권한을 차례대로 확인해 주세요.'} />}
            {testResult.success && <Alert type="success" showIcon message="검증된 값을 적용하려면 설정을 저장하세요" action={<Button type="primary" size="small" onClick={() => { setTestOpen(false); void save() }}>저장하기</Button>} />}
          </Space>
        )}
      </Drawer>
    </>
  )
}
