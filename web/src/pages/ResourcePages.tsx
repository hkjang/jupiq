import { ApiOutlined, PlayCircleOutlined, PoweroffOutlined, ReloadOutlined, SyncOutlined } from '@ant-design/icons'
import { Alert, Button, Result, Tag } from 'antd'
import { Link } from 'react-router-dom'
import { jsonBody, request } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { ResourceListPage, type ResourceColumn, type ResourceField, type RowAction } from '../components/ResourceListPage'
import type { ApiRecord } from '../types'
import { asText, pick, statusTone } from '../utils/format'
import { hubPreflightPayload } from '../utils/hubs'
import { formatManagedUserRuntime, managedUserFields } from '../utils/managedUsers'
import { canOpenUserDetail } from '../utils/navigation'

const id = (record: ApiRecord) => encodeURIComponent(asText(pick(record, 'id', 'uuid', 'name'), ''))

const hubColumns: ResourceColumn[] = [
  { title: 'Hub', keys: ['name'], width: 160 },
  { title: '망', keys: ['network', 'network_name'], width: 120 },
  { title: 'URL', keys: ['base_url'], width: 260 },
  { title: '상태', keys: ['status'], format: 'status', width: 110, sortKey: 'status' },
  { title: '버전', keys: ['version'], width: 100 },
  { title: '사용자', keys: ['user_count', 'users'], format: 'number', width: 90, suffix: '명' },
  { title: '실행 서버', keys: ['running_servers', 'servers'], format: 'number', width: 100, suffix: '대' },
  { title: '마지막 통신', keys: ['last_success_at', 'last_seen_at'], format: 'date', width: 180 },
]
const hubFields: ResourceField[] = [
  { name: 'name', label: 'Hub 이름', required: true, placeholder: '예: 업무망 JupyterHub' },
  { name: 'network', label: '망 구분', required: true, placeholder: '예: 업무망' },
  { name: 'base_url', label: 'JupyterHub URL', required: true, placeholder: 'https://jupyter.internal' },
  { name: 'api_token', label: '관리자 API 토큰', type: 'password', required: true, help: '등록할 때는 필수입니다. 편집에서 URL·TLS가 같으면 비워 두어 기존 암호화 토큰을 유지할 수 있고, 둘 중 하나를 바꾸면 token을 다시 입력해야 합니다.' },
  { name: 'verify_tls', label: 'TLS 인증서 검증', type: 'switch', initialValue: true },
  { name: 'collect_interval_seconds', label: '수집 주기(초)', type: 'number', min: 30, max: 3600, initialValue: 60 },
  { name: 'enabled', label: '중앙 관리 사용', type: 'switch', initialValue: true },
]
const hubActions: RowAction[] = [
  { key: 'test', label: '연결 테스트', icon: <ApiOutlined />, path: (row) => `/hubs/${id(row)}/test`, permission: 'hubs:write' },
  { key: 'sync', label: '지금 동기화', icon: <SyncOutlined />, path: (row) => `/hubs/${id(row)}/sync`, permission: 'hubs:write' },
]

export function HubsPage() {
  return <ResourceListPage
    title="JupyterHub 관리"
    description="망별 Hub 연결, 수집 상태와 버전을 중앙에서 관리합니다. 등록 Drawer에서 현재 입력값을 저장 전에 검증할 수 있습니다."
    endpoint="/hubs"
    columns={hubColumns}
    fields={hubFields}
    rowActions={hubActions}
    writePermission="hubs:write"
    scopeAwareWrite
    globalCreate
    createLabel="Hub 등록"
    emptyDescription="등록된 JupyterHub가 없습니다."
    editorVariant="drawer"
    editorTest={{
      label: '현재 입력값 연결 테스트',
      run: (values, editing) => request<ApiRecord & { success: boolean }>('/integrations/test', {
        method: 'POST',
        body: jsonBody(hubPreflightPayload(values, pick(editing || {}, 'id'))),
      }),
    }}
  />
}

