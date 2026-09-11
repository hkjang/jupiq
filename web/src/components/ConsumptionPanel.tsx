import { Alert, Card, Flex, Segmented, Space, Statistic, Table, Tag, Typography, type TableColumnsType } from 'antd'
import { useMemo, useState } from 'react'
import { AsyncState } from './AsyncState'
import { useApi } from '../hooks/useApi'
import type { ApiRecord } from '../types'
import { asNumber, asText, formatCoreHours, formatDate, formatDuration, formatGbHours, formatObservedRatio } from '../utils/format'
import { stableRowKey } from '../utils/rowKey'
import { sorterFor } from '../utils/sorting'

type GroupBy = 'user' | 'hub' | 'network' | 'department'

const groupLabels: Record<GroupBy, string> = { user: '사용자', hub: 'Hub', network: '망', department: '부서' }
const rangeDays: Record<string, number> = { '7일': 7, '30일': 30, '90일': 90 }

function records(value: unknown): ApiRecord[] {
  return Array.isArray(value) ? value.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object') : []
}

// Measured consumption, as opposed to the cost figures an administrator
// registers by hand. These are integrals of what the JupyterHub pods actually
// held over time, so they answer how much a user consumed rather than how busy
// their pods were while running.
export function ConsumptionPanel({ gpuEnabled }: { gpuEnabled: boolean }) {
  const [groupBy, setGroupBy] = useState<GroupBy>('user')
  const [range, setRange] = useState('30일')
  const path = useMemo(() => {
    const to = new Date()
    const from = new Date(to.getTime() - (rangeDays[range] ?? 30) * 24 * 60 * 60 * 1000)
    return `/usage/consumption?group_by=${groupBy}&from=${from.toISOString()}&to=${to.toISOString()}&limit=200`
  }, [groupBy, range])
  const { data, loading, refreshing, error, reload } = useApi<ApiRecord>(path)

  const rows = records(data?.data)
  const totals = rows.reduce<{ cpu: number; memory: number; runtime: number }>(
    (sum, row) => ({
      cpu: sum.cpu + asNumber(row.cpu_core_hours),
      memory: sum.memory + asNumber(row.memory_gb_hours),
      runtime: sum.runtime + asNumber(row.runtime_seconds),
    }),
    { cpu: 0, memory: 0, runtime: 0 },
  )
  // A total built mostly from unobserved runtime understates real consumption,
  // so the weakest coverage in the table is surfaced rather than buried.
  const lowestObserved = rows.reduce((lowest, row) => Math.min(lowest, asNumber(row.observed_ratio)), rows.length ? 1 : 1)

  const columns: TableColumnsType<ApiRecord> = [
    { title: groupLabels[groupBy], key: 'group', width: 200, sorter: sorterFor(['group']), showSorterTooltip: false, render: (_value, row) => asText(row.group) },
    { title: 'CPU 사용량', key: 'cpu', width: 150, defaultSortOrder: 'descend', sorter: sorterFor(['cpu_core_hours'], 'number'), showSorterTooltip: false, render: (_value, row) => formatCoreHours(row.cpu_core_hours) },
    { title: '메모리 사용량', key: 'memory', width: 150, sorter: sorterFor(['memory_gb_hours'], 'number'), showSorterTooltip: false, render: (_value, row) => formatGbHours(row.memory_gb_hours) },
    ...(gpuEnabled ? [{ title: 'GPU 사용량', key: 'gpu', width: 140, sorter: sorterFor(['gpu_hours'], 'number'), showSorterTooltip: false, render: (_value: unknown, row: ApiRecord) => `${asNumber(row.gpu_hours).toLocaleString('ko-KR', { maximumFractionDigits: 2 })} GPU·h` }] : []),
    { title: '실행시간', key: 'runtime', width: 130, sorter: sorterFor(['runtime_seconds'], 'number'), showSorterTooltip: false, render: (_value, row) => formatDuration(row.runtime_seconds) },
    { title: 'CPU 최대', key: 'cpu_peak', width: 120, sorter: sorterFor(['cpu_peak_cores'], 'number'), showSorterTooltip: false, render: (_value, row) => `${asNumber(row.cpu_peak_cores).toLocaleString('ko-KR', { maximumFractionDigits: 2 })} Core` },
    {
      title: '관측 비율', key: 'observed', width: 120, sorter: sorterFor(['observed_ratio'], 'number'), showSorterTooltip: false,
      render: (_value, row) => {
        const ratio = asNumber(row.observed_ratio)
        return <Tag color={ratio >= 0.9 ? 'success' : ratio >= 0.5 ? 'warning' : 'error'}>{formatObservedRatio(ratio)}</Tag>
      },
    },
  ]

  return (
    <Card
      title="측정된 자원 사용량"
      extra={
        <Space wrap>
          <Segmented value={range} options={Object.keys(rangeDays)} onChange={(value) => setRange(String(value))} />
          <Segmented value={groupBy} options={(Object.keys(groupLabels) as GroupBy[]).map((value) => ({ value, label: groupLabels[value] }))} onChange={(value) => setGroupBy(value as GroupBy)} />
        </Space>
      }
    >
      <Alert
        className="data-note"
        type="info"
        showIcon
        message="점유한 자원을 시간으로 적분한 실측값입니다"
        description="1 Core를 한 시간 점유하면 1 Core·h입니다. 평균 사용률은 실행 중 얼마나 바빴는지만 말해 주어 오래 점유한 사용자와 잠깐 쓴 사용자를 구분하지 못합니다. 원시 표본이 보존 기간으로 삭제된 뒤에도 이 집계는 남습니다."
      />
      {lowestObserved < 0.9 && rows.length > 0 && (
        <Alert
          className="data-note"
          type="warning"
          showIcon
          message="일부 구간은 수집이 끊겨 실제보다 적게 집계됐습니다"
          description={`가장 낮은 관측 비율이 ${formatObservedRatio(lowestObserved)}입니다. 수집기나 Prometheus가 중단된 동안의 사용량은 집계에 반영되지 않으며, 사용량이 적었던 것과는 다릅니다.`}
        />
      )}
      <AsyncState loading={loading && !data} refreshing={refreshing} error={error} onRetry={reload} empty={!loading && !error && rows.length === 0} emptyDescription="집계된 사용량이 없습니다. 수집이 한 시간 이상 누적되면 표시됩니다.">
        <Flex gap={16} wrap style={{ marginBottom: 16 }}>
          <Card size="small"><Statistic title="합계 CPU" value={formatCoreHours(totals.cpu)} /></Card>
          <Card size="small"><Statistic title="합계 메모리" value={formatGbHours(totals.memory)} /></Card>
          <Card size="small"><Statistic title="합계 실행시간" value={formatDuration(totals.runtime)} /></Card>
        </Flex>
        <Table<ApiRecord> rowKey={(row) => stableRowKey(row, row.group)} columns={columns} dataSource={rows} pagination={{ pageSize: 20, showSizeChanger: true }} scroll={{ x: 960 }} />
        {Boolean(data?.rolled_up_through) && (
          <Typography.Text type="secondary">집계 완료 시각 {formatDate(data?.rolled_up_through)} · 진행 중인 시간대는 아직 포함되지 않습니다.</Typography.Text>
        )}
      </AsyncState>
    </Card>
  )
}
