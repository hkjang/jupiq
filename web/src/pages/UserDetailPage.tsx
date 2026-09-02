import {
  ArrowLeftOutlined,
  ClockCircleOutlined,
  CloudServerOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  UserOutlined,
} from '@ant-design/icons'
import {
  Alert,
  Avatar,
  Button,
  Card,
  Col,
  Descriptions,
  Empty,
  Flex,
  Row,
  Segmented,
  Space,
  Statistic,
  Table,
  Tabs,
  Tag,
  Timeline,
  Typography,
  type TableColumnsType,
} from 'antd'
import type { EChartsOption } from 'echarts'
import { useMemo } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { AsyncState } from '../components/AsyncState'
import { ChartCard } from '../components/ChartCard'
import { PageHeader } from '../components/PageHeader'
import { useApi } from '../hooks/useApi'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatBytes, formatDate, formatDuration, normalizePercentValue, pick, statusTone } from '../utils/format'
import { normalizeUserDetail, periodSummary, records, type UsagePeriod } from '../utils/userDetail'

const periodLabels: Record<UsagePeriod, string> = { day: '일', week: '주', month: '월' }
const stateLabels: Record<string, string> = {
  running: '실행 중', starting: '시작 중', stopped: '중지', pending: '대기', active: '활성', inactive: '비활성',
  healthy: '정상', degraded: '성능 저하', success: '성공', failed: '실패', error: '오류', unknown: '확인 불가',
}
const actionLabels: Record<string, string> = {
  'auth.login': '로그인', 'auth.oidc': 'SSO 로그인', 'server.started': '서버 시작', 'server.start': '서버 시작',
  'server.stop': '서버 종료', 'server.restart': '서버 재시작', 'server.activity': '서버 활동', 'hub.activity': 'Hub 활동',
}

function hasValue(value: unknown) {
  return value !== null && value !== undefined && value !== ''
}

function runtimeOf(row: ApiRecord) {
  const direct = pick(row, 'runtime_seconds', 'running_seconds')
  if (hasValue(direct)) return formatDuration(direct)
  const started = pick(row, 'started_at')
  if (!started) return '—'
  const timestamp = new Date(String(started)).getTime()
  return Number.isFinite(timestamp) ? formatDuration(Math.max(0, (Date.now() - timestamp) / 1000)) : '—'
}

function resourceTotal(rows: ApiRecord[], keys: string[]) {
  return rows.reduce((total, row) => total + asNumber(pick(row, ...keys)), 0)
}

function stateLabel(value: unknown) {
  const state = asText(value, '')
  return stateLabels[state.toLowerCase()] || state || '확인 불가'
}

function OverviewTab({ view, gpuEnabled }: { view: ReturnType<typeof normalizeUserDetail>; gpuEnabled: boolean }) {
  const identity = view.user || view.hubs[0] || {}
  const cpu = resourceTotal(view.currentServers, ['cpu_cores', 'cpu'])
  const memory = resourceTotal(view.currentServers, ['memory_bytes', 'memory'])
  const gpu = resourceTotal(view.currentServers, ['gpu_count', 'gpus'])
  const vram = resourceTotal(view.currentServers, ['vram_bytes', 'gpu_memory_bytes'])

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card>
        <Flex gap={18} align="center" wrap>
          <Avatar size={64} icon={<UserOutlined />} />
          <div>
            <Typography.Title level={3} style={{ margin: 0 }}>{asText(pick(identity, 'display_name', 'name'), view.username)}</Typography.Title>
            <Typography.Text type="secondary">{view.username}</Typography.Text>
          </div>
          <Tag color={Boolean(pick(identity, 'active') ?? true) ? 'success' : 'default'}>{Boolean(pick(identity, 'active') ?? true) ? '활성' : '비활성'}</Tag>
        </Flex>
        <Descriptions column={{ xs: 1, sm: 2, lg: 3 }} style={{ marginTop: 22 }}>
          <Descriptions.Item label="이메일">{asText(identity.email)}</Descriptions.Item>
          <Descriptions.Item label="부서">{asText(pick(identity, 'department', 'department_name'))}</Descriptions.Item>
          <Descriptions.Item label="인증 방식">{asText(pick(identity, 'auth_source', 'identity_source'), view.user ? '중앙 계정' : 'Hub 계정')}</Descriptions.Item>
          <Descriptions.Item label="역할">{asText(pick(identity, 'roles', 'role'))}</Descriptions.Item>
          <Descriptions.Item label="마지막 로그인">{formatDate(identity.last_login_at)}</Descriptions.Item>
          <Descriptions.Item label="최근 활동">{formatDate(pick(identity, 'last_activity_at', 'last_activity'))}</Descriptions.Item>
        </Descriptions>
      </Card>
      <Row gutter={[16, 16]}>
        <Col xs={12} lg={6}><Card className="kpi-card"><Statistic title="실행 서버" value={view.currentServers.length} suffix="대" prefix={<CloudServerOutlined />} /></Card></Col>
        <Col xs={12} lg={6}><Card className="kpi-card"><Statistic title="CPU 할당·사용" value={cpu} precision={Math.abs(cpu % 1) > 0 ? 1 : 0} suffix="Core" prefix={<ExperimentOutlined />} /></Card></Col>
        <Col xs={12} lg={6}><Card className="kpi-card"><Statistic title="메모리" value={formatBytes(memory)} prefix={<DatabaseOutlined />} /></Card></Col>
        {gpuEnabled && <Col xs={12} lg={vram > 0 ? 3 : 6}><Card className="kpi-card"><Statistic title="GPU 할당" value={gpu} suffix="장" /></Card></Col>}
        {gpuEnabled && vram > 0 && <Col xs={12} lg={3}><Card className="kpi-card"><Statistic title="VRAM" value={formatBytes(vram)} /></Card></Col>}
      </Row>
    </Space>
  )
}