function managedUserColumns(canViewDetails: boolean): ResourceColumn[] {
  return [
    { title: '사용자 ID', keys: ['username', 'user_name'], width: 150, sortKey: 'username', render: (value) => {
      const username = asText(value, '')
      if (!username) return '—'
      return canViewDetails ? <Link className="text-link" to={`/users/${encodeURIComponent(username)}`}>{username}</Link> : username
    } },
    { title: '이름', keys: ['display_name', 'name'], width: 130, sortKey: 'display_name' },
    { title: '부서', keys: ['department', 'department_name'], width: 140, sortKey: 'department' },
    { title: 'Hub', keys: ['hub_name', 'hub'], width: 150 },
    { title: '역할', keys: managedUserFields.roles, format: 'tags', width: 180 },
    { title: '서버 상태', keys: managedUserFields.serverStatus, format: 'status', width: 110, sortKey: 'server_status' },
    { title: '실행 서버', keys: managedUserFields.runningServers, format: 'number', suffix: '대', width: 105, sortKey: 'running_servers' },
    { title: '총 실행시간', keys: managedUserFields.runtime, width: 125, render: (value) => formatManagedUserRuntime(value), sortKey: 'runtime_seconds' },
    { title: 'CPU', keys: managedUserFields.cpu, format: 'number', suffix: ' Core', width: 100 },
    { title: 'RAM', keys: managedUserFields.memory, format: 'bytes', width: 110 },
    { title: '최근 활동', keys: managedUserFields.lastActivity, format: 'date', width: 180, sortKey: 'last_activity_at' },
  ]
}

const linkedManagedUserColumns = managedUserColumns(true)
const plainManagedUserColumns = managedUserColumns(false)

export function UsersPage() {
  const { user } = useAuth()
  const columns = canOpenUserDetail(user?.global_permissions) ? linkedManagedUserColumns : plainManagedUserColumns
  return <ResourceListPage title="통합 사용자" description="모든 Hub의 사용자, 조직, 활동과 자원 현황을 조회합니다." endpoint="/users" emptyDescription="동기화된 사용자가 없습니다." columns={columns} />
}

const serverActions: RowAction[] = [
  { key: 'start', label: '서버 시작', icon: <PlayCircleOutlined />, path: (row) => `/servers/${id(row)}/start`, permission: 'servers:operate', visible: (row) => !/running|starting/i.test(asText(pick(row, 'status'))) },
  { key: 'stop', label: '서버 종료', icon: <PoweroffOutlined />, path: (row) => `/servers/${id(row)}/stop`, permission: 'servers:operate', danger: true, confirm: '사용자 작업이 중단될 수 있습니다. 서버를 종료하시겠습니까?', visible: (row) => /running|active/i.test(asText(pick(row, 'status'))) },
  { key: 'restart', label: '서버 재시작', icon: <ReloadOutlined />, path: (row) => `/servers/${id(row)}/restart`, permission: 'servers:operate', confirm: '현재 커널 연결이 끊어집니다. 서버를 재시작하시겠습니까?', visible: (row) => /running|active/i.test(asText(pick(row, 'status'))) },
]
const serverColumns: ResourceColumn[] = [
  { title: '사용자', keys: ['username', 'user_name'], width: 150, sortKey: 'username' },
  { title: 'Hub', keys: ['hub_name', 'hub'], width: 150 },
  { title: '상태', keys: ['status'], format: 'status', width: 110 },
  { title: '프로필', keys: ['profile_name', 'profile'], width: 140 },
  { title: '이미지', keys: ['image', 'image_name'], width: 220, sortKey: 'image' },
  { title: 'Node / Pod', keys: ['node_name', 'pod_name'], width: 180, render: (_value, row) => <>{asText(pick(row, 'node_name'))}<br /><small>{asText(pick(row, 'pod_name'))}</small></> },
  { title: '실행시간', keys: ['runtime', 'running_time'], width: 110 },
  { title: 'CPU', keys: ['cpu_cores', 'cpu'], format: 'number', suffix: ' Core', width: 100, sortKey: 'cpu_cores' },
  { title: 'RAM', keys: ['memory_bytes', 'memory'], format: 'bytes', width: 110, sortKey: 'memory_bytes' },
  { title: '시작 시각', keys: ['started_at'], format: 'date', width: 180, sortKey: 'started_at' },
]

export function ServersPage() {
  return <ResourceListPage title="Notebook 서버" description="사용자 서버의 실행 상태와 자원을 확인하고 안전하게 제어합니다." endpoint="/servers" rowActions={serverActions} scopeAwareWrite emptyDescription="조회된 Notebook 서버가 없습니다." columns={serverColumns} />
}

