import {
  ClockCircleOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  FireOutlined,
  TeamOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { Alert, Card, Col, Flex, Progress, Row, Segmented, Select, Space, Statistic, Table, Tag, Typography, type TableColumnsType } from 'antd'
import type { EChartsOption } from 'echarts'
import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { AsyncState } from '../components/AsyncState'
import { ChartCard } from '../components/ChartCard'
import { PageHeader } from '../components/PageHeader'
import { useLiveDashboard, type DashboardFilters } from '../hooks/useLiveDashboard'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatBytes, formatDate, formatDuration, formatPercent, pick, statusTone } from '../utils/format'
import { filterLiveUsers, formatCpuResource, formatMemoryResource, summarizeLiveUsers } from '../utils/dashboard'

function records(value: unknown): ApiRecord[] {
  return Array.isArray(value) ? value.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object') : []
}

function nestedRecord(value: unknown): ApiRecord {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as ApiRecord : {}
}

function metric(record: ApiRecord, ...keys: string[]) {
  return pick(record, ...keys)
}

function uniqueOptions(items: ApiRecord[], keys: string[]) {
  const values = items.map((item) => pick(item, ...keys)).filter((value) => value !== undefined).map(String)
  return [...new Set(values)].map((value) => ({ label: value, value }))
}

function chartText(value: unknown) { return asText(value, '') }
function statisticValue(value: unknown): string | number | undefined {
  if (value === null || value === undefined) return undefined
  return typeof value === 'string' || typeof value === 'number' ? value : asText(value)
}