function HubsTab({ rows }: { rows: ApiRecord[] }) {
  const columns: TableColumnsType<ApiRecord> = [
    { title: 'Hub', key: 'hub', width: 180, render: (_value, row) => asText(pick(row, 'hub_name', 'name')) },
    { title: '망', key: 'network', width: 130, render: (_value, row) => asText(pick(row, 'network', 'network_name')) },
    { title: 'Hub 상태', key: 'hub_status', width: 110, render: (_value, row) => <Tag color={statusTone(row.hub_status)}>{stateLabel(row.hub_status)}</Tag> },
    { title: '버전', key: 'hub_version', width: 100, render: (_value, row) => asText(row.hub_version) },
    { title: '관리자', key: 'admin', width: 100, render: (_value, row) => <Tag>{Boolean(row.admin) ? '관리자' : '사용자'}</Tag> },
    { title: '상태', key: 'active', width: 100, render: (_value, row) => <Tag color={Boolean(row.active ?? true) ? 'success' : 'default'}>{Boolean(row.active ?? true) ? '활성' : '비활성'}</Tag> },
    { title: '최근 활동', key: 'last_activity', width: 180, render: (_value, row) => formatDate(pick(row, 'last_activity_at', 'last_activity')) },
    { title: '마지막 동기화', key: 'synced_at', width: 180, render: (_value, row) => formatDate(row.synced_at) },
  ]
  return rows.length ? <Table<ApiRecord> rowKey={(row) => asText(pick(row, 'hub_id', 'id', 'hub_name', 'name'))} columns={columns} dataSource={rows} pagination={false} scroll={{ x: 900 }} /> : <Empty description="연결된 Hub 계정이 없습니다." />
}

