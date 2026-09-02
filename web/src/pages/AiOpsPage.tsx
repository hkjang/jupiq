import { BulbOutlined, RobotOutlined, SendOutlined, StopOutlined } from '@ant-design/icons'
import { Alert, Avatar, Button, Card, Col, Flex, Input, Row, Segmented, Space, Statistic, Table, Tabs, Tag, Typography, type TableColumnsType } from 'antd'
import type { EChartsOption } from 'echarts'
import { useMemo, useRef, useState } from 'react'
import { streamAI } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { AsyncState } from '../components/AsyncState'
import { ChartCard } from '../components/ChartCard'
import { PageHeader } from '../components/PageHeader'
import { useApi } from '../hooks/useApi'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatDate, normalizePercentValue, pick, statusTone } from '../utils/format'

interface ChatMessage { id: string; role: 'user' | 'assistant'; content: string }

let messageSequence = 0
function newMessageID() {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID()
  const bytes = new Uint32Array(2)
  globalThis.crypto?.getRandomValues?.(bytes)
  messageSequence += 1
  return `message-${Date.now()}-${bytes[0].toString(16)}${bytes[1].toString(16)}-${messageSequence}`
}

const suggestions = [
  'JupyterHub 운영 점검 항목을 한국어로 정리해줘',
  '아래에 붙여 넣을 사용량 표를 분석할 기준을 제안해줘',
  '장시간 실행 서버를 검토할 때 확인할 위험 요소를 알려줘',
]

function records(value: unknown): ApiRecord[] {
  return Array.isArray(value) ? value.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object') : []
}

function OpsChat() {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [question, setQuestion] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)

  const ask = async (text = question) => {
    const trimmed = text.trim()
    if (!trimmed || streaming) return
    const userMessage: ChatMessage = { id: newMessageID(), role: 'user', content: trimmed }
    const assistantId = newMessageID()
    const history = [...messages, userMessage]
    setMessages([...history, { id: assistantId, role: 'assistant', content: '' }])
    setQuestion('')
    setError('')
    setStreaming(true)
    controller.current = new AbortController()
    try {
      await streamAI({
        messages: history.map(({ role, content }) => ({ role, content })),
        context: 'operations',
      }, (chunk) => {
        setMessages((current) => current.map((message) => message.id === assistantId ? { ...message, content: message.content + chunk } : message))
      }, controller.current.signal)
    } catch (caught) {
      if (!(caught instanceof DOMException && caught.name === 'AbortError')) setError(caught instanceof Error ? caught.message : 'AI 분석 요청에 실패했습니다.')
    } finally {
      setStreaming(false)
      controller.current = null
    }
  }

  return (
    <div className="ai-workspace">
      <Alert type="info" showIcon icon={<BulbOutlined />} message="v1.0은 스트리밍 AI proxy입니다" description="입력한 메시지만 설정된 Provider로 전달하며 jupiq DB·Prometheus 자료를 자동 첨부하거나 서버·정책을 변경하지 않습니다." />
      <div className="chat-suggestions" aria-label="추천 질문">{suggestions.map((suggestion) => <Button key={suggestion} onClick={() => void ask(suggestion)}>{suggestion}</Button>)}</div>
      <section className="chat-thread" aria-live="polite" aria-label="AI 운영 분석 대화">
        {!messages.length && <div className="chat-empty"><Avatar size={52} icon={<RobotOutlined />} /><Typography.Title level={3}>운영 질문을 입력해 보세요</Typography.Title><Typography.Paragraph type="secondary">필요한 비민감 자료를 직접 입력하면 OpenAI-compatible Provider 응답을 실시간으로 표시합니다.</Typography.Paragraph></div>}
        {messages.map((message) => <article key={message.id} className={`chat-message ${message.role}`}><Avatar icon={message.role === 'assistant' ? <RobotOutlined /> : undefined}>{message.role === 'user' ? '나' : undefined}</Avatar><div><strong>{message.role === 'assistant' ? 'jupiq AI' : '관리자'}</strong><pre>{message.content || (streaming ? '분석을 시작하고 있습니다…' : '응답이 없습니다.')}</pre></div></article>)}
      </section>
      {error && <Alert type="error" showIcon message="AI 분석 오류" description={error} closable onClose={() => setError('')} />}
      <Flex className="chat-composer" gap={8} align="end">
        <Input.TextArea value={question} onChange={(event) => setQuestion(event.target.value)} onPressEnter={(event) => { if (!event.shiftKey) { event.preventDefault(); void ask() } }} autoSize={{ minRows: 2, maxRows: 6 }} placeholder="예: JupyterHub 운영 점검 항목을 한국어로 정리해줘" aria-label="AI 운영 분석 질문" disabled={streaming} />
        {streaming ? <Button danger icon={<StopOutlined />} onClick={() => controller.current?.abort()}>중지</Button> : <Button type="primary" icon={<SendOutlined />} onClick={() => void ask()} disabled={!question.trim()}>전송</Button>}
      </Flex>
    </div>
  )
}

