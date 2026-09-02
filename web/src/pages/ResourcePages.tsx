import { Alert, Button, Result, Tag } from 'antd'
import { Link } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { ResourceListPage, type ResourceColumn, type ResourceField, type RowAction } from '../components/ResourceListPage'
import type { ApiRecord } from '../types'
import { asText, pick, statusTone } from '../utils/format'

const id = (record: ApiRecord) => encodeURIComponent(asText(pick(record, 'id', 'uuid', 'name'), ''))

const hubColumns: ResourceColumn[] = [
  { title: 'Hub', keys: ['name'], width: 160 },
  { title: '망', keys: ['network', 'network_name'], width: 120 },
  { title: 'URL', keys: ['base_url'], width: 260 },
  { title: '상태', keys: ['status'], format: 'status', width: 110 },
  { title: '버전', keys: ['version'], width: 100 },
  { title: '사용자', keys: ['user_count', 'users'], format: 'number', width: 90, suffix: '명' },
  { title: '실행 서버', keys: ['running_servers', 'servers'], format: 'number', width: 100, suffix: '대' },
  { title: '마지막 통신', keys: ['last_success_at', 'last_seen_at'], format: 'date', width: 180 },
]
const hubFields: ResourceField[] = [
  { name: 'name', label: 'Hub 이름', required: true, placeholder: '예: 업무망 JupyterHub' },
  { name: 'network', label: '망 구분', required: true, placeholder: '예: 업무망' },
  { name: 'base_url', label: 'JupyterHub URL', required: true, placeholder: 'https://jupyter.internal' },
  { name: 'api_token', label: '관리자 API 토큰', type: 'password', required: true, help: '저장 후 다시 표시하지 않습니다.' },
  { name: 'verify_tls', label: 'TLS 인증서 검증', type: 'switch', initialValue: true },
  { name: 'collect_interval_seconds', label: '수집 주기(초)', type: 'number', min: 30, max: 3600, initialValue: 60 },
  { name: 'enabled', label: '중앙 관리 사용', type: 'switch', initialValue: true },
]
const hubActions: RowAction[] = [
  { key: 'test', label: '연결 테스트', path: (row) => `/hubs/${id(row)}/test` },
  { key: 'sync', label: '지금 동기화', path: (row) => `/hubs/${id(row)}/sync` },
]

export function HubsPage() {
  return <ResourceListPage title="JupyterHub 관리" description="망별 Hub 연결, 수집 상태와 버전을 중앙에서 관리합니다." endpoint="/hubs" columns={hubColumns} fields={hubFields} rowActions={hubActions} createLabel="Hub 등록" emptyDescription="등록된 JupyterHub가 없습니다." />
}

export function UsersPage() {
  return <ResourceListPage title="통합 사용자" description="모든 Hub의 사용자, 조직, 활동과 자원 현황을 조회합니다." endpoint="/users" emptyDescription="동기화된 사용자가 없습니다." columns={[
    { title: '사용자 ID', keys: ['username', 'user_name'], width: 150, render: (value) => {
      const username = asText(value, '')
      return username ? <Link className="text-link" to={`/users/${encodeURIComponent(username)}`}>{username}</Link> : '—'
    } },
    { title: '이름', keys: ['display_name', 'name'], width: 130 },
    { title: '부서', keys: ['department', 'department_name'], width: 140 },
    { title: 'Hub', keys: ['hub_name', 'hub'], width: 150 },
    { title: '역할', keys: ['roles', 'role'], format: 'tags', width: 180 },
    { title: '서버', keys: ['server_status', 'status'], format: 'status', width: 110 },
    { title: '실행시간', keys: ['runtime', 'running_time'], width: 110 },
    { title: 'CPU', keys: ['cpu_cores'], format: 'number', suffix: ' Core', width: 100 },
    { title: 'RAM', keys: ['memory_bytes'], format: 'bytes', width: 110 },
    { title: '최근 활동', keys: ['last_activity', 'last_login_at'], format: 'date', width: 180 },
  ]} />
}