function ServersTab({ current, history, gpuEnabled }: { current: ApiRecord[]; history: ApiRecord[]; gpuEnabled: boolean }) {
  const columns: TableColumnsType<ApiRecord> = [
    { title: 'Hub', key: 'hub', width: 150, render: (_value, row) => asText(pick(row, 'hub_name', 'hub')) },
    { title: '서버', key: 'server', width: 150, render: (_value, row) => asText(pick(row, 'server_name', 'name'), '기본 서버') },
    { title: '상태', key: 'status', width: 110, render: (_value, row) => <Tag color={statusTone(row.status)}>{stateLabel(row.status)}</Tag> },
    { title: '실행시간', key: 'runtime', width: 130, render: (_value, row) => runtimeOf(row) },
    { title: 'CPU', key: 'cpu', width: 100, render: (_value, row) => hasValue(pick(row, 'cpu_cores', 'cpu')) ? `${asNumber(pick(row, 'cpu_cores', 'cpu')).toLocaleString('ko-KR')} Core` : '—' },
    { title: 'RAM', key: 'memory', width: 110, render: (_value, row) => hasValue(pick(row, 'memory_bytes', 'memory')) ? formatBytes(pick(row, 'memory_bytes', 'memory')) : '—' },
    ...(gpuEnabled ? [{ title: 'GPU', key: 'gpu', width: 90, render: (_value: unknown, row: ApiRecord) => hasValue(pick(row, 'gpu_count', 'gpus')) ? `${asNumber(pick(row, 'gpu_count', 'gpus'))}장` : '—' }] : []),
    { title: 'Node / Pod', key: 'pod', width: 220, render: (_value, row) => <>{asText(row.node_name)}<br /><Typography.Text type="secondary">{asText(row.pod_name)}</Typography.Text></> },
    { title: '이미지', key: 'image', width: 220, render: (_value, row) => asText(row.image) },
    { title: '최근 활동', key: 'last_activity', width: 180, render: (_value, row) => formatDate(row.last_activity_at) },
  ]
  const items = [
    { key: 'current', label: `현재 서버 ${current.length}`, children: current.length ? <Table<ApiRecord> rowKey={(row) => asText(pick(row, 'id', 'server_name'))} columns={columns} dataSource={current} pagination={false} scroll={{ x: 1300 }} /> : <Empty description="현재 실행 중인 서버가 없습니다." /> },
    { key: 'history', label: `서버 이력 ${history.length}`, children: history.length ? <Table<ApiRecord> rowKey={(row) => asText(pick(row, 'id', 'server_name'))} columns={columns} dataSource={history} pagination={{ pageSize: 20 }} scroll={{ x: 1300 }} /> : <Empty description="저장된 서버 이력이 없습니다." /> },
  ]
  return <Tabs items={items} />
}

function UsageTab({ usage, gpuEnabled }: { usage: Record<UsagePeriod, ApiRecord>; gpuEnabled: boolean }) {
  const [params, setParams] = useSearchParams()
  const requested = params.get('period') as UsagePeriod | null
  const period: UsagePeriod = requested && requested in periodLabels ? requested : 'day'
  const selected = periodSummary(usage[period])
  const runtime = pick(selected, 'runtime_seconds', 'server_running_seconds')
  const memory = pick(selected, 'memory_average', 'memory_bytes_average', 'memory_bytes')
  const comparison: ApiRecord[] = (Object.keys(periodLabels) as UsagePeriod[]).map((key) => ({ period: key, ...periodSummary(usage[key]) }))
  const chartOption: EChartsOption = {
    tooltip: { trigger: 'axis' },
    grid: { left: 48, right: 18, top: 24, bottom: 48 },
    xAxis: { type: 'category', data: comparison.map((row) => `${periodLabels[row.period as UsagePeriod]}간`) },
    yAxis: { type: 'value', name: '시간' },
    series: [{ name: '서버 실행시간', type: 'bar', data: comparison.map((row) => asNumber(pick(row, 'runtime_seconds', 'server_running_seconds')) / 3600), itemStyle: { color: '#2563eb', borderRadius: [6, 6, 0, 0] } }],
  }
  const columns: TableColumnsType<ApiRecord> = [
    { title: '기간', dataIndex: 'period', key: 'period', render: (value) => periodLabels[value as UsagePeriod] },
    { title: '로그인', key: 'logins', render: (_value, row) => `${asNumber(pick(row, 'login_count', 'logins')).toLocaleString('ko-KR')}회` },
    { title: '서버 시작', key: 'starts', render: (_value, row) => `${asNumber(pick(row, 'server_start_count', 'server_starts', 'servers')).toLocaleString('ko-KR')}회` },
    { title: '실행시간', key: 'runtime', render: (_value, row) => formatDuration(pick(row, 'runtime_seconds', 'server_running_seconds')) },
    { title: 'CPU 평균', key: 'cpu', render: (_value, row) => hasValue(row.cpu_average) ? `${asNumber(row.cpu_average).toLocaleString('ko-KR', { maximumFractionDigits: 2 })} Core` : '수집 불가' },
    { title: 'RAM 평균', key: 'memory', render: (_value, row) => hasValue(pick(row, 'memory_average', 'memory_bytes_average')) ? formatBytes(pick(row, 'memory_average', 'memory_bytes_average')) : '수집 불가' },
    ...(gpuEnabled ? [{ title: 'GPU 평균', key: 'gpu', render: (_value: unknown, row: ApiRecord) => hasValue(row.gpu_average) ? `${asNumber(row.gpu_average).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}%` : '수집 불가' }] : []),
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Segmented<UsagePeriod> value={period} options={(Object.keys(periodLabels) as UsagePeriod[]).map((value) => ({ value, label: `${periodLabels[value]}간` }))} onChange={(value) => setParams((current) => { const next = new URLSearchParams(current); next.set('period', value); next.set('tab', 'usage'); return next })} />
      <Row gutter={[16, 16]}>
        <Col xs={12} lg={6}><Card><Statistic title="로그인" value={asNumber(pick(selected, 'login_count', 'logins'))} suffix="회" /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="서버 실행시간" value={hasValue(runtime) ? formatDuration(runtime) : '수집 불가'} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="CPU 평균" value={hasValue(selected.cpu_average) ? asNumber(selected.cpu_average) : '수집 불가'} suffix={hasValue(selected.cpu_average) ? 'Core' : undefined} precision={hasValue(selected.cpu_average) ? 2 : undefined} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="RAM 평균" value={hasValue(memory) ? formatBytes(memory) : '수집 불가'} /></Card></Col>
      </Row>
      <ChartCard title="기간별 서버 실행시간 비교" subtitle="보존된 메타데이터 기준" option={chartOption} empty={comparison.every((row) => !asNumber(row.runtime_seconds))} />
      <Card title="일·주·월 비교"><Table<ApiRecord> rowKey={(row) => asText(row.period)} columns={columns} dataSource={comparison} pagination={false} scroll={{ x: 800 }} /></Card>
    </Space>
  )
}