const gpuColumns: ResourceColumn[] = [
  { title: 'Hub / 망', keys: ['hub'], width: 170, render: (_value, row) => <>{asText(pick(row, 'hub'))}<br /><small>{asText(pick(row, 'network'))}</small></> },
  { title: 'GPU 노드', keys: ['node_name', 'hostname'], width: 160 },
  { title: '할당 GPU', keys: ['gpu_count'], format: 'number', suffix: '장', width: 100 },
  { title: 'GPU 사용률', keys: ['utilization', 'gpu_utilization'], format: 'percent', width: 120 },
  { title: 'VRAM 사용량', keys: ['vram_bytes', 'memory_used', 'vram_used'], format: 'bytes', width: 120 },
  { title: '사용자', keys: ['username', 'user_name'], width: 140 },
  { title: 'Pod', keys: ['pod_name', 'pod'], width: 210 },
  { title: '수집 시각', keys: ['sampled_at'], format: 'date', width: 180 },
  { title: '최신성', keys: ['stale'], width: 90, render: (value) => value === true ? <Tag color="warning">오래됨</Tag> : <Tag color="success">최신</Tag> },
]
const gpuNote = <Alert type="info" showIcon message="GPU Util이 장시간 낮은 할당은 낭비 후보로 분류됩니다." description="DCGM Exporter 지표를 Pod 단위로 집계합니다. GPU 모니터링을 끄면 수집·API·화면 노출이 모두 중단됩니다." />

export function GpusPage() {
  const { features, isAdmin } = useAuth()
  if (!features.gpuMonitoring) return <Result status="info" title="GPU 모니터링이 비활성화되어 있습니다" subTitle="GPU 데이터는 수집되지 않으며 관련 메뉴와 통계도 표시되지 않습니다." extra={isAdmin ? <Button type="primary"><Link to="/admin/settings?tab=features">관리자 설정에서 사용</Link></Button> : undefined} />
  return <ResourceListPage title="GPU 모니터링" description="실행 서버·Pod·사용자별 GPU 할당과 수집 지표를 확인합니다." endpoint="/gpus" emptyDescription="수집된 GPU 메트릭이 없습니다." dataNote={gpuNote} columns={gpuColumns} />
}

const projectFields: ResourceField[] = [
  { name: 'name', label: '프로젝트 이름', required: true }, { name: 'owner', label: '책임자', required: true },
  { name: 'description', label: '설명', type: 'textarea' }, { name: 'hub_ids', label: '허용 Hub ID', placeholder: '쉼표로 구분' },
  { name: 'cpu_quota', label: 'CPU Quota', type: 'number', min: 0 }, { name: 'memory_quota_gb', label: 'RAM Quota(GB)', type: 'number', min: 0 },
  { name: 'gpu_quota', label: 'GPU Quota', type: 'number', min: 0 }, { name: 'budget', label: '월 예산', type: 'number', min: 0 },
]
const projectColumns: ResourceColumn[] = [
  { title: '프로젝트', keys: ['name'], width: 180, sortKey: 'name' }, { title: '책임자', keys: ['owner_name', 'owner'], width: 130 },
  { title: '구성원', keys: ['member_count', 'members'], format: 'number', suffix: '명', width: 100 }, { title: 'Hub', keys: ['hubs', 'hub_names'], format: 'tags', width: 180 },
  { title: 'CPU Quota', keys: ['cpu_quota'], width: 110 }, { title: 'RAM Quota', keys: ['memory_quota_gb'], format: 'number', suffix: 'GB', width: 110 },
  { title: 'GPU Quota', keys: ['gpu_quota'], width: 110 }, { title: '상태', keys: ['status'], format: 'status', width: 100 },
  { title: '종료일', keys: ['end_date'], format: 'date', width: 160 },
]
const projectNote = <Alert type="info" showIcon message="v1.2는 프로젝트·Quota 등록과 조회를 제공합니다" description="JupyterHub 또는 Kubernetes에 Quota를 자동 집행하지는 않습니다." />

export function ProjectsPage() {
  return <ResourceListPage title="프로젝트" description="구성원, 기간과 계획 자원 한도를 프로젝트 단위 카탈로그로 관리합니다." dataNote={projectNote} endpoint="/projects" writePermission="project:write" createLabel="프로젝트 등록" emptyDescription="등록된 프로젝트가 없습니다." fields={projectFields} columns={projectColumns} />
}