export function DashboardPage() {
  const { features } = useAuth()
  const navigate = useNavigate()
  const [filters, setFilters] = useState<DashboardFilters>({ range: 'day' })
  const { data, loading, error, connection, lastUpdated, stale, transportStale, sourceStale, reload } = useLiveDashboard(filters)

  const rawSummary = nestedRecord(data?.summary || data?.metrics || data)
  const usageStats = nestedRecord(data?.usage_stats)
  const liveUsers = records(data?.live_users || data?.sessions || (Array.isArray(data?.active_users) ? data?.active_users : []))
  const hubs = records(data?.hubs)
  const projects = records(data?.projects)
  const trend = records(data?.usage_trend || data?.trend)
  const topUsers = records(data?.top_users)
  const gpuWaste = records(data?.gpu_waste || data?.waste_users)
  const providedFilters = nestedRecord(data?.filters)
  const aggregates = nestedRecord(data?.aggregates)
  const aggregateAll = nestedRecord(aggregates.all)
  const dimensionFilterActive = Boolean(filters.network || filters.hub || filters.department || filters.project)
  const displayedLiveUsers = useMemo(() => filterLiveUsers(liveUsers, filters), [data, filters.network, filters.hub, filters.department, filters.project]) // eslint-disable-line react-hooks/exhaustive-deps
  const localSummary: ApiRecord = dimensionFilterActive ? summarizeLiveUsers(displayedLiveUsers) : {}
  const summary = dimensionFilterActive ? { ...rawSummary, ...localSummary } : rawSummary

  const options = useMemo(() => ({
    network: records(providedFilters.networks).length
      ? records(providedFilters.networks).map((item) => ({ label: asText(pick(item, 'label', 'name')), value: asText(pick(item, 'value', 'id', 'name')) }))
      : uniqueOptions([...hubs, ...liveUsers], ['network', 'network_name']),
    hub: uniqueOptions([...hubs, ...liveUsers], ['hub_name', 'hub', 'name']),
    department: uniqueOptions(liveUsers, ['department', 'department_name']),
    project: uniqueOptions([...projects, ...liveUsers], ['project_name', 'project', 'name']),
  }), [data]) // eslint-disable-line react-hooks/exhaustive-deps

  const cpuPercent = metric(summary, 'cpu_utilization', 'cpu_percent')
  const cpuCores = metric(summary, 'cpu_cores', 'cpu_usage') ?? aggregateAll.cpu_cores
  const memoryPercent = metric(summary, 'memory_utilization', 'memory_percent')
  const memoryBytes = metric(summary, 'memory_bytes', 'memory_usage') ?? aggregateAll.memory_bytes
  const gpuPercent = metric(summary, 'gpu_utilization', 'gpu_percent')
  const gpuCount = metric(summary, 'gpu_count', 'gpu_usage') ?? aggregateAll.gpu_count
  const vramPercent = metric(summary, 'vram_utilization', 'vram_percent')
  const vramBytes = metric(summary, 'vram_bytes', 'vram_usage') ?? aggregateAll.vram_bytes
  const kpis = [
    { key: 'users', title: '현재 접속자', value: metric(summary, 'active_users', 'online_users', 'current_users'), suffix: '명', icon: <TeamOutlined />, tone: 'blue' },
    { key: 'servers', title: '실행 서버', value: metric(summary, 'running_servers', 'active_servers', 'servers'), suffix: '대', icon: <CloudServerOutlined />, tone: 'cyan' },
    { key: 'cpu', title: cpuPercent !== undefined ? 'CPU 사용률' : 'CPU 사용량', value: cpuPercent ?? cpuCores, suffix: cpuPercent !== undefined ? '%' : ' Core', icon: <DashboardOutlined />, tone: 'geekblue', progress: cpuPercent !== undefined },
    { key: 'memory', title: memoryPercent !== undefined ? 'RAM 사용률' : 'RAM 사용량', value: memoryPercent !== undefined ? asNumber(memoryPercent) : memoryBytes !== undefined ? formatBytes(memoryBytes) : undefined, suffix: memoryPercent !== undefined ? '%' : undefined, icon: <DatabaseOutlined />, tone: 'purple', progress: memoryPercent !== undefined },
    ...(features.gpuMonitoring ? [
      { key: 'gpu', title: gpuPercent !== undefined ? 'GPU 사용률' : '할당 GPU', value: gpuPercent ?? gpuCount, suffix: gpuPercent !== undefined ? '%' : '장', icon: <ExperimentOutlined />, tone: 'volcano', progress: gpuPercent !== undefined },
      { key: 'vram', title: vramPercent !== undefined ? 'VRAM 사용률' : 'VRAM 사용량', value: vramPercent !== undefined ? asNumber(vramPercent) : vramBytes !== undefined ? formatBytes(vramBytes) : undefined, suffix: vramPercent !== undefined ? '%' : undefined, icon: <DatabaseOutlined />, tone: 'magenta', progress: vramPercent !== undefined },
    ] : []),
    { key: 'idle', title: 'Idle 세션', value: metric(summary, 'idle_sessions', 'idle_servers'), suffix: '건', icon: <ClockCircleOutlined />, tone: 'gold' },
    { key: 'long', title: '장시간 세션', value: metric(summary, 'long_running_sessions', 'long_sessions'), suffix: '건', icon: <WarningOutlined />, tone: 'red' },
    { key: 'dau', title: '전체 일간 이용자', value: metric(usageStats, 'dau'), suffix: '명', icon: <TeamOutlined />, tone: 'blue' },
    { key: 'wau', title: '전체 주간 이용자', value: metric(usageStats, 'wau'), suffix: '명', icon: <TeamOutlined />, tone: 'cyan' },
    { key: 'mau', title: '전체 월간 이용자', value: metric(usageStats, 'mau'), suffix: '명', icon: <TeamOutlined />, tone: 'purple' },
  ]

  const userColumns: TableColumnsType<ApiRecord> = [
    { title: '사용자', key: 'user', fixed: 'left', width: 150, render: (_value, row) => <button className="text-link" onClick={() => navigate(`/users/${encodeURIComponent(asText(pick(row, 'username', 'user_name', 'name'), ''))}`)}>{asText(pick(row, 'display_name', 'name', 'username', 'user_name'))}</button> },
    { title: '망 / Hub', key: 'hub', width: 180, render: (_value, row) => <Space direction="vertical" size={0}><span>{asText(pick(row, 'network', 'network_name'))}</span><Typography.Text type="secondary">{asText(pick(row, 'hub_name', 'hub'))}</Typography.Text></Space> },
    { title: '부서 / 프로젝트', key: 'org', width: 180, render: (_value, row) => <Space direction="vertical" size={0}><span>{asText(pick(row, 'department', 'department_name'))}</span><Typography.Text type="secondary">{asText(pick(row, 'project_name', 'project'))}</Typography.Text></Space> },
    { title: '실행시간', key: 'runtime', width: 120, render: (_value, row) => row.runtime_seconds !== undefined ? formatDuration(row.runtime_seconds) : asText(pick(row, 'runtime', 'running_time', 'server_runtime')) },
    { title: 'CPU', key: 'cpu', width: 110, render: (_value, row) => formatCpuResource(row) },
    { title: 'RAM', key: 'memory', width: 120, render: (_value, row) => formatMemoryResource(row) },
    ...(features.gpuMonitoring ? [
      { title: 'GPU', key: 'gpu', width: 110, render: (_value: unknown, row: ApiRecord) => pick(row, 'gpu_percent', 'gpu_utilization') !== undefined ? formatPercent(pick(row, 'gpu_percent', 'gpu_utilization')) : `${asNumber(pick(row, 'gpu_count', 'gpu'))}장` },
      { title: 'VRAM', key: 'vram', width: 120, render: (_value: unknown, row: ApiRecord) => pick(row, 'vram_percent', 'vram_utilization') !== undefined ? formatPercent(pick(row, 'vram_percent', 'vram_utilization')) : formatBytes(pick(row, 'vram_bytes', 'vram')) },
    ] : []),
    { title: '상태', key: 'status', width: 100, render: (_value, row) => <Tag color={statusTone(pick(row, 'status', 'server_status'))}>{asText(pick(row, 'status', 'server_status'))}</Tag> },
  ]

  const trendOption: EChartsOption = {
    tooltip: { trigger: 'axis' },
    legend: { bottom: 0, data: ['로그인', '서버 시작', 'CPU Core'] },
    grid: { left: 48, right: 24, top: 24, bottom: 48 },
    xAxis: { type: 'category', data: trend.map((row) => chartText(pick(row, 'time', 'timestamp', 'date', 'label'))), boundaryGap: false },
    yAxis: { type: 'value' },
    series: [
      { name: '로그인', type: 'line', smooth: true, showSymbol: false, areaStyle: { opacity: 0.08 }, data: trend.map((row) => asNumber(pick(row, 'login_count', 'active_users', 'users'))) },
      { name: '서버 시작', type: 'line', smooth: true, showSymbol: false, data: trend.map((row) => asNumber(pick(row, 'server_starts', 'running_servers', 'servers'))) },
      { name: 'CPU Core', type: 'line', smooth: true, showSymbol: false, data: trend.map((row) => asNumber(pick(row, 'cpu_cores', 'cpu_usage', 'cpu'))) },
    ],
  }
  const topOption: EChartsOption = {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { left: 100, right: 28, top: 16, bottom: 28 },
    xAxis: { type: 'value' },
    yAxis: { type: 'category', data: topUsers.map((row) => chartText(pick(row, 'username', 'user', 'name'))).reverse() },
    series: [{ type: 'bar', name: '사용시간', data: topUsers.map((row) => row.runtime_seconds !== undefined ? asNumber(row.runtime_seconds) / 3600 : asNumber(pick(row, 'usage_hours', 'server_hours', 'hours'))).reverse(), itemStyle: { color: '#2563eb', borderRadius: [0, 6, 6, 0] } }],
  }
  const wasteOption: EChartsOption = {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { left: 100, right: 28, top: 16, bottom: 28 },
    xAxis: { type: 'value', max: 100, axisLabel: { formatter: '{value}%' } },
    yAxis: { type: 'category', data: gpuWaste.map((row) => chartText(pick(row, 'username', 'user', 'name'))).reverse() },
    series: [{ type: 'bar', name: '낭비 점수', data: gpuWaste.map((row) => pick(row, 'waste_score', 'idle_ratio') !== undefined ? asNumber(pick(row, 'waste_score', 'idle_ratio')) : Math.max(0, 100 - asNumber(row.gpu_utilization))).reverse(), itemStyle: { color: '#f97316', borderRadius: [0, 6, 6, 0] } }],
  }

  const updatedText = lastUpdated ? formatDate(lastUpdated.toISOString()) : '아직 갱신되지 않음'
  return (
    <>
      <PageHeader title="통합 대시보드" description="현재 운영 상태와 이용 추세를 실시간으로 확인합니다." onRefresh={reload} extra={
        <Space wrap>
          <Tag color={connection === '실시간' ? 'success' : connection === '주기 조회' ? 'processing' : 'warning'}>{connection}</Tag>
          <Typography.Text type="secondary">마지막 갱신 {updatedText}</Typography.Text>
        </Space>
      } />
      {stale && <Alert className="dashboard-alert" type="warning" showIcon message="표시 중인 데이터가 최신 상태가 아닙니다" description={transportStale ? '실시간 연결 또는 주기 조회가 45초 이상 갱신되지 않았습니다.' : sourceStale ? '화면 연결은 정상이지만 하나 이상의 수집 원본이 오래되었거나 연결되지 않았습니다. Hub별 마지막 통신 시각을 확인해 주세요.' : '망 연결과 수집기 상태를 확인해 주세요.'} />}
      <Card className="dashboard-filters" size="small">
        <Flex gap={10} wrap align="center">
          <Segmented aria-label="통계 기간" value={filters.range} options={[{ label: '일', value: 'day' }, { label: '주', value: 'week' }, { label: '월', value: 'month' }]} onChange={(range) => setFilters((current) => ({ ...current, range: range as DashboardFilters['range'] }))} />
          <Select allowClear placeholder="전체 망" aria-label="망 필터" options={options.network} value={filters.network} onChange={(network) => setFilters((current) => ({ ...current, network }))} />
          <Select allowClear showSearch optionFilterProp="label" placeholder="전체 Hub" aria-label="Hub 필터" options={options.hub} value={filters.hub} onChange={(hub) => setFilters((current) => ({ ...current, hub }))} />
          <Select allowClear showSearch optionFilterProp="label" placeholder="전체 부서" aria-label="부서 필터" options={options.department} value={filters.department} onChange={(department) => setFilters((current) => ({ ...current, department }))} />
          <Select allowClear showSearch optionFilterProp="label" placeholder="전체 프로젝트" aria-label="프로젝트 필터" options={options.project} value={filters.project} onChange={(project) => setFilters((current) => ({ ...current, project }))} />
        </Flex>
      </Card>
      <AsyncState loading={loading && !data} error={error && !data ? error : null} onRetry={reload} empty={!loading && !error && !data} emptyDescription="수집된 대시보드 데이터가 없습니다.">
        <Row gutter={[16, 16]} className="kpi-grid">
          {kpis.map((item) => (
            <Col xs={24} sm={12} xl={6} xxl={3} key={item.key}>
              <Card className="kpi-card">
                <Flex align="center" justify="space-between">
                  <Statistic title={item.title} value={statisticValue(item.value) ?? '—'} suffix={item.value === undefined ? undefined : item.suffix} />
                  <span className={`kpi-icon kpi-${item.tone}`}>{item.icon}</span>
                </Flex>
                {item.progress && item.value !== undefined && <Progress percent={Math.min(100, asNumber(item.value))} showInfo={false} size="small" strokeColor={asNumber(item.value) >= 85 ? '#dc2626' : '#2563eb'} />}
              </Card>
            </Col>
          ))}
        </Row>
        {hubs.length > 0 && <section className="hub-strip" aria-label="Hub 상태"><Row gutter={[12, 12]}>{hubs.map((hub) => <Col xs={24} sm={12} xl={6} key={asText(pick(hub, 'id', 'name'))}><Card size="small" hoverable onClick={() => navigate(`/hubs?search=${encodeURIComponent(asText(pick(hub, 'name'), ''))}`)}><Flex justify="space-between"><strong>{asText(pick(hub, 'name'))}</strong><Tag color={statusTone(pick(hub, 'status'))}>{asText(pick(hub, 'status'))}</Tag></Flex><Typography.Text type="secondary">접속 {asText(pick(hub, 'active_users', 'users'), '0')}명 · 서버 {asText(pick(hub, 'running_servers', 'servers'), '0')}대</Typography.Text></Card></Col>)}</Row></section>}
        <section className="dashboard-section">
          <Flex justify="space-between" align="end" wrap gap={8}><div><Typography.Title level={3}>실시간 사용자 현황</Typography.Title><Typography.Paragraph type="secondary">누가 어느 환경에서 얼마나 자원을 사용 중인지 확인합니다.</Typography.Paragraph></div><Tag>{displayedLiveUsers.length}명</Tag></Flex>
          {displayedLiveUsers.length ? <Table<ApiRecord> size="middle" rowKey={(row) => asText(pick(row, 'id', 'username', 'name'))} columns={userColumns} dataSource={displayedLiveUsers} pagination={{ pageSize: 10 }} scroll={{ x: 1050 }} /> : <Alert type="info" showIcon message={dimensionFilterActive ? '선택한 조건에 해당하는 실시간 사용자가 없습니다.' : '현재 수집된 실시간 사용자 정보가 없습니다.'} />}
        </section>
        <div className="chart-grid">
          <ChartCard title="이용 추세" subtitle="로그인·서버 시작·CPU Core 변화" option={trendOption} empty={!trend.length} />
          <ChartCard title="Top 사용자" subtitle="사용시간 기준 · 막대를 누르면 상세 조회" option={topOption} empty={!topUsers.length} onEvents={{ click: (params) => params.name && navigate(`/users/${encodeURIComponent(params.name)}`) }} />
          {features.gpuMonitoring && <ChartCard title="GPU 낭비 후보" subtitle="할당 대비 실사용이 낮은 사용자" option={wasteOption} empty={!gpuWaste.length} onEvents={{ click: (params) => params.name && navigate(`/users/${encodeURIComponent(params.name)}`) }} extra={<FireOutlined className="warning-icon" />} />}
        </div>
      </AsyncState>
    </>
  )
}