function LlmUsagePanel() {
  const [range, setRange] = useState<'day' | 'week' | 'month'>('day')
  const { data, loading, error, reload } = useApi<ApiRecord>(`/llm-usage?range=${range}&group_by=detail`)
  const details = records(data?.data || data?.items || data?.top_callers || data?.usage || data?.breakdown)
  const calls = details.reduce((sum, row) => sum + asNumber(pick(row, 'requests', 'request_count', 'calls')), 0)
  const successes = details.reduce((sum, row) => sum + asNumber(pick(row, 'success', 'success_count')), 0)
  const costs = details.reduce((sum, row) => sum + asNumber(pick(row, 'estimated_cost', 'cost')), 0)
  const summary = data?.summary && typeof data.summary === 'object' ? data.summary as ApiRecord : { calls, success_rate: calls ? successes / calls * 100 : 0, estimated_cost: costs }
  const trend = records(data?.usage_trend || data?.trend)
  const top = records(data?.top_callers)
  const stale = Boolean(data?.stale)

  const tokenText = (value: unknown) => value === undefined || value === null ? '수집 불가' : asNumber(value).toLocaleString('ko-KR')
  const money = (value: unknown) => value === undefined || value === null ? '수집 불가' : new Intl.NumberFormat('ko-KR', { style: 'currency', currency: 'KRW', maximumFractionDigits: 0 }).format(asNumber(value))
  const columns: TableColumnsType<ApiRecord> = [
    { title: '사용자', key: 'username', width: 140, render: (_value, row) => asText(pick(row, 'username', 'user', 'group')) },
    { title: 'Pod', key: 'pod', width: 210, render: (_value, row) => asText(pick(row, 'pod', 'pod_name')) },
    { title: '모델', key: 'model', width: 150, render: (_value, row) => asText(row.model) },
    { title: '호출', key: 'requests', width: 100, render: (_value, row) => asNumber(pick(row, 'requests', 'request_count', 'calls')).toLocaleString('ko-KR') },
    { title: '성공률', key: 'success_rate', width: 100, render: (_value, row) => `${normalizePercentValue(row.success_rate !== undefined ? row.success_rate : asNumber(row.calls) ? asNumber(row.success) / asNumber(row.calls) : 0).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}%` },
    { title: 'P95', key: 'p95', width: 100, render: (_value, row) => pick(row, 'p95_ms', 'latency_p95_ms') === undefined ? '수집 불가' : `${asNumber(pick(row, 'p95_ms', 'latency_p95_ms')).toLocaleString('ko-KR')}ms` },
    { title: 'Input tokens', key: 'input', width: 130, render: (_value, row) => tokenText(pick(row, 'input_tokens')) },
    { title: 'Output tokens', key: 'output', width: 135, render: (_value, row) => tokenText(pick(row, 'output_tokens')) },
    { title: '추정 비용', key: 'cost', width: 130, render: (_value, row) => money(pick(row, 'estimated_cost', 'cost')) },
  ]

  const trendOption: EChartsOption = useMemo(() => ({
    tooltip: { trigger: 'axis' }, legend: { bottom: 0 }, grid: { left: 55, right: 24, top: 20, bottom: 50 },
    xAxis: { type: 'category', data: trend.map((row) => asText(pick(row, 'bucket', 'time', 'timestamp', 'label'), '')) }, yAxis: { type: 'value' },
    series: [{ name: '호출 수', type: 'line', smooth: true, areaStyle: { opacity: 0.1 }, data: trend.map((row) => asNumber(pick(row, 'requests', 'calls'))) }, { name: '오류 수', type: 'line', smooth: true, data: trend.map((row) => asNumber(pick(row, 'errors', 'failed'))) }],
  }), [trend])
  const topOption: EChartsOption = useMemo(() => ({
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } }, grid: { left: 100, right: 24, top: 20, bottom: 28 },
    xAxis: { type: 'value' }, yAxis: { type: 'category', data: top.map((row) => asText(pick(row, 'username', 'user', 'group'), '')).reverse() },
    series: [{ type: 'bar', data: top.map((row) => asNumber(pick(row, 'requests', 'calls'))).reverse(), itemStyle: { color: '#7c3aed', borderRadius: [0, 6, 6, 0] } }],
  }), [top])

  return (
    <>
      <Flex justify="space-between" gap={12} wrap><Space direction="vertical" size={0}><Typography.Title level={3}>LLM API 사용량</Typography.Title><Typography.Text type="secondary">마지막 수집 {formatDate(data?.data_freshness || data?.sampled_at)}</Typography.Text></Space><Segmented value={range} options={[{ label: '일', value: 'day' }, { label: '주', value: 'week' }, { label: '월', value: 'month' }]} onChange={(value) => setRange(value as typeof range)} /></Flex>
      <Alert className="data-note" type="info" showIcon message="프롬프트와 응답 본문은 수집하지 않습니다" description="운영 메타데이터만 집계하며 token metric이 없는 소스는 ‘수집 불가’로 표시합니다." />
      {stale && <Alert className="data-note" type="warning" showIcon message="LLM 사용량 데이터가 최신 상태가 아닙니다" description="Prometheus 수집 연동을 확인해 주세요." />}
      <AsyncState loading={loading} error={error} onRetry={reload} empty={!loading && !error && !data} emptyDescription="수집된 LLM API 사용량이 없습니다.">
        <Row gutter={[16, 16]} className="kpi-grid">
          <Col xs={12} xl={6}><Card><Statistic title="전체 호출" value={asNumber(pick(summary, 'requests', 'request_count', 'calls'))} /></Card></Col>
          <Col xs={12} xl={6}><Card><Statistic title="성공률" value={normalizePercentValue(summary.success_rate)} suffix="%" precision={1} /></Card></Col>
          <Col xs={12} xl={6}><Card><Statistic title="P95 지연" value={pick(summary, 'p95_ms', 'latency_p95_ms') === undefined ? '수집 불가' : asNumber(pick(summary, 'p95_ms', 'latency_p95_ms'))} suffix={pick(summary, 'p95_ms', 'latency_p95_ms') === undefined ? undefined : 'ms'} /></Card></Col>
          <Col xs={12} xl={6}><Card><Statistic title="추정 비용" value={money(pick(summary, 'estimated_cost', 'cost'))} /></Card></Col>
        </Row>
        <div className="chart-grid two"><ChartCard title="시간대별 호출 추세" option={trendOption} empty={!trend.length} /><ChartCard title="Top 호출자" option={topOption} empty={!top.length} /></div>
        <Card title="사용자 → Pod → 모델 상세" extra={<Tag color={stale ? 'warning' : statusTone('success')}>{stale ? 'stale' : '최신'}</Tag>}><Table<ApiRecord> rowKey={(row) => `${asText(row.username)}-${asText(pick(row, 'pod_name', 'pod'))}-${asText(row.model)}`} columns={columns} dataSource={details} scroll={{ x: 1200 }} /></Card>
      </AsyncState>
    </>
  )
}

export function AiOpsPage() {
  const { features } = useAuth()
  const items = [{ key: 'analysis', label: 'AI 운영 분석', children: <OpsChat /> }]
  if (features.llmUsageMonitoring) items.push({ key: 'usage', label: 'LLM API 사용량', children: <LlmUsagePanel /> })
  return (
    <>
      <PageHeader title="AI 운영 분석" description="입력한 메시지에 대한 Provider 응답을 기본 스트리밍으로 받습니다." />
      <Tabs items={items} />
    </>
  )
}