const policyFields: ResourceField[] = [
  { name: 'name', label: '정책 이름', required: true }, { name: 'target_type', label: '대상 유형', type: 'select', required: true, options: [{ label: '전체', value: 'global' }, { label: 'Hub', value: 'hub' }, { label: '그룹', value: 'group' }, { label: '사용자', value: 'user' }] },
  { name: 'target', label: '대상' }, { name: 'idle_timeout_minutes', label: 'Idle 제한(분)', type: 'number', min: 0 }, { name: 'max_runtime_minutes', label: '최대 실행시간(분)', type: 'number', min: 0 },
  { name: 'rules', label: '추가 규칙(JSON)', type: 'textarea' }, { name: 'enabled', label: '정책 사용', type: 'switch', initialValue: true },
]
const policyColumns: ResourceColumn[] = [
  { title: '정책', keys: ['name'], width: 180, sortKey: 'name' }, { title: '대상 유형', keys: ['target_type'], width: 110 }, { title: '대상', keys: ['target_name', 'target'], width: 160 },
  { title: 'Idle 제한', keys: ['idle_timeout_minutes'], format: 'number', suffix: '분', width: 110 }, { title: '최대 실행', keys: ['max_runtime_minutes'], format: 'number', suffix: '분', width: 110 },
  { title: '상태', keys: ['enabled', 'status'], format: 'status', width: 100 }, { title: '수정 시각', keys: ['updated_at'], format: 'date', width: 180, sortKey: 'updated_at' },
]
const policyNote = <Alert type="info" showIcon message="정책 카탈로그" description="v1.2는 정책 자동 배포·Idle 종료·Quota 집행을 수행하지 않습니다." />

export function PoliciesPage() {
  return <ResourceListPage title="정책" description="망·그룹·사용자별 운영 정책을 중앙 카탈로그로 등록하고 비교합니다." dataNote={policyNote} endpoint="/policies" writePermission="policy:write" createLabel="정책 작성" emptyDescription="등록된 정책이 없습니다." fields={policyFields} columns={policyColumns} />
}

const profileFields: ResourceField[] = [
  { name: 'name', label: '프로필 이름', required: true }, { name: 'description', label: '설명', type: 'textarea' },
  { name: 'cpu_limit', label: 'CPU Core', type: 'number', required: true, min: 0 }, { name: 'memory_gb', label: 'RAM(GB)', type: 'number', required: true, min: 0 },
  { name: 'gpu_count', label: 'GPU 개수', type: 'number', min: 0 }, { name: 'storage_gb', label: 'Storage(GB)', type: 'number', min: 0 },
  { name: 'image_id', label: '이미지 ID' }, { name: 'enabled', label: '프로필 사용', type: 'switch', initialValue: true },
]
const profileColumns: ResourceColumn[] = [
  { title: '프로필', keys: ['name'], width: 180, sortKey: 'name' }, { title: '설명', keys: ['description'], width: 240 },
  { title: 'CPU', keys: ['cpu_limit', 'cpu'], format: 'number', suffix: ' Core', width: 100 }, { title: 'RAM', keys: ['memory_gb', 'memory'], format: 'number', suffix: 'GB', width: 100 },
  { title: 'GPU', keys: ['gpu_count', 'gpu'], format: 'number', width: 90 }, { title: 'Storage', keys: ['storage_gb'], format: 'number', suffix: 'GB', width: 110 },
  { title: '이미지', keys: ['image_name', 'image'], width: 180 }, { title: '상태', keys: ['enabled', 'status'], format: 'status', width: 100 },
]
const profileNote = <Alert type="info" showIcon message="프로필 카탈로그" description="Hub Spawner 자동 배포와 rollback은 후속 범위입니다." />

export function ProfilesPage() {
  return <ResourceListPage title="환경 프로필" description="CPU, RAM, GPU와 실행 이미지 조합을 표준 템플릿 카탈로그로 관리합니다." dataNote={profileNote} endpoint="/profiles" writePermission="profiles:write" createLabel="프로필 등록" emptyDescription="등록된 환경 프로필이 없습니다." fields={profileFields} columns={profileColumns} />
}

