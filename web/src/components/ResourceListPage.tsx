import { DeleteOutlined, EditOutlined, MoreOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons'
import {
  App,
  Button,
  Dropdown,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  type MenuProps,
  type TableColumnsType,
} from 'antd'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { jsonBody, request } from '../api/client'
import { useList } from '../hooks/useList'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatBytes, formatDate, formatPercent, pick, statusTone } from '../utils/format'
import { AsyncState } from './AsyncState'
import { PageHeader } from './PageHeader'

type FieldFormat = 'text' | 'status' | 'date' | 'percent' | 'bytes' | 'number' | 'tags' | 'boolean'

export interface ResourceColumn {
  title: string
  keys: string[]
  format?: FieldFormat
  width?: number
  suffix?: string
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
  method?: 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: (record: ApiRecord) => string
  danger?: boolean
  confirm?: string
  body?: (record: ApiRecord) => unknown
  visible?: (record: ApiRecord) => boolean
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
  return String(pick(record, 'id', 'key', 'uuid', 'name') || '')
}

export function ResourceListPage({ title, description, endpoint, columns, emptyDescription, rowActions = [], fields, createLabel = '새 항목 등록', dataNote }: Props) {
  const { message, modal } = App.useApp()
  const { data, meta, loading, error, reload } = useList<ApiRecord>(endpoint)
  const [query, setQuery] = useState(() => new URLSearchParams(window.location.search).get('search') || '')
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<ApiRecord | null>(null)
  const [saving, setSaving] = useState(false)
  const [acting, setActing] = useState('')
  const [form] = Form.useForm()

  useEffect(() => {
    if (editing) form.setFieldsValue(editing)
    else if (fields) {
      form.resetFields()
      const initial = Object.fromEntries(fields.filter((field) => field.initialValue !== undefined).map((field) => [field.name, field.initialValue]))
      form.setFieldsValue(initial)
    }
  }, [editing, fields, form])

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase('ko-KR')
    if (!needle) return data
    return data.filter((record) => Object.values(record).some((value) => asText(value, '').toLocaleLowerCase('ko-KR').includes(needle)))
  }, [data, query])

  const executeAction = async (action: RowAction, record: ApiRecord) => {
    const execute = async () => {
      const key = `${action.key}-${rowId(record)}`
      setActing(key)
      try {
        await request(action.path(record), {
          method: action.method || 'POST',
          body: action.body ? jsonBody(action.body(record)) : undefined,
        })
        message.success(`${action.label} 작업을 완료했습니다.`)
        await reload()
      } catch (caught) {
        message.error(caught instanceof Error ? caught.message : `${action.label} 작업에 실패했습니다.`)
      } finally {
        setActing('')
      }
    }
    if (action.confirm) {
      modal.confirm({ title: action.label, content: action.confirm, okText: action.label, cancelText: '취소', okButtonProps: { danger: action.danger }, onOk: execute })
    } else await execute()
  }

  const openCreate = () => { setEditing(null); setEditorOpen(true) }
  const openEdit = (record: ApiRecord) => { setEditing(record); setEditorOpen(true) }

  const remove = (record: ApiRecord) => {
    modal.confirm({
      title: '항목 삭제',
      content: '이 항목을 삭제하시겠습니까? 관련 운영에 영향을 줄 수 있습니다.',
      okText: '삭제', cancelText: '취소', okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await request(`${endpoint}/${encodeURIComponent(rowId(record))}`, { method: 'DELETE' })
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
      render: (_value, record) => {
        const value = pick(record, ...column.keys)
        return column.render ? column.render(value, record) : renderValue(value, column.format, column.suffix)
      },
    }))
    if (fields || rowActions.length) {
      configured.push({
        title: '작업', key: 'actions', width: 92, fixed: 'right',
        render: (_value, record) => {
          const actions = rowActions.filter((action) => !action.visible || action.visible(record))
          const menuItems: MenuProps['items'] = [
            ...actions.map((action) => ({ key: action.key, label: action.label, danger: action.danger, disabled: Boolean(acting) })),
            ...(fields ? [
              { type: 'divider' as const },
              { key: 'edit', icon: <EditOutlined />, label: '수정' },
              { key: 'delete', icon: <DeleteOutlined />, label: '삭제', danger: true },
            ] : []),
          ]
          return (
            <Dropdown menu={{ items: menuItems, onClick: ({ key }) => {
              if (key === 'edit') openEdit(record)
              else if (key === 'delete') remove(record)
              else {
                const action = actions.find((candidate) => candidate.key === key)
                if (action) void executeAction(action, record)
              }
            } }} trigger={['click']}>
              <Tooltip title="작업 메뉴"><Button loading={acting.endsWith(rowId(record))} icon={<MoreOutlined />} aria-label="작업 메뉴 열기" /></Tooltip>
            </Dropdown>
          )
        },
      })
    }
    return configured
  }, [columns, fields, rowActions, acting])

  return (
    <>
      <PageHeader
        title={title}
        description={description}
        onRefresh={reload}
        extra={fields && <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{createLabel}</Button>}
      />
      {dataNote && <div className="data-note">{dataNote}</div>}
      <div className="table-toolbar">
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder={`${title} 검색`}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          aria-label={`${title} 검색`}
        />
        <Typography.Text type="secondary">총 {meta.total ?? data.length}건</Typography.Text>
      </div>
      <AsyncState loading={loading} error={error} onRetry={reload} empty={!loading && !error && filtered.length === 0} emptyDescription={query ? '검색 결과가 없습니다.' : emptyDescription}>
        <Table<ApiRecord>
          rowKey={(record) => rowId(record)}
          columns={tableColumns}
          dataSource={filtered}
          scroll={{ x: Math.max(900, tableColumns.length * 150) }}
          pagination={{ pageSize: 20, showSizeChanger: true, showTotal: (total) => `총 ${total}건` }}
        />
      </AsyncState>
      {fields && (
        <Modal
          title={editing ? `${title} 수정` : createLabel}
          open={editorOpen}
          onCancel={() => { setEditorOpen(false); setEditing(null) }}
          onOk={() => form.submit()}
          confirmLoading={saving}
          okText="저장"
          cancelText="취소"
          destroyOnHidden
        >
          <Form form={form} layout="vertical" onFinish={save} requiredMark="optional">
            {fields.map((field) => (
              <Form.Item key={field.name} name={field.name} label={field.label} valuePropName={field.type === 'switch' ? 'checked' : 'value'} rules={field.required ? [{ required: true, message: `${field.label}을(를) 입력해 주세요.` }] : undefined} help={field.help}>
                {field.type === 'textarea' ? <Input.TextArea rows={4} placeholder={field.placeholder} />
                  : field.type === 'password' ? <Input.Password placeholder={editing ? '변경할 때만 입력' : field.placeholder} />
                    : field.type === 'number' ? <InputNumber min={field.min} max={field.max} style={{ width: '100%' }} placeholder={field.placeholder} />
                      : field.type === 'select' ? <Select options={field.options} placeholder={field.placeholder} />
                        : field.type === 'switch' ? <Switch checkedChildren="사용" unCheckedChildren="사용 안 함" />
                          : <Input placeholder={field.placeholder} />}
              </Form.Item>
            ))}
          </Form>
        </Modal>
      )}
    </>
  )
}