export function ServersPage() {
  const actions: RowAction[] = [
    { key: 'start', label: '서버 시작', path: (row) => `/servers/${id(row)}/start`, visible: (row) => !/running|starting/i.test(asText(pick(row, 'status'))) },
    { key: 'stop', label: '서버 종료', path: (row) => `/servers/${id(row)}/stop`, danger: true, confirm: '사용자 작업이 중단될 수 있습니다. 서버를 종료하시겠습니까?', visible: (row) => /running|active/i.test(asText(pick(row, 'status'))) },
    { key: 'restart', label: '서버 재시작', path: (row) => `/servers/${id(row)}/restart`, confirm: '현재 커널 연결이 끊어집니다. 서버를 재시작하시겠습니까?', visible: (row) => /running|active/i.test(asText(pick(row, 'status'))) },
  ]
  return <ResourceListPage title="Notebook 서버" description="사용자 서버의 실행 상태와 자원을 확인하고 안전하게 제어합니다." endpoint="/servers" rowActions={actions} emptyDescription="조회된 Notebook 서버가 없습니다." columns={[
    { title: '사용자', keys: ['username', 'user_name'], width: 150 },
    { title: 'Hub', keys: ['hub_name', 'hub'], width: 150 },
    { title: '상태', keys: ['status'], format: 'status', width: 110 },
    { title: '프로필', keys: ['profile_name', 'profile'], width: 140 },
    { title: '이미지', keys: ['image', 'image_name'], width: 220 },
    { title: 'Node / Pod', keys: ['node_name', 'pod_name'], width: 180, render: (_value, row) => <>{asText(pick(row, 'node_name'))}<br /><small>{asText(pick(row, 'pod_name'))}</small></> },
    { title: '실행시간', keys: ['runtime', 'running_time'], width: 110 },
    { title: 'CPU', keys: ['cpu_cores', 'cpu'], format: 'number', suffix: ' Core', width: 100 },
    { title: 'RAM', keys: ['memory_bytes', 'memory'], format: 'bytes', width: 110 },
    { title: '시작 시각', keys: ['started_at'], format: 'date', width: 180 },
  ]} />
}

export function GpusPage() {
  const { features, isAdmin } = useAuth()
  if (!features.gpuMonitoring) return <Result status="info" title="GPU 모니터링이 비활성화되어 있습니다" subTitle="GPU 데이터는 수집되지 않으며 관련 메뉴와 통계도 표시되지 않습니다." extra={isAdmin ? <Button type="primary"><Link to="/admin/settings?tab=features">관리자 설정에서 사용</Link></Button> : undefined} />
  return <ResourceListPage title="GPU 모니터링" description="노드·장치·사용자별 GPU와 VRAM 효율을 실시간으로 확인합니다." endpoint="/gpus" emptyDescription="수집된 GPU 메트릭이 없습니다." dataNote={<Alert type="info" showIcon message="GPU Util이 장시간 낮은 할당은 낭비 후보로 분류됩니다." />} columns={[
    { title: 'GPU 노드', keys: ['node_name', 'hostname'], width: 160 },
    { title: '모델', keys: ['model', 'gpu_model'], width: 150 },
    { title: '장치', keys: ['index', 'device_id'], width: 80 },
    { title: '상태', keys: ['status'], format: 'status', width: 100 },
    { title: 'GPU 사용률', keys: ['utilization', 'gpu_utilization'], format: 'percent', width: 120 },
    { title: 'VRAM 사용률', keys: ['memory_utilization', 'vram_utilization'], format: 'percent', width: 120 },
    { title: 'VRAM', keys: ['memory_used', 'vram_used'], format: 'bytes', width: 110 },
    { title: '온도', keys: ['temperature'], format: 'number', suffix: '°C', width: 90 },
    { title: '사용자', keys: ['username', 'user_name'], width: 140 },
    { title: 'Pod', keys: ['pod_name', 'pod'], width: 210 },
  ]} />
}

