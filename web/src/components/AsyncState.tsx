import { Alert, Button, Empty, Skeleton } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { ReactNode } from 'react'

interface AsyncStateProps {
  loading: boolean
  error?: Error | null
  empty?: boolean
  onRetry?: () => void
  emptyDescription?: string
  children: ReactNode
}

export function AsyncState({ loading, error, empty, onRetry, emptyDescription = '표시할 데이터가 없습니다.', children }: AsyncStateProps) {
  if (loading) return <Skeleton active paragraph={{ rows: 7 }} aria-label="데이터를 불러오는 중" />
  if (error) return (
    <Alert
      type="error"
      showIcon
      message="데이터를 불러오지 못했습니다"
      description={error.message}
      action={onRetry ? <Button icon={<ReloadOutlined />} onClick={onRetry}>다시 시도</Button> : undefined}
    />
  )
  if (empty) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyDescription} />
  return <>{children}</>
}