const imageFields: ResourceField[] = [
  { name: 'name', label: '이미지 이름', required: true }, { name: 'image', label: 'Registry 경로', required: true, placeholder: 'registry.internal/team/image:tag' },
  { name: 'version', label: '버전', required: true }, { name: 'description', label: '설명', type: 'textarea' },
  { name: 'lifecycle', label: '수명주기', type: 'select', options: [{ label: '초안', value: 'draft' }, { label: '테스트', value: 'testing' }, { label: '승인', value: 'approved' }, { label: '운영', value: 'production' }, { label: '사용 중단', value: 'deprecated' }] },
  { name: 'default', label: '기본 이미지', type: 'switch' },
]
const imageColumns: ResourceColumn[] = [
  { title: '이름', keys: ['name'], width: 170, sortKey: 'name' }, { title: '이미지', keys: ['image', 'repository'], width: 280 }, { title: '버전', keys: ['version', 'tag'], width: 100 },
  { title: '단계', keys: ['lifecycle', 'status'], format: 'status', width: 110 }, { title: '취약점', keys: ['critical_vulnerabilities', 'critical_count'], format: 'number', suffix: '건', width: 100 },
  { title: 'SBOM', keys: ['sbom_status'], format: 'status', width: 100 }, { title: '기본', keys: ['default', 'is_default'], format: 'boolean', width: 90 },
  { title: '수정 시각', keys: ['updated_at'], format: 'date', width: 180, sortKey: 'updated_at' },
]
const imageNote = <Alert type="info" showIcon message="이미지 검증 결과는 외부 도구에서 등록합니다" description="jupiq v1.2 자체는 Registry 배포, SBOM 생성이나 취약점 스캔을 수행하지 않습니다." />

export function ImagesPage() {
  return <ResourceListPage title="Notebook 이미지" description="외부에서 검증한 실행 이미지의 상태와 수명주기를 카탈로그로 관리합니다." dataNote={imageNote} endpoint="/images" writePermission="image:write" createLabel="이미지 등록" emptyDescription="등록된 Notebook 이미지가 없습니다." fields={imageFields} columns={imageColumns} />
}

const incidentFields: ResourceField[] = [
  { name: 'name', label: '제목', required: true }, { name: 'severity', label: '심각도', type: 'select', required: true, options: [{ label: '심각', value: 'critical' }, { label: '주의', value: 'warning' }, { label: '정보', value: 'info' }] },
  { name: 'hub_id', label: 'Hub ID' }, { name: 'description', label: '상세 내용', type: 'textarea', required: true }, { name: 'status', label: '상태', type: 'select', options: [{ label: '진행 중', value: 'open' }, { label: '해결', value: 'resolved' }] },
]
const incidentColumns: ResourceColumn[] = [
  { title: '번호', keys: ['incident_no', 'id'], width: 150 }, { title: '심각도', keys: ['severity'], format: 'status', width: 100 }, { title: '제목', keys: ['title', 'name'], width: 260 },
  { title: 'Hub', keys: ['hub_name', 'hub'], width: 140 }, { title: '영향 사용자', keys: ['affected_users'], format: 'number', suffix: '명', width: 110 },
  { title: '상태', keys: ['status'], format: 'status', width: 100 }, { title: '발생 시각', keys: ['started_at', 'created_at'], format: 'date', width: 180 }, { title: '복구 시각', keys: ['resolved_at'], format: 'date', width: 180 },
]
const incidentNote = <Alert type="info" showIcon message="수동 인시던트 기록" description="수집 장애의 자동 인시던트 생성은 후속 범위입니다." />

export function IncidentsPage() {
  return <ResourceListPage title="인시던트" description="Hub, Pod, GPU와 인증 장애를 수동 등록해 영향과 복구 상태를 추적합니다." dataNote={incidentNote} endpoint="/incidents" writePermission="incident:write" createLabel="인시던트 등록" emptyDescription="등록된 인시던트가 없습니다." fields={incidentFields} columns={incidentColumns} />
}

const auditColumns: ResourceColumn[] = [
  { title: '시각', keys: ['created_at', 'timestamp'], format: 'date', width: 180, sortKey: 'created_at' }, { title: '행위자', keys: ['actor_name', 'username', 'actor'], width: 140, sortKey: 'actor_username' },
  { title: '작업', keys: ['action'], width: 170, sortKey: 'action' }, { title: '대상 유형', keys: ['resource_type', 'target_type'], width: 120, sortKey: 'resource_type' }, { title: '대상', keys: ['resource_name', 'target'], width: 190 },
  { title: 'Hub', keys: ['hub_name'], width: 130 }, { title: 'IP', keys: ['ip_address', 'ip'], width: 130, sortKey: 'ip_address' }, { title: '결과', keys: ['result', 'status'], format: 'status', width: 100, sortKey: 'result' },
]

