import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import {
  Alert,
  App,
  Button,
  Card,
  Drawer,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  type TableColumnsType,
} from 'antd'
import { useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { jsonBody, request } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { useList } from '../hooks/useList'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatBytes, formatDate, formatPercent, pick, statusTone } from '../utils/format'
import { hasPermissionForTargetMode } from '../utils/permissions'
import { stableRowKey } from '../utils/rowKey'
import { sortKindForFormat, sorterFor } from '../utils/sorting'
import { AsyncState } from './AsyncState'
import { PageHeader } from './PageHeader'

type FieldFormat = 'text' | 'status' | 'date' | 'percent' | 'bytes' | 'number' | 'tags' | 'boolean'

export interface ResourceColumn {
  title: string
  keys: string[]
  format?: FieldFormat
  width?: number
  suffix?: string
  sortable?: boolean
  // Sent as ?sort= when the server orders this list. Without it the column
  // sorts only the rows already on screen.
  sortKey?: string
  render?: (value: unknown, record: ApiRecord) => ReactNode
}

export interface ResourceField {
  name: string
  label: string
  type?: 'text' | 'password' | 'number' | 'textarea' | 'select' | 'switch'
  required?: boolean
  placeholder?: string
  options?: { label: string; value: string | number }[]
  initialValue?: unknown
  help?: string
  min?: number
  max?: number
}

export interface RowAction {
  key: string
  label: string
  icon?: ReactNode
  method?: 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: (record: ApiRecord) => string
  danger?: boolean
  confirm?: string
  body?: (record: ApiRecord) => unknown
  visible?: (record: ApiRecord) => boolean
  permission?: string
}

export interface ResourceEditorTest {
  label?: string
  run: (values: ApiRecord, editing: ApiRecord | null) => Promise<ApiRecord & { success: boolean }>
}

interface Props {
  title: string
  description: string
  endpoint: string
  columns: ResourceColumn[]
  emptyDescription: string
  rowActions?: RowAction[]
  fields?: ResourceField[]
  createLabel?: string
  dataNote?: ReactNode
  writePermission?: string
  scopeAwareWrite?: boolean
  globalCreate?: boolean
  editorVariant?: 'modal' | 'drawer'
  editorTest?: ResourceEditorTest
}

const statusLabels: Record<string, string> = {
  running: '실행 중', stopped: '중지', active: '활성', inactive: '비활성', enabled: '사용', disabled: '사용 안 함',
  healthy: '정상', degraded: '성능 저하', error: '오류', critical: '심각', warning: '주의', normal: '정상',
  pending: '대기', approved: '승인', rejected: '반려', review: '검토 중', open: '진행 중', resolved: '해결',
  online: '온라인', offline: '오프라인', success: '성공', failed: '실패', revoked: '폐기', expired: '만료',
}

function translatedStatus(value: unknown) {
  const text = asText(value)
  return statusLabels[text.toLowerCase()] || text
}

function renderValue(value: unknown, format: FieldFormat = 'text', suffix?: string) {
  if (value === null || value === undefined || value === '') return '—'
  if (format === 'status') return <Tag color={statusTone(value)}>{translatedStatus(value)}</Tag>
  if (format === 'date') return formatDate(value)
  if (format === 'percent') return formatPercent(value)
  if (format === 'bytes') return formatBytes(value)
  if (format === 'number') return `${asNumber(value).toLocaleString('ko-KR')}${suffix || ''}`
  if (format === 'boolean') return <Tag color={value ? 'blue' : 'default'}>{value ? '사용' : '사용 안 함'}</Tag>
  if (format === 'tags') {
    const items = Array.isArray(value) ? value : value ? String(value).split(',') : []
    return items.length ? <Space size={[0, 4]} wrap>{items.map((item) => <Tag key={String(item)}>{asText(item)}</Tag>)}</Space> : '—'
  }
  return asText(value)
}

function rowId(record: ApiRecord) {
  return String(pick(record, 'id', 'key', 'uuid', 'name') ?? '')
}

