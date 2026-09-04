import { Alert, App, Button, Flex, Input, Modal, Result, Space, Table, Tag, Typography, type TableColumnsType } from 'antd'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { jsonBody, request } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { AsyncState } from '../components/AsyncState'
import { PageHeader } from '../components/PageHeader'
import { useList } from '../hooks/useList'
import type { ApiRecord } from '../types'
import { asText, formatDate, pick, statusTone } from '../utils/format'
import { stableRowKey } from '../utils/rowKey'
import { sorterFor } from '../utils/sorting'

type ApprovalAction = 'review' | 'approve' | 'reject'

const actionLabels: Record<ApprovalAction, string> = { review: '검토 완료', approve: '승인', reject: '반려' }
const statusLabels: Record<string, string> = {
  pending_review: '팀장 검토 대기', pending: '승인 대기', executing: '실행 중',
  approved: '승인 완료', rejected: '반려', failed: '실행 실패',
}
const serverActions: Record<string, string> = { start: '서버 시작', stop: '서버 종료', restart: '서버 재시작' }

function requiresReason(row: ApiRecord) {
  const workflow = row.workflow && typeof row.workflow === 'object' && !Array.isArray(row.workflow)
    ? row.workflow as ApiRecord
    : {}
  return Boolean(workflow.require_reason ?? row.require_reason)
}

function requestSummary(row: ApiRecord) {
  const requestType = asText(pick(row, 'request_type', 'type'), '')
  if (requestType === 'server_action') {
    return `${serverActions[asText(row.action, '')] || asText(row.action)} · 서버 #${asText(row.server_id)}`
  }
  return asText(pick(row, 'summary', 'description', 'name'))
}

export function ApprovalsPage() {
  const { message } = App.useApp()
  const { features, isAdmin, hasGlobalPermission } = useAuth()
  const { data, loading, refreshing, error, reload } = useList<ApiRecord>('/approvals')
  const [selected, setSelected] = useState<{ row: ApiRecord; action: ApprovalAction } | null>(null)
  const [reason, setReason] = useState('')
  const [actionError, setActionError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const openAction = (row: ApiRecord, action: ApprovalAction) => {
    setSelected({ row, action })
    setReason('')
    setActionError('')
  }

  const closeAction = () => {
    if (submitting) return
    setSelected(null)
    setReason('')
    setActionError('')
  }

  const submitAction = async () => {
    if (!selected) return
    if (requiresReason(selected.row) && !reason.trim()) {
      setActionError('관리자 설정에 따라 처리 사유를 반드시 입력해야 합니다.')
      return
    }
    setSubmitting(true)
    setActionError('')
    try {
      const approvalID = encodeURIComponent(asText(pick(selected.row, 'id', 'uuid'), ''))
      await request(`/approvals/${approvalID}/${selected.action}`, {
        method: 'POST',
        body: jsonBody({ reason: reason.trim() }),
      })
      message.success(`${actionLabels[selected.action]} 처리를 완료했습니다.`)
      setSelected(null)
      setReason('')
      await reload()
    } catch (caught) {
      const messageText = caught instanceof Error ? caught.message : `${actionLabels[selected.action]} 처리에 실패했습니다.`
      setActionError(messageText)
      message.error(messageText)
    } finally {
      setSubmitting(false)
    }
  }

  const columns: TableColumnsType<ApiRecord> = [
    { title: '요청 번호', key: 'id', width: 120, sorter: sorterFor(['request_no', 'id']), showSorterTooltip: false, render: (_value, row) => `#${asText(pick(row, 'request_no', 'id'))}` },
    { title: '요청자', key: 'requester', width: 140, sorter: sorterFor(['requested_by', 'requester_name', 'username']), showSorterTooltip: false, render: (_value, row) => asText(pick(row, 'requested_by', 'requester_name', 'username')) },
    { title: '요청 유형', key: 'type', width: 160, render: (_value, row) => asText(pick(row, 'request_type', 'type')) === 'server_action' ? '서버 작업' : asText(pick(row, 'request_type', 'type')) },
    { title: '요청 내용', key: 'summary', width: 280, render: (_value, row) => requestSummary(row) },
    { title: '상태', key: 'status', width: 140, sorter: sorterFor(['status']), showSorterTooltip: false, render: (_value, row) => {
      const status = asText(row.status, '')
      return <Tag color={statusTone(status)}>{statusLabels[status] || status}</Tag>
    } },
    { title: '요청 시각', key: 'created_at', width: 180, sorter: sorterFor(['requested_at', 'created_at'], 'date'), showSorterTooltip: false, render: (_value, row) => formatDate(pick(row, 'requested_at', 'created_at')) },
    { title: '처리', key: 'actions', fixed: 'right', width: 180, render: (_value, row) => {
      const status = asText(row.status, '')
      if (status === 'pending_review' && hasGlobalPermission('approval:review')) {
        return <Button type="primary" size="small" onClick={() => openAction(row, 'review')}>검토</Button>
      }
      if (status === 'pending' && hasGlobalPermission('approval:approve')) {
        return <Space><Button type="primary" size="small" onClick={() => openAction(row, 'approve')}>승인</Button><Button danger size="small" onClick={() => openAction(row, 'reject')}>반려</Button></Space>
      }
      return <Typography.Text type="secondary">처리 권한 없음</Typography.Text>
    } },
  ]

  if (!features.approvalWorkflow) {
    return <Result status="info" title="검토·승인 프로세스가 비활성화되어 있습니다" subTitle="비활성 상태에서는 대상 작업이 승인 단계 없이 즉시 처리됩니다." extra={isAdmin ? <Button type="primary"><Link to="/admin/settings?tab=workflow">관리자 설정에서 사용</Link></Button> : undefined} />
  }

  return (
    <>
      <PageHeader title="검토·승인" description="팀장 검토와 최종 승인 권한을 분리해 운영 요청을 안전하게 처리합니다." onRefresh={reload} refreshing={refreshing} />
      <AsyncState loading={loading} refreshing={refreshing} error={error} onRetry={reload} empty={!loading && !error && data.length === 0} emptyDescription="검토하거나 승인할 요청이 없습니다.">
        <Table<ApiRecord> rowKey={(row) => stableRowKey(row, row.id)} loading={refreshing} columns={columns} dataSource={data} scroll={{ x: 1100 }} pagination={{ pageSize: 20 }} />
      </AsyncState>
      <Modal
        title={selected ? `${actionLabels[selected.action]} 사유` : '승인 요청 처리'}
        open={Boolean(selected)}
        onCancel={closeAction}
        onOk={() => void submitAction()}
        okText={selected ? actionLabels[selected.action] : '확인'}
        cancelText="취소"
        confirmLoading={submitting}
        okButtonProps={{ danger: selected?.action === 'reject' }}
        maskClosable={!submitting}
      >
        <Flex vertical gap={12}>
          {selected && <Alert type="info" showIcon message={requestSummary(selected.row)} description={requiresReason(selected.row) ? '이 요청은 처리 사유 입력이 필수입니다.' : '사유 입력은 선택 사항입니다.'} />}
          {actionError && <Alert type="error" showIcon message="요청을 처리하지 못했습니다" description={actionError} />}
          <label htmlFor="approval-reason">처리 사유 {selected && requiresReason(selected.row) ? '(필수)' : '(선택)'}</label>
          <Input.TextArea id="approval-reason" value={reason} onChange={(event) => setReason(event.target.value)} rows={4} maxLength={1000} showCount placeholder="검토·승인·반려 근거를 입력하세요." status={actionError && !reason.trim() ? 'error' : undefined} />
        </Flex>
      </Modal>
    </>
  )
}