export function AuditPage() {
  return <ResourceListPage title="감사 로그" description="사용자·관리자·API 작업의 대상, 변경 전후와 결과를 추적합니다." endpoint="/audit" emptyDescription="감사 로그가 없습니다." columns={auditColumns} />
}

const costNote = <Alert type="info" showIcon message="비용 결과 카탈로그" description="자동 단가 환산과 용량 포화 예측은 후속 범위입니다." />
const costBaseColumns: ResourceColumn[] = [
  { title: '집계 대상', keys: ['name', 'project_name', 'department'], width: 180 }, { title: '구분', keys: ['type', 'group_type'], width: 110 },
  { title: 'CPU 시간', keys: ['cpu_hours'], format: 'number', suffix: 'h', width: 110 }, { title: 'RAM 시간', keys: ['memory_gb_hours'], format: 'number', suffix: 'GBh', width: 120 },
]
const costGpuColumn: ResourceColumn = { title: 'GPU 시간', keys: ['gpu_hours'], format: 'number', suffix: 'h', width: 110 }
const costTailColumns: ResourceColumn[] = [
  { title: 'Storage', keys: ['storage_gb_month'], format: 'number', suffix: 'GB·월', width: 120 }, { title: '추정 비용', keys: ['estimated_cost', 'cost'], format: 'number', suffix: '원', width: 140 },
  { title: '예산 사용률', keys: ['budget_usage'], format: 'percent', width: 120 }, { title: '기간', keys: ['period'], width: 120 },
]
const costColumnsWithGpu: ResourceColumn[] = [...costBaseColumns, costGpuColumn, ...costTailColumns]
const costColumnsWithoutGpu: ResourceColumn[] = [...costBaseColumns, ...costTailColumns]

export function CostsPage() {
  const { features } = useAuth()
  return <ResourceListPage title="비용·용량" description="관리자가 등록한 부서·프로젝트·망별 비용 산정 결과를 조회합니다." dataNote={costNote} endpoint="/costs" emptyDescription="등록된 비용 데이터가 없습니다." columns={features.gpuMonitoring ? costColumnsWithGpu : costColumnsWithoutGpu} />
}

const notificationFields: ResourceField[] = [
  { name: 'name', label: '규칙 이름', required: true }, { name: 'event_type', label: '이벤트 유형', required: true },
  { name: 'severity', label: '최소 심각도', type: 'select', options: [{ label: '정보', value: 'info' }, { label: '주의', value: 'warning' }, { label: '심각', value: 'critical' }] },
  { name: 'channel', label: '채널', type: 'select', options: [{ label: '포털', value: 'portal' }, { label: 'Webhook', value: 'webhook' }, { label: '이메일', value: 'email' }] },
  { name: 'enabled', label: '규칙 사용', type: 'switch', initialValue: true },
]
const notificationColumns: ResourceColumn[] = [
  { title: '시각', keys: ['created_at'], format: 'date', width: 180 }, { title: '심각도', keys: ['severity'], format: 'status', width: 100 },
  { title: '제목 / 규칙', keys: ['title', 'name'], width: 220 }, { title: '내용', keys: ['message', 'description'], width: 300 }, { title: '채널', keys: ['channel'], width: 100 },
  { title: '전달 상태', keys: ['delivery_status', 'status'], format: 'status', width: 110 },
]
const notificationNote = <Alert type="info" showIcon message="알림 카탈로그" description="Webhook은 관리자 연결 테스트만 제공하며 운영 이벤트 자동 발송은 후속 범위입니다." />

export function NotificationsPage() {
  return <ResourceListPage title="알림 센터" description="운영 알림 규칙과 수동 기록을 카탈로그로 관리합니다." dataNote={notificationNote} endpoint="/notifications" writePermission="notification:write" createLabel="알림 규칙 등록" emptyDescription="표시할 알림이 없습니다." fields={notificationFields} columns={notificationColumns} />
}

export function StatusCell({ value }: { value: unknown }) {
  return <Tag color={statusTone(value)}>{asText(value)}</Tag>
}
