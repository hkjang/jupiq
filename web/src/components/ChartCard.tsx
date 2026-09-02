import { Empty, Typography } from 'antd'
import type { EChartsOption } from 'echarts'
import ReactECharts from 'echarts-for-react'
import type { ReactNode } from 'react'

export function ChartCard({ title, subtitle, option, empty, onEvents, extra }: {
  title: string
  subtitle?: string
  option: EChartsOption
  empty?: boolean
  onEvents?: Record<string, (params: { name?: string; data?: unknown }) => void>
  extra?: ReactNode
}) {
  return (
    <section className="chart-card">
      <div className="chart-card-heading">
        <div><Typography.Title level={4}>{title}</Typography.Title>{subtitle && <Typography.Text type="secondary">{subtitle}</Typography.Text>}</div>
        {extra}
      </div>
      {empty ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="수집된 통계가 없습니다." /> : <ReactECharts option={option} style={{ height: 300 }} notMerge lazyUpdate onEvents={onEvents} />}
    </section>
  )
}