function LlmTab({ llm, dataPolicy }: { llm: ApiRecord; dataPolicy: ApiRecord }) {
  const included = llm.included !== false
  const summary = periodSummary(llm)
  const rows = records(llm.data || llm.top_callers || llm.breakdown)
  const columns: TableColumnsType<ApiRecord> = [
    { title: 'Hub', key: 'hub', render: (_value, row) => asText(pick(row, 'hub_name', 'hub')) },
    { title: 'Pod', dataIndex: 'pod_name', key: 'pod', width: 220 },
    { title: '모델', dataIndex: 'model', key: 'model', width: 170 },
    { title: '호출', key: 'calls', render: (_value, row) => asNumber(pick(row, 'calls', 'requests')).toLocaleString('ko-KR') },
    { title: '성공률', key: 'success', render: (_value, row) => hasValue(row.success_rate) ? `${normalizePercentValue(row.success_rate).toLocaleString('ko-KR', { maximumFractionDigits: 1 })}%` : '수집 불가' },
    { title: 'P95', key: 'p95', render: (_value, row) => hasValue(row.latency_p95_ms) ? `${asNumber(row.latency_p95_ms).toLocaleString('ko-KR')}ms` : '수집 불가' },
    { title: 'Input / Output', key: 'tokens', render: (_value, row) => hasValue(row.input_tokens) || hasValue(row.output_tokens) ? `${asNumber(row.input_tokens).toLocaleString('ko-KR')} / ${asNumber(row.output_tokens).toLocaleString('ko-KR')}` : '수집 불가' },
    { title: '추정 비용', key: 'cost', render: (_value, row) => hasValue(row.estimated_cost) ? asNumber(row.estimated_cost).toLocaleString('ko-KR', { maximumFractionDigits: 4 }) : '수집 불가' },
  ]
  if (!included) return <Empty description="이 요청에는 LLM 사용량이 포함되지 않았습니다." />
  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Alert type="info" showIcon message="본문을 수집하지 않습니다" description={Boolean(dataPolicy.metadata_only) ? '프롬프트·응답·Notebook 코드는 저장하지 않고 호출 메타데이터만 표시합니다.' : '운영 정책에 따라 수집 범위를 확인해 주세요.'} />
      <Row gutter={[16, 16]}>
        <Col xs={12} lg={6}><Card><Statistic title="호출" value={asNumber(pick(summary, 'calls', 'requests', 'request_count'))} suffix="회" /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="성공률" value={hasValue(summary.success_rate) ? normalizePercentValue(summary.success_rate) : '수집 불가'} suffix={hasValue(summary.success_rate) ? '%' : undefined} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="P95 지연" value={hasValue(summary.latency_p95_ms) ? asNumber(summary.latency_p95_ms) : '수집 불가'} suffix={hasValue(summary.latency_p95_ms) ? 'ms' : undefined} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="전체 토큰" value={hasValue(summary.total_tokens) ? asNumber(summary.total_tokens) : '수집 불가'} /></Card></Col>
      </Row>
      {rows.length ? <Table<ApiRecord> rowKey={(row) => `${asText(row.pod_name)}-${asText(row.model)}`} columns={columns} dataSource={rows} pagination={{ pageSize: 20 }} scroll={{ x: 1100 }} /> : <Empty description="수집된 LLM 호출 메타데이터가 없습니다." />}
    </Space>
  )
}

