import { Breadcrumb, Button, Flex, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { ReactNode } from 'react'

interface PageHeaderProps {
  title: string
  description: string
  extra?: ReactNode
  onRefresh?: () => void
}

export function PageHeader({ title, description, extra, onRefresh }: PageHeaderProps) {
  return (
    <header className="page-heading">
      <Breadcrumb items={[{ title: 'jupiq' }, { title }]} aria-label="현재 위치" />
      <Flex align="flex-start" justify="space-between" gap={16} wrap>
        <div>
          <Typography.Title level={2}>{title}</Typography.Title>
          <Typography.Paragraph type="secondary">{description}</Typography.Paragraph>
        </div>
        <Flex gap={8} wrap>
          {onRefresh && <Button icon={<ReloadOutlined />} onClick={onRefresh}>새로고침</Button>}
          {extra}
        </Flex>
      </Flex>
    </header>
  )
}