function rowKeyOf(record: ApiRecord) {
  return stableRowKey(record, rowId(record))
}

export function ResourceListPage({ title, description, endpoint, columns, emptyDescription, rowActions = [], fields, createLabel = '새 항목 등록', dataNote, writePermission, scopeAwareWrite = false, globalCreate = false, editorVariant = 'modal', editorTest }: Props) {
  const { message, modal } = App.useApp()
  const { user, hasGlobalPermission } = useAuth()
  const [searchParams, setSearchParams] = useSearchParams()
  const queryParam = searchParams.get('search') || ''
  const [query, setQuery] = useState(() => queryParam)
  const deferredQuery = useDeferredValue(query.trim())
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [sort, setSort] = useState<{ key: string; order: 'asc' | 'desc' } | null>(null)
  const listPath = useMemo(() => {
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) })
    if (deferredQuery) params.set('search', deferredQuery)
    if (sort) { params.set('sort', sort.key); params.set('order', sort.order) }
    return `${endpoint}?${params.toString()}`
  }, [endpoint, page, pageSize, deferredQuery, sort])
  const { data, meta, loading, refreshing, error, reload } = useList<ApiRecord>(listPath)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<ApiRecord | null>(null)
  const [saving, setSaving] = useState(false)
  const [editorTesting, setEditorTesting] = useState(false)
  const [editorTestResult, setEditorTestResult] = useState<(ApiRecord & { success: boolean }) | null>(null)
  const [acting, setActing] = useState<{ row: string; action: string } | null>(null)
  const [form] = Form.useForm()
  const permitsWrite = !writePermission || hasPermissionForTargetMode(user?.permissions, user?.global_permissions, writePermission, scopeAwareWrite)
  const canModify = Boolean(fields) && permitsWrite
  const canCreate = canModify && (!globalCreate || !writePermission || hasGlobalPermission(writePermission))

  useEffect(() => {
    setQuery((current) => current === queryParam ? current : queryParam)
    setPage(1)
  }, [queryParam])

  // Pages declare `fields` inline, so the array identity changes on every parent
  // render. Keying the reset on that identity wiped whatever the user had just
  // typed or selected; the editor only needs to be seeded when it opens or when
  // the edited record changes.
  const fieldsRef = useRef(fields)
  fieldsRef.current = fields
  useEffect(() => {
    if (!editorOpen) return
    const current = fieldsRef.current
    if (editing) {
      form.resetFields()
      form.setFieldsValue(editing)
    } else if (current) {
      form.resetFields()
      const initial = Object.fromEntries(current.filter((field) => field.initialValue !== undefined).map((field) => [field.name, field.initialValue]))
      form.setFieldsValue(initial)
    }
  }, [editing, editorOpen, form])

  const executeAction = async (action: RowAction, record: ApiRecord) => {
    const execute = async () => {
      setActing({ row: rowKeyOf(record), action: action.key })
      try {
        const result = await request<ApiRecord | null>(action.path(record), {
          method: action.method || 'POST',
          body: action.body ? jsonBody(action.body(record)) : undefined,
        })
        if (result?.approval_required === true) {
          message.info('승인 요청이 등록되었습니다. 검토·승인 완료 후 작업이 실행됩니다.')
        } else if (result?.success === false) {
          message.error(asText(pick(result, 'error', 'message'), `${action.label} 검증에 실패했습니다.`))
        } else {
          message.success(`${action.label} 작업을 완료했습니다.`)
        }
        await reload()
      } catch (caught) {
        message.error(caught instanceof Error ? caught.message : `${action.label} 작업에 실패했습니다.`)
      } finally {
        setActing(null)
      }
    }
    if (action.confirm) {
      modal.confirm({ title: action.label, content: action.confirm, okText: action.label, cancelText: '취소', okButtonProps: { danger: action.danger }, onOk: execute })
    } else await execute()
  }

  const openCreate = () => { setEditing(null); setEditorTestResult(null); setEditorOpen(true) }
  const openEdit = (record: ApiRecord) => { setEditing(record); setEditorTestResult(null); setEditorOpen(true) }
  const closeEditor = () => {
    if (saving || editorTesting) return
    setEditorOpen(false)
    setEditing(null)
    setEditorTestResult(null)
  }

  const testEditorValues = async () => {
    if (!editorTest || editorTesting || saving) return
    try {
      const values = await form.validateFields()
      setEditorTesting(true)
      setEditorTestResult(null)
      const result = await editorTest.run(values, editing)
      setEditorTestResult(result)
    } catch (caught) {
      if (caught && typeof caught === 'object' && 'errorFields' in caught) return
      setEditorTestResult({ success: false, error: caught instanceof Error ? caught.message : '연결 테스트에 실패했습니다.' })
    } finally {
      setEditorTesting(false)
    }
  }

  const remove = (record: ApiRecord) => {
    const identifier = rowId(record)
    if (!identifier) {
      message.error('식별자가 없는 항목은 삭제할 수 없습니다.')
      return
    }
    modal.confirm({
      title: '항목 삭제',
      content: '이 항목을 삭제하시겠습니까? 관련 운영에 영향을 줄 수 있습니다.',
      okText: '삭제', cancelText: '취소', okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await request(`${endpoint}/${encodeURIComponent(identifier)}`, { method: 'DELETE' })
          message.success('항목을 삭제했습니다.')
          await reload()
        } catch (caught) {
          message.error(caught instanceof Error ? caught.message : '항목을 삭제하지 못했습니다.')
        }
      },
    })
  }

  const save = async (values: ApiRecord) => {
    setSaving(true)
    try {
      const id = editing ? rowId(editing) : ''
      const wrappedEndpoints = ['/projects', '/policies', '/profiles', '/images', '/incidents', '/notifications']
      let payload: ApiRecord = values
      if (wrappedEndpoints.includes(endpoint)) {
        const { name, status, owner_user_id, ...resourceData } = values
        payload = { name, status, owner_user_id, data: resourceData }
      }
      await request(id ? `${endpoint}/${encodeURIComponent(id)}` : endpoint, {
        method: id ? 'PUT' : 'POST', body: jsonBody(payload),
      })
      message.success(editing ? '변경 사항을 저장했습니다.' : '새 항목을 등록했습니다.')
      setEditorOpen(false)
      setEditing(null)
      setEditorTestResult(null)
      await reload()
    } catch (caught) {
      message.error(caught instanceof Error ? caught.message : '저장하지 못했습니다.')
    } finally {
      setSaving(false)
    }
  }

  const tableColumns = useMemo<TableColumnsType<ApiRecord>>(() => {
    const configured: TableColumnsType<ApiRecord> = columns.map((column) => ({
      title: column.title,
      key: column.keys.join('-'),
      width: column.width,
      sorter: column.sortable === false ? undefined : column.sortKey ? true : sorterFor(column.keys, sortKindForFormat(column.format)),
      sortOrder: column.sortKey && sort?.key === column.sortKey ? (sort.order === 'desc' ? 'descend' : 'ascend') : undefined,
      showSorterTooltip: false,
      render: (_value, record) => {
        const value = pick(record, ...column.keys)
        return column.render ? column.render(value, record) : renderValue(value, column.format, column.suffix)
      },
    }))
    const permittedRowActions = rowActions.filter((action) => !action.permission || hasPermissionForTargetMode(user?.permissions, user?.global_permissions, action.permission, scopeAwareWrite))
    if (canModify || permittedRowActions.length) {
      // Every row action is a plain, always-visible button. The earlier ⋯ menu
      // was an antd Dropdown rendered into <body> through a portal, and popups
      // rendered that way have been unreachable in at least one deployment
      // where every in-tree control works. A direct button has no popup,
      // no positioning and nothing to clip - the click either happens or it
      // doesn't, and it is visible on the row at all times.
      const buttonCount = permittedRowActions.length + (canModify ? 2 : 0)
      configured.push({
        title: '작업', key: 'actions', width: 24 + buttonCount * 38, fixed: 'right',
        render: (_value, record) => {
          const rowKey = rowKeyOf(record)
          const rowBusy = acting?.row === rowKey
          const actions = permittedRowActions.filter((action) => !action.visible || action.visible(record))
          return (
            <Space size={4} className="row-actions">
              {actions.map((action) => (
                <Button
                  key={action.key}
                  size="small"
                  danger={action.danger}
                  icon={action.icon}
                  loading={rowBusy && acting?.action === action.key}
                  disabled={Boolean(acting) && !(rowBusy && acting?.action === action.key)}
                  title={action.label}
                  aria-label={action.label}
                  onClick={() => void executeAction(action, record)}
                >
                  {action.icon ? undefined : action.label}
                </Button>
              ))}
              {canModify && <Button size="small" icon={<EditOutlined />} disabled={Boolean(acting)} title="수정" aria-label="수정" onClick={() => openEdit(record)} />}
              {canModify && <Button size="small" danger icon={<DeleteOutlined />} disabled={Boolean(acting)} title="삭제" aria-label="삭제" onClick={() => remove(record)} />}
            </Space>
          )
        },
      })
    }
    return configured
  }, [columns, rowActions, acting, canModify, scopeAwareWrite, user?.global_permissions, user?.permissions])

  const updateQuery = (value: string) => {
    setQuery(value)
    setPage(1)
    const next = new URLSearchParams(searchParams)
    if (value.trim()) next.set('search', value)
    else next.delete('search')
    setSearchParams(next, { replace: true })
  }

  const editorTitle = editing ? `${title} 수정` : createLabel
  const editorForm = fields && (
    <Form
      form={form}
      layout="vertical"
      onFinish={save}
      onValuesChange={() => setEditorTestResult(null)}
      requiredMark="optional"
      disabled={saving || editorTesting}
    >
      {fields.map((field) => (
        <Form.Item key={field.name} name={field.name} label={field.label} valuePropName={field.type === 'switch' ? 'checked' : 'value'} rules={field.required && !(editing && field.type === 'password') ? [{ required: true, message: `${field.label}을(를) 입력해 주세요.` }] : undefined} help={field.help}>
          {field.type === 'textarea' ? <Input.TextArea rows={4} placeholder={field.placeholder} />
            : field.type === 'password' ? <Input.Password autoComplete="new-password" placeholder={editing ? '비워 두면 기존 값을 유지합니다' : field.placeholder} />
              : field.type === 'number' ? <InputNumber min={field.min} max={field.max} style={{ width: '100%' }} placeholder={field.placeholder} />
                : field.type === 'select' ? <Select options={field.options} placeholder={field.placeholder} />
                  : field.type === 'switch' ? <Switch checkedChildren="사용" unCheckedChildren="사용 안 함" />
                    : <Input placeholder={field.placeholder} />}
        </Form.Item>
      ))}
      {editorTestResult && (() => {
        const version = asText(pick(editorTestResult, 'version', 'remote_version'), '')
        const latency = pick(editorTestResult, 'latency_ms', 'response_time_ms')
        const steps = pick(editorTestResult, 'steps', 'checks')
        const guidance = asText(pick(editorTestResult, 'guidance', 'remediation'), '')
        return <Alert
          showIcon
          type={editorTestResult.success ? 'success' : 'error'}
          message={editorTestResult.success ? '현재 입력값으로 연결 검증을 통과했습니다' : '현재 입력값으로 연결하지 못했습니다'}
          description={<Space direction="vertical" size={8} style={{ width: '100%' }}>
            <Flex gap={8} wrap>{version && <Tag>버전 {version}</Tag>}{latency !== undefined && <Tag>응답 {asNumber(latency)}ms</Tag>}</Flex>
            {Boolean(editorTestResult.error) && <Typography.Text>{asText(editorTestResult.error)}</Typography.Text>}
            {Array.isArray(steps) && steps.map((rawStep, index) => {
              const step = rawStep && typeof rawStep === 'object' ? rawStep as ApiRecord : {}
              const successful = step.success !== false
              return <Card key={`${asText(step.name)}-${index}`} size="small"><Flex justify="space-between" gap={12}><div><strong>{asText(step.name, `검사 ${index + 1}`)}</strong><div><Typography.Text type="secondary">{asText(pick(step, 'detail', 'message'), '검사 완료')}</Typography.Text></div></div><Space direction="vertical" align="end" size={2}><Tag color={successful ? 'success' : 'error'}>{successful ? '성공' : '실패'}</Tag>{step.latency_ms !== undefined && <Typography.Text type="secondary">{asNumber(step.latency_ms)}ms</Typography.Text>}</Space></Flex></Card>
            })}
            {!editorTestResult.success && guidance && <Typography.Text type="secondary">조치: {guidance}</Typography.Text>}
            <Typography.Text type="secondary">연결 테스트는 입력값을 저장하지 않습니다. 적용하려면 별도로 저장하세요.</Typography.Text>
          </Space>}
        />
      })()}
    </Form>
  )

  return (
    <>
      <PageHeader
        title={title}
        description={description}
        onRefresh={reload}
        refreshing={refreshing}
        extra={canCreate && <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{createLabel}</Button>}
      />
      {dataNote && <div className="data-note">{dataNote}</div>}
      <div className="table-toolbar">
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder={`${title} 검색`}
          value={query}
          onChange={(event) => updateQuery(event.target.value)}
          aria-label={`${title} 검색`}
        />
        <Typography.Text type="secondary">총 {meta.total ?? data.length}건</Typography.Text>
      </div>
      <AsyncState loading={loading} refreshing={refreshing} error={error} onRetry={reload} empty={!loading && !error && data.length === 0} emptyDescription={query ? '검색 결과가 없습니다.' : emptyDescription}>
        <Table<ApiRecord>
          rowKey={rowKeyOf}
          loading={refreshing}
          columns={tableColumns}
          dataSource={data}
          scroll={{ x: Math.max(900, tableColumns.length * 150) }}
          onChange={(_pagination, _filters, sorter) => {
            // Only server-ordered columns reach here with a sortKey; the rest
            // are sorted in place by antd and need no request.
            const active = Array.isArray(sorter) ? sorter[0] : sorter
            const key = columns.find((column) => column.keys.join('-') === String(active?.columnKey ?? ''))?.sortKey
            if (!key) return
            const next = active?.order === 'ascend' ? { key, order: 'asc' as const } : active?.order === 'descend' ? { key, order: 'desc' as const } : null
            setSort((current) => (current?.key === next?.key && current?.order === next?.order ? current : next))
            setPage(1)
          }}
          pagination={{
            current: page,
            pageSize,
            total: meta.total ?? data.length,
            showSizeChanger: true,
            showTotal: (total) => `총 ${total}건`,
            onChange: (nextPage, nextPageSize) => {
              setPage(nextPageSize !== pageSize ? 1 : nextPage)
              setPageSize(nextPageSize)
            },
          }}
        />
      </AsyncState>
      {canModify && fields && editorVariant === 'drawer' && (
        <Drawer
          title={editorTitle}
          width="min(620px, 100vw)"
          open={editorOpen}
          onClose={closeEditor}
          closable={!saving && !editorTesting}
          maskClosable={!saving && !editorTesting}
          destroyOnHidden
          footer={<Flex justify="end" gap={8} wrap>
            <Button onClick={closeEditor} disabled={saving || editorTesting}>취소</Button>
            {editorTest && <Button icon={<ApiOutlined />} onClick={() => void testEditorValues()} loading={editorTesting} disabled={saving}>{editorTest.label || '연결 테스트'}</Button>}
            <Button type="primary" onClick={() => form.submit()} loading={saving} disabled={editorTesting}>저장</Button>
          </Flex>}
        >
          {editorForm}
        </Drawer>
      )}
      {canModify && fields && editorVariant === 'modal' && (
        <Modal title={editorTitle} open={editorOpen} onCancel={closeEditor} onOk={() => form.submit()} confirmLoading={saving} okText="저장" cancelText="취소" destroyOnHidden>
          {editorForm}
        </Modal>
      )}
    </>
  )
}