export function ProjectsPage() {
  return <ResourceListPage title="프로젝트" description="구성원, 기간, 자원 한도와 비용을 프로젝트 단위로 관리합니다." endpoint="/projects" createLabel="프로젝트 등록" emptyDescription="등록된 프로젝트가 없습니다." fields={[
    { name: 'name', label: '프로젝트 이름', required: true }, { name: 'owner', label: '책임자', required: true },
    { name: 'description', label: '설명', type: 'textarea' }, { name: 'hub_ids', label: '허용 Hub ID', placeholder: '쉼표로 구분' },
    { name: 'cpu_quota', label: 'CPU Quota', type: 'number', min: 0 }, { name: 'memory_quota_gb', label: 'RAM Quota(GB)', type: 'number', min: 0 },
    { name: 'gpu_quota', label: 'GPU Quota', type: 'number', min: 0 }, { name: 'budget', label: '월 예산', type: 'number', min: 0 },
  ]} columns={[
    { title: '프로젝트', keys: ['name'], width: 180 }, { title: '책임자', keys: ['owner_name', 'owner'], width: 130 },
    { title: '구성원', keys: ['member_count', 'members'], format: 'number', suffix: '명', width: 100 }, { title: 'Hub', keys: ['hubs', 'hub_names'], format: 'tags', width: 180 },
    { title: 'CPU Quota', keys: ['cpu_quota'], width: 110 }, { title: 'RAM Quota', keys: ['memory_quota_gb'], format: 'number', suffix: 'GB', width: 110 },
    { title: 'GPU Quota', keys: ['gpu_quota'], width: 110 }, { title: '상태', keys: ['status'], format: 'status', width: 100 },
    { title: '종료일', keys: ['end_date'], format: 'date', width: 160 },
  ]} />
}

export function PoliciesPage() {
  return <ResourceListPage title="정책" description="망·그룹·사용자별 자원과 자동화 정책을 중앙에서 관리합니다." endpoint="/policies" createLabel="정책 작성" emptyDescription="등록된 정책이 없습니다." fields={[
    { name: 'name', label: '정책 이름', required: true }, { name: 'target_type', label: '대상 유형', type: 'select', required: true, options: [{ label: '전체', value: 'global' }, { label: 'Hub', value: 'hub' }, { label: '그룹', value: 'group' }, { label: '사용자', value: 'user' }] },
    { name: 'target', label: '대상' }, { name: 'idle_timeout_minutes', label: 'Idle 제한(분)', type: 'number', min: 0 }, { name: 'max_runtime_minutes', label: '최대 실행시간(분)', type: 'number', min: 0 },
    { name: 'rules', label: '추가 규칙(JSON)', type: 'textarea' }, { name: 'enabled', label: '정책 사용', type: 'switch', initialValue: true },
  ]} columns={[
    { title: '정책', keys: ['name'], width: 180 }, { title: '대상 유형', keys: ['target_type'], width: 110 }, { title: '대상', keys: ['target_name', 'target'], width: 160 },
    { title: 'Idle 제한', keys: ['idle_timeout_minutes'], format: 'number', suffix: '분', width: 110 }, { title: '최대 실행', keys: ['max_runtime_minutes'], format: 'number', suffix: '분', width: 110 },
    { title: '상태', keys: ['enabled', 'status'], format: 'status', width: 100 }, { title: '수정 시각', keys: ['updated_at'], format: 'date', width: 180 },
  ]} />
}

