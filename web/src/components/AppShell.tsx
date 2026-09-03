import {
  AppstoreOutlined,
  AuditOutlined,
  BellOutlined,
  CloudServerOutlined,
  CodeOutlined,
  ControlOutlined,
  DashboardOutlined,
  DollarOutlined,
  ExperimentOutlined,
  FileProtectOutlined,
  IdcardOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  NotificationOutlined,
  PictureOutlined,
  ProjectOutlined,
  RobotOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  SettingOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { Avatar, Button, Drawer, Dropdown, Flex, Grid, Input, Layout, Menu, Space, Tag, type MenuProps } from 'antd'
import { useMemo, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { canAccessNavigationPath, canUseGlobalSearch, firstAccessiblePath, selectedNavigationPath, serviceVersionLabel } from '../utils/navigation'

const { Header, Sider, Content } = Layout

const primaryItems: MenuProps['items'] = [
  { key: '/dashboard', icon: <DashboardOutlined />, label: <Link to="/dashboard">통합 대시보드</Link> },
  {
    type: 'group', label: '자원 관리', children: [
      { key: '/hubs', icon: <AppstoreOutlined />, label: <Link to="/hubs">JupyterHub</Link> },
      { key: '/users', icon: <TeamOutlined />, label: <Link to="/users">통합 사용자</Link> },
      { key: '/servers', icon: <CloudServerOutlined />, label: <Link to="/servers">Notebook 서버</Link> },
      { key: '/gpus', icon: <ExperimentOutlined />, label: <Link to="/gpus">GPU</Link> },
      { key: '/projects', icon: <ProjectOutlined />, label: <Link to="/projects">프로젝트</Link> },
    ],
  },
  {
    type: 'group', label: '운영 및 거버넌스', children: [
      { key: '/policies', icon: <FileProtectOutlined />, label: <Link to="/policies">정책</Link> },
      { key: '/profiles', icon: <ControlOutlined />, label: <Link to="/profiles">환경 프로필</Link> },
      { key: '/images', icon: <PictureOutlined />, label: <Link to="/images">이미지</Link> },
      { key: '/approvals', icon: <SafetyCertificateOutlined />, label: <Link to="/approvals">검토·승인</Link> },
      { key: '/incidents', icon: <NotificationOutlined />, label: <Link to="/incidents">인시던트</Link> },
      { key: '/audit', icon: <AuditOutlined />, label: <Link to="/audit">감사 로그</Link> },
      { key: '/costs', icon: <DollarOutlined />, label: <Link to="/costs">비용·용량</Link> },
    ],
  },
  { type: 'group', label: 'AI 운영', children: [
    { key: '/ai-ops', icon: <RobotOutlined />, label: <Link to="/ai-ops">AI·LLM 운영</Link> },
  ] },
]

export function AppShell({ children }: { children: ReactNode }) {
  const { user, version, features, hasGlobalPermission, hasAnyGlobalPermission, logout } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const screens = Grid.useBreakpoint()
  const desktop = Boolean(screens.lg)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [collapsed, setCollapsed] = useState(() => {
    try { return window.localStorage?.getItem('jupiq.sidebar.collapsed') === 'true' } catch { return false }
  })

  const items = useMemo<MenuProps['items']>(() => {
    const permitted = (item: NonNullable<MenuProps['items']>[number]) => {
      if (!item || !('key' in item) || typeof item.key !== 'string') return true
      return canAccessNavigationPath(item.key, user?.permissions, features, user?.global_permissions)
    }
    const featureAwareItems = (primaryItems || []).map((item) => {
      if (!item) return null
      if (!('children' in item) || !Array.isArray(item.children)) return permitted(item) ? item : null
      const children = item.children.filter((child) => {
        if (!child || !('key' in child)) return true
        return permitted(child)
      })
      return children.length ? { ...item, children } : null
    }).filter(Boolean)
    return [
    ...featureAwareItems,
    ...(hasAnyGlobalPermission(['settings:read', 'settings:write']) ? [{ type: 'group' as const, label: '서비스 관리', children: [
      { key: '/admin/settings', icon: <SettingOutlined />, label: <Link to="/admin/settings">관리자 설정</Link> },
    ] }] : []),
    { type: 'group' as const, label: '개인화', children: [
      { key: '/personal', icon: <IdcardOutlined />, label: <Link to="/personal">내 프로필·API 키</Link> },
    ] },
    ]
  }, [features, hasAnyGlobalPermission, user?.global_permissions, user?.permissions])

  const homePath = firstAccessiblePath(user?.permissions, features, user?.global_permissions)
  const canGlobalSearch = canUseGlobalSearch(user?.global_permissions)

  const toggleCollapsed = () => {
    const next = !collapsed
    setCollapsed(next)
    try { window.localStorage?.setItem('jupiq.sidebar.collapsed', String(next)) } catch { /* 브라우저 저장소 차단 시 현재 세션만 유지 */ }
  }

  const profileItems: MenuProps['items'] = [
    { key: 'identity', disabled: true, label: <div className="profile-summary"><strong>{user?.display_name || user?.name || user?.username}</strong><span>{user?.email || user?.department || 'jupiq 사용자'}</span></div> },
    { type: 'divider' },
    { key: 'profile', icon: <UserOutlined />, label: '내 프로필', onClick: () => navigate('/personal?tab=profile') },
    ...(hasGlobalPermission('profile:keys') ? [{ key: 'keys', icon: <CodeOutlined />, label: 'API 키 관리', onClick: () => navigate('/personal?tab=keys') }] : []),
    { type: 'divider' },
    { key: 'version', disabled: true, label: <Flex justify="space-between" gap={24}><span>서비스 버전</span><Tag>{serviceVersionLabel(version.version)}</Tag></Flex> },
    { key: 'logout', danger: true, label: '로그아웃', onClick: async () => { await logout(); navigate('/login', { replace: true }) } },
  ]

  const navigation = (
    <>
      <Link className="brand" to={homePath} aria-label="jupiq 첫 화면으로 이동">
        <img src="/logo.svg" alt="jupiq" />
        {!collapsed && <span>Control Plane</span>}
      </Link>
      <Menu
        className="main-menu"
        mode="inline"
        items={items}
        selectedKeys={[selectedNavigationPath(location.pathname)]}
        onClick={() => setDrawerOpen(false)}
        inlineCollapsed={desktop ? collapsed : false}
      />
    </>
  )

  return (
    <Layout className="app-layout">
      {desktop ? (
        <Sider width={272} collapsedWidth={84} collapsed={collapsed} className="app-sider" theme="light">
          {navigation}
        </Sider>
      ) : (
        <Drawer title="jupiq 메뉴" placement="left" open={drawerOpen} onClose={() => setDrawerOpen(false)} width={300} styles={{ body: { padding: 0 } }}>
          <nav aria-label="주 메뉴">{navigation}</nav>
        </Drawer>
      )}
      <Layout>
        <Header className="app-header">
          <Flex align="center" justify="space-between" gap={12}>
            <Button
              type="text"
              aria-label={desktop ? (collapsed ? '메뉴 펼치기' : '메뉴 접기') : '메뉴 열기'}
              icon={desktop ? (collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />) : <MenuUnfoldOutlined />}
              onClick={desktop ? toggleCollapsed : () => setDrawerOpen(true)}
            />
            {canGlobalSearch && <Input.Search
              className="global-search"
              allowClear
              prefix={<SearchOutlined />}
              placeholder="사용자, Hub, 서버 통합 검색"
              aria-label="통합 검색"
              onSearch={(term) => term.trim() && navigate(`/search?q=${encodeURIComponent(term.trim())}`)}
            />}
            <Space size={8}>
              {canGlobalSearch && <Button className="mobile-search-button" type="text" icon={<SearchOutlined />} aria-label="통합 검색 열기" onClick={() => navigate('/search')} />}
              {hasGlobalPermission('notification:read') && <Button type="text" icon={<BellOutlined />} aria-label="알림 센터" onClick={() => navigate('/notifications')} />}
              <Dropdown menu={{ items: profileItems }} trigger={['click']} overlayClassName="profile-dropdown" placement="bottomRight">
                <Button className="profile-trigger" type="text" aria-label="사용자 메뉴 열기">
                  <Avatar icon={<UserOutlined />} />
                  {desktop && <span>{user?.display_name || user?.name || user?.username}</span>}
                </Button>
              </Dropdown>
            </Space>
          </Flex>
        </Header>
        <Content id="main-content" className="app-content" tabIndex={-1}>
          <div className="app-content-inner">{children}</div>
        </Content>
      </Layout>
    </Layout>
  )
}