function TimelineTab({ rows }: { rows: ApiRecord[] }) {
  if (!rows.length) return <Empty description="저장된 사용자 활동 이력이 없습니다." />
  return <Timeline items={rows.map((row) => ({
    color: statusTone(pick(row, 'result', 'status')) === 'error' ? 'red' : statusTone(pick(row, 'result', 'status')) === 'warning' ? 'orange' : 'blue',
    children: <Card size="small"><Flex justify="space-between" gap={12} wrap><strong>{actionLabels[asText(pick(row, 'action', 'event_type'), '')] || asText(pick(row, 'title', 'action', 'event_type'), '사용자 활동')}</strong><Typography.Text type="secondary">{formatDate(pick(row, 'created_at', 'timestamp', 'occurred_at'))}</Typography.Text></Flex><Typography.Paragraph type="secondary" style={{ margin: '8px 0 0' }}>{asText(pick(row, 'description', 'detail', 'reason'), `${asText(row.hub_name, '')}${row.hub_name ? ' · ' : ''}${stateLabel(row.result)}`)}</Typography.Paragraph></Card>,
  }))} />
}

export function UserDetailPage() {
  const { username = '' } = useParams()
  const decodedUsername = (() => { try { return decodeURIComponent(username) } catch { return username } })()
  const { features } = useAuth()
  const [params, setParams] = useSearchParams()
  const { data, loading, error, reload } = useApi<ApiRecord>(`/users/${encodeURIComponent(decodedUsername)}?page=1&limit=50&include_llm=true`)
  const view = useMemo(() => normalizeUserDetail(data), [data])
  const gpuEnabled = Boolean(view.featureEnabled.gpu_monitoring ?? features.gpuMonitoring)
  const llmEnabled = Boolean(view.featureEnabled.llm_usage_monitoring ?? features.llmUsageMonitoring)
  const tab = params.get('tab') || 'overview'
  const items = [
    { key: 'overview', label: '기본정보·자원', icon: <UserOutlined />, children: <OverviewTab view={view} gpuEnabled={gpuEnabled} /> },
    { key: 'hubs', label: `Hub ${view.hubs.length}`, children: <HubsTab rows={view.hubs} /> },
    { key: 'servers', label: `서버 ${view.currentServers.length}`, icon: <CloudServerOutlined />, children: <ServersTab current={view.currentServers} history={view.serverHistory} gpuEnabled={gpuEnabled} /> },
    { key: 'usage', label: '일·주·월 사용', icon: <ExperimentOutlined />, children: <UsageTab usage={view.usage} gpuEnabled={gpuEnabled} /> },
    ...(llmEnabled ? [{ key: 'llm', label: 'LLM 사용량', children: <LlmTab llm={view.llmUsage} dataPolicy={view.dataPolicy} /> }] : []),
    { key: 'timeline', label: `Timeline ${view.timeline.length}`, icon: <ClockCircleOutlined />, children: <TimelineTab rows={view.timeline} /> },
  ]
  const activeTab = items.some((item) => item.key === tab) ? tab : 'overview'

  return (
    <>
      <PageHeader title={view.username || decodedUsername || '사용자 상세'} description="Hub 계정, 서버, 자원, 이용 통계와 활동 이력을 한 화면에서 확인합니다." onRefresh={reload} extra={<Button icon={<ArrowLeftOutlined />}><Link to="/users">사용자 목록</Link></Button>} />
      <AsyncState loading={loading} error={error} onRetry={reload} empty={!loading && !error && !data} emptyDescription="사용자 상세 정보가 없습니다.">
        <Tabs activeKey={activeTab} items={items} onChange={(nextTab) => setParams((current) => { const next = new URLSearchParams(current); next.set('tab', nextTab); return next })} />
      </AsyncState>
    </>
  )
}