export function ProfilesPage() {
  return <ResourceListPage title="환경 프로필" description="CPU, RAM, GPU와 실행 이미지 조합을 표준 템플릿으로 관리합니다." endpoint="/profiles" createLabel="프로필 등록" emptyDescription="등록된 환경 프로필이 없습니다." fields={[
    { name: 'name', label: '프로필 이름', required: true }, { name: 'description', label: '설명', type: 'textarea' },
    { name: 'cpu_limit', label: 'CPU Core', type: 'number', required: true, min: 0 }, { name: 'memory_gb', label: 'RAM(GB)', type: 'number', required: true, min: 0 },
    { name: 'gpu_count', label: 'GPU 개수', type: 'number', min: 0 }, { name: 'storage_gb', label: 'Storage(GB)', type: 'number', min: 0 },
    { name: 'image_id', label: '이미지 ID' }, { name: 'enabled', label: '프로필 사용', type: 'switch', initialValue: true },
  ]} columns={[
    { title: '프로필', keys: ['name'], width: 180 }, { title: '설명', keys: ['description'], width: 240 },
    { title: 'CPU', keys: ['cpu_limit', 'cpu'], format: 'number', suffix: ' Core', width: 100 }, { title: 'RAM', keys: ['memory_gb', 'memory'], format: 'number', suffix: 'GB', width: 100 },
    { title: 'GPU', keys: ['gpu_count', 'gpu'], format: 'number', width: 90 }, { title: 'Storage', keys: ['storage_gb'], format: 'number', suffix: 'GB', width: 110 },
    { title: '이미지', keys: ['image_name', 'image'], width: 180 }, { title: '상태', keys: ['enabled', 'status'], format: 'status', width: 100 },
  ]} />
}

export function ImagesPage() {
  return <ResourceListPage title="Notebook 이미지" description="검증된 실행 이미지의 승인 상태와 수명주기를 관리합니다." endpoint="/images" createLabel="이미지 등록" emptyDescription="등록된 Notebook 이미지가 없습니다." fields={[
    { name: 'name', label: '이미지 이름', required: true }, { name: 'image', label: 'Registry 경로', required: true, placeholder: 'registry.internal/team/image:tag' },
    { name: 'version', label: '버전', required: true }, { name: 'description', label: '설명', type: 'textarea' },
    { name: 'lifecycle', label: '수명주기', type: 'select', options: [{ label: '초안', value: 'draft' }, { label: '테스트', value: 'testing' }, { label: '승인', value: 'approved' }, { label: '운영', value: 'production' }, { label: '사용 중단', value: 'deprecated' }] },
    { name: 'default', label: '기본 이미지', type: 'switch' },
  ]} columns={[
    { title: '이름', keys: ['name'], width: 170 }, { title: '이미지', keys: ['image', 'repository'], width: 280 }, { title: '버전', keys: ['version', 'tag'], width: 100 },
    { title: '단계', keys: ['lifecycle', 'status'], format: 'status', width: 110 }, { title: '취약점', keys: ['critical_vulnerabilities', 'critical_count'], format: 'number', suffix: '건', width: 100 },
    { title: 'SBOM', keys: ['sbom_status'], format: 'status', width: 100 }, { title: '기본', keys: ['default', 'is_default'], format: 'boolean', width: 90 },
    { title: '수정 시각', keys: ['updated_at'], format: 'date', width: 180 },
  ]} />
}

export function IncidentsPage() {
  return <ResourceListPage title="인시던트" description="Hub, Pod, GPU와 인증 장애를 묶어 영향과 복구 상태를 추적합니다." endpoint="/incidents" createLabel="인시던트 등록" emptyDescription="등록된 인시던트가 없습니다." fields={[
    { name: 'title', label: '제목', required: true }, { name: 'severity', label: '심각도', type: 'select', required: true, options: [{ label: '심각', value: 'critical' }, { label: '주의', value: 'warning' }, { label: '정보', value: 'info' }] },
    { name: 'hub_id', label: 'Hub ID' }, { name: 'description', label: '상세 내용', type: 'textarea', required: true }, { name: 'status', label: '상태', type: 'select', options: [{ label: '진행 중', value: 'open' }, { label: '해결', value: 'resolved' }] },
  ]} columns={[
    { title: '번호', keys: ['incident_no', 'id'], width: 150 }, { title: '심각도', keys: ['severity'], format: 'status', width: 100 }, { title: '제목', keys: ['title'], width: 260 },
    { title: 'Hub', keys: ['hub_name', 'hub'], width: 140 }, { title: '영향 사용자', keys: ['affected_users'], format: 'number', suffix: '명', width: 110 },
    { title: '상태', keys: ['status'], format: 'status', width: 100 }, { title: '발생 시각', keys: ['started_at', 'created_at'], format: 'date', width: 180 }, { title: '복구 시각', keys: ['resolved_at'], format: 'date', width: 180 },
  ]} />
}

