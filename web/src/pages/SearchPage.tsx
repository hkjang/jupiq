import { AppstoreOutlined, CloudServerOutlined, ProjectOutlined, TeamOutlined } from '@ant-design/icons'
import { Card, Empty, Input, List, Space, Tag, Typography } from 'antd'
import type { ReactNode } from 'react'
import { useSearchParams, useNavigate } from 'react-router-dom'
import { AsyncState } from '../components/AsyncState'
import { PageHeader } from '../components/PageHeader'
import { useApi } from '../hooks/useApi'
import type { ApiRecord } from '../types'
import { asText } from '../utils/format'

const typeInfo: Record<string, { label: string; icon: ReactNode; color: string }> = {
  user: { label: '사용자', icon: <TeamOutlined />, color: 'blue' },
  hub: { label: 'JupyterHub', icon: <AppstoreOutlined />, color: 'purple' },
  server: { label: 'Notebook 서버', icon: <CloudServerOutlined />, color: 'cyan' },
  project: { label: '프로젝트', icon: <ProjectOutlined />, color: 'gold' },
}

function SearchResults({ query }: { query: string }) {
  const navigate = useNavigate()
  const { data, loading, error, reload } = useApi<ApiRecord>(`/search?q=${encodeURIComponent(query)}`)
  const items = Array.isArray(data?.items) ? data.items.filter((item): item is ApiRecord => Boolean(item) && typeof item === 'object') : []
  return (
    <AsyncState loading={loading} error={error} onRetry={reload} empty={!loading && !error && items.length === 0} emptyDescription="권한 범위에서 일치하는 사용자, Hub, 서버 또는 프로젝트가 없습니다.">
      <Card>
        <List<ApiRecord>
          dataSource={items}
          renderItem={(item) => {
            const type = asText(item.type)
            const info = typeInfo[type] || { label: type, icon: null, color: 'default' }
            return <List.Item className="search-result-item"><button type="button" className="search-result-button" onClick={() => navigate(asText(item.path, '/'))}><List.Item.Meta avatar={info.icon} title={<Space><Tag color={info.color}>{info.label}</Tag><strong>{asText(item.title)}</strong></Space>} description={asText(item.subtitle)} /></button></List.Item>
          }}
        />
      </Card>
    </AsyncState>
  )
}

export function SearchPage() {
  const [params, setParams] = useSearchParams()
  const query = (params.get('q') || '').trim()
  return (
    <>
      <PageHeader title="통합 검색" description="내 권한 범위에서 사용자, JupyterHub, Notebook 서버와 프로젝트를 한 번에 찾습니다." />
      <Input.Search key={query} size="large" allowClear defaultValue={query} placeholder="2자 이상 검색어 입력" enterButton="검색" aria-label="통합 검색어" onSearch={(value) => setParams(value.trim() ? { q: value.trim() } : {})} />
      <div className="search-results">
        {query.length >= 2 ? <SearchResults query={query} /> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<Typography.Text type="secondary">검색어를 2자 이상 입력해 주세요.</Typography.Text>} />}
      </div>
    </>
  )
}