export function AuditPage() {
  return <ResourceListPage title="감사 로그" description="사용자·관리자·API 작업의 대상, 변경 전후와 결과를 추적합니다." endpoint="/audit" emptyDescription="감사 로그가 없습니다." columns={[
    { title: '시각', keys: ['created_at', 'timestamp'], format: 'date', width: 180 }, { title: '행위자', keys: ['actor_name', 'username', 'actor'], width: 140 },
    { title: '작업', keys: ['action'], width: 170 }, { title: '대상 유형', keys: ['resource_type', 'target_type'], width: 120 }, { title: '대상', keys: ['resource_name', 'target'], width: 190 },
    { title: 'Hub', keys: ['hub_name'], width: 130 }, { title: 'IP', keys: ['ip_address', 'ip'], width: 130 }, { title: '결과', keys: ['result', 'status'], format: 'status', width: 100 },
  ]} />
}

export function CostsPage() {
  const { features } = useAuth()
  const columns: ResourceColumn[] = [
    { title: '집계 대상', keys: ['name', 'project_name', 'department'], width: 180 }, { title: '구분', keys: ['type', 'group_type'], width: 110 },
    { title: 'CPU 시간', keys: ['cpu_hours'], format: 'number', suffix: 'h', width: 110 }, { title: 'RAM 시간', keys: ['memory_gb_hours'], format: 'number', suffix: 'GBh', width: 120 },
    ...(features.gpuMonitoring ? [{ title: 'GPU 시간', keys: ['gpu_hours'], format: 'number' as const, suffix: 'h', width: 110 }] : []),
    { title: 'Storage', keys: ['storage_gb_month'], format: 'number', suffix: 'GB·월', width: 120 }, { title: '추정 비용', keys: ['estimated_cost', 'cost'], format: 'number', suffix: '원', width: 140 },
    { title: '예산 사용률', keys: ['budget_usage'], format: 'percent', width: 120 }, { title: '기간', keys: ['period'], width: 120 },
  ]
  return <ResourceListPage title="비용·용량" description="부서·프로젝트·망별 자원 소비를 내부 단가로 환산하고 포화 시점을 분석합니다." endpoint="/costs" emptyDescription="집계된 비용 데이터가 없습니다." columns={columns} />
}

export function NotificationsPage() {
  return <ResourceListPage title="알림 센터" description="운영 이벤트를 확인하고 알림 규칙과 전달 상태를 관리합니다." endpoint="/notifications" createLabel="알림 규칙 등록" emptyDescription="표시할 알림이 없습니다." fields={[
    { name: 'name', label: '규칙 이름', required: true }, { name: 'event_type', label: '이벤트 유형', required: true },
    { name: 'severity', label: '최소 심각도', type: 'select', options: [{ label: '정보', value: 'info' }, { label: '주의', value: 'warning' }, { label: '심각', value: 'critical' }] },
    { name: 'channel', label: '채널', type: 'select', options: [{ label: '포털', value: 'portal' }, { label: 'Webhook', value: 'webhook' }, { label: '이메일', value: 'email' }] },
    { name: 'enabled', label: '규칙 사용', type: 'switch', initialValue: true },
  ]} columns={[
    { title: '시각', keys: ['created_at'], format: 'date', width: 180 }, { title: '심각도', keys: ['severity'], format: 'status', width: 100 },
    { title: '제목 / 규칙', keys: ['title', 'name'], width: 220 }, { title: '내용', keys: ['message', 'description'], width: 300 }, { title: '채널', keys: ['channel'], width: 100 },
    { title: '전달 상태', keys: ['delivery_status', 'status'], format: 'status', width: 110 },
  ]} />
}

export function StatusCell({ value }: { value: unknown }) {
  return <Tag color={statusTone(value)}>{asText(value)}</Tag>
}
