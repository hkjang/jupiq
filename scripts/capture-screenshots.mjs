#!/usr/bin/env node

import { readFile, writeFile, mkdir } from 'node:fs/promises'
import { basename, dirname, resolve } from 'node:path'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)

let chromium
try {
  ;({ chromium } = require('playwright'))
} catch {
  console.error('Playwright 패키지를 찾지 못했습니다. scripts/capture-screenshots.sh를 사용해 주세요.')
  process.exit(2)
}

const defaultManifest = resolve('docs/assets/screenshots/manifest.json')
const defaultOutput = resolve('docs/assets/screenshots')

function usage() {
  console.log(`jupiq 화면 캡처

사용법:
  node scripts/capture-screenshots.mjs [옵션]

옵션:
  --base-url URL          캡처할 jupiq 주소 (기본: http://127.0.0.1:8080)
  --username USER         데모 관리자 ID
  --password PASSWORD     데모 관리자 비밀번호 (프로세스 목록 노출에 주의)
  --manifest PATH         캡처 manifest 경로
  --output PATH           WebP 출력 디렉터리
  --only FILES            쉼표로 구분한 파일명만 캡처
  --allow-nonlocal        localhost가 아닌 주소의 캡처 허용
  --ignore-https-errors   데모 인증서 오류 무시
  --no-restore-settings   캡처 후 기능 설정 원복 생략
  --help                  도움말

비밀번호는 --password보다 JUPIQ_CAPTURE_PASSWORD 환경변수 사용을 권장합니다.`)
}

function parseArgs(argv) {
  const options = {
    baseUrl: process.env.JUPIQ_CAPTURE_BASE_URL || 'http://127.0.0.1:8080',
    username: process.env.JUPIQ_CAPTURE_USERNAME || 'admin',
    password: process.env.JUPIQ_CAPTURE_PASSWORD || '',
    manifest: process.env.JUPIQ_CAPTURE_MANIFEST || defaultManifest,
    output: process.env.JUPIQ_CAPTURE_OUTPUT || defaultOutput,
    allowNonlocal: false,
    ignoreHTTPSErrors: false,
    restoreSettings: true,
    only: null,
  }
  const valueOptions = new Map([
    ['--base-url', 'baseUrl'], ['--username', 'username'], ['--password', 'password'],
    ['--manifest', 'manifest'], ['--output', 'output'], ['--only', 'only'],
  ])
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index]
    if (argument === '--help') return { ...options, help: true }
    if (argument === '--allow-nonlocal') { options.allowNonlocal = true; continue }
    if (argument === '--ignore-https-errors') { options.ignoreHTTPSErrors = true; continue }
    if (argument === '--no-restore-settings') { options.restoreSettings = false; continue }
    const key = valueOptions.get(argument)
    if (!key || index + 1 >= argv.length) throw new Error(`알 수 없거나 값이 없는 옵션: ${argument}`)
    options[key] = argv[index + 1]
    index += 1
  }
  options.baseUrl = options.baseUrl.replace(/\/$/, '')
  options.manifest = resolve(options.manifest)
  options.output = resolve(options.output)
  if (typeof options.only === 'string') options.only = new Set(options.only.split(',').map((item) => item.trim()).filter(Boolean))
  return options
}

function assertSafeTarget(options) {
  const target = new URL(options.baseUrl)
  if (!['http:', 'https:'].includes(target.protocol)) throw new Error('base URL은 http 또는 https여야 합니다.')
  const localHosts = new Set(['127.0.0.1', 'localhost', '::1', '[::1]', 'host.docker.internal'])
  if (!options.allowNonlocal && !localHosts.has(target.hostname)) {
    throw new Error('실제 운영 화면의 우발적 캡처를 막았습니다. 안전한 데모 환경이면 --allow-nonlocal을 명시하세요.')
  }
}

function validateEntry(entry) {
  if (!entry || typeof entry !== 'object') throw new Error('manifest 항목이 객체가 아닙니다.')
  if (typeof entry.path !== 'string' || !entry.path.startsWith('/')) throw new Error(`잘못된 path: ${entry.path}`)
  if (typeof entry.file !== 'string' || basename(entry.file) !== entry.file || !entry.file.endsWith('.webp')) {
    throw new Error(`안전하지 않은 출력 파일명: ${entry.file}`)
  }
  if (!['desktop', 'mobile'].includes(entry.viewport)) throw new Error(`지원하지 않는 viewport: ${entry.viewport}`)
}

async function api(page, path, init = {}) {
  return page.evaluate(async ({ path, init }) => {
    const response = await fetch(`/api/v1${path}`, {
      ...init,
      credentials: 'include',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(init.headers || {}) },
    })
    const body = await response.json().catch(() => null)
    if (!response.ok) throw new Error(body?.error?.message || body?.message || `HTTP ${response.status}`)
    return body?.data ?? body
  }, { path, init })
}

async function login(page, options) {
  if (!options.password) throw new Error('인증 화면 캡처에는 JUPIQ_CAPTURE_PASSWORD가 필요합니다.')
  await page.getByLabel('사용자 ID').fill(options.username)
  await page.getByLabel('비밀번호').fill(options.password)
  await Promise.all([
    page.waitForURL((url) => url.pathname !== '/login', { timeout: 20_000 }),
    page.getByRole('button', { name: '로그인', exact: true }).click(),
  ])
  await page.getByRole('button', { name: '사용자 메뉴 열기' }).waitFor({ timeout: 20_000 })
}

async function readScreenshotFeatureState(page) {
  const response = await api(page, '/settings')
  const settings = response?.settings || response || {}
  return {
    features: structuredClone(settings.features || {}),
    workflow: structuredClone(settings.workflow || {}),
  }
}

async function configureScreenshotFeatures(page, original, entry) {
  const gpuOn = new Set([
    'integrated-dashboard.webp', 'gpu-efficiency.webp', 'costs.webp',
    'admin-settings.webp', 'admin-settings-mobile.webp', 'profile-menu.webp',
  ]).has(entry.file)
  const llmOn = new Set([
    'integrated-dashboard.webp', 'ai-copilot.webp',
    'admin-settings.webp', 'admin-settings-mobile.webp', 'profile-menu.webp',
  ]).has(entry.file)
  const approvalOn = new Set([
    'integrated-dashboard.webp', 'approvals.webp',
    'admin-settings.webp', 'admin-settings-mobile.webp', 'profile-menu.webp',
  ]).has(entry.file)
  await api(page, '/settings', {
    method: 'PUT',
    body: JSON.stringify({
      features: { ...original.features, gpu_monitoring: gpuOn, llm_usage_monitoring: llmOn },
      workflow: { ...original.workflow, approval_enabled: approvalOn },
    }),
  })
}

async function restoreScreenshotFeatures(page, original) {
  if (!original) return
  await api(page, '/settings', { method: 'PUT', body: JSON.stringify(original) })
}

function demoDashboard() {
  const gib = 1024 ** 3
  return {
    sampled_at: '2026-09-02T10:30:00+09:00',
    stale: false,
    summary: { active_users: 3, running_servers: 3, cpu_cores: 7.8, memory_bytes: 42 * gib, idle_sessions: 1, long_running_sessions: 1, gpu_count: 0 },
    hubs: [
      { id: 1, name: '업무망 Hub', network: '업무망', status: 'online', active_users: 2, running_servers: 2, last_success_at: '2026-09-02T10:29:50+09:00', stale: false },
      { id: 2, name: '개발망 Hub', network: '개발망', status: 'online', active_users: 1, running_servers: 1, last_success_at: '2026-09-02T10:29:46+09:00', stale: false },
      { id: 3, name: '분석망 Hub', network: '분석망', status: 'online', active_users: 0, running_servers: 0, last_success_at: '2026-09-02T10:29:42+09:00', stale: false },
    ],
    live_users: [
      { id: 1, server_id: 101, hub_id: 1, username: 'user01', display_name: '김하늘', network: '업무망', hub_name: '업무망 Hub', department: 'AI 플랫폼팀', project_name: 'RAG 고도화', runtime_seconds: 11220, cpu_cores: 3.2, memory_bytes: 18 * gib, status: 'running', stale: false, resource_stale: false, data_freshness: '2026-09-02T10:29:50+09:00', resource_sampled_at: '2026-09-02T10:29:40+09:00' },
      { id: 2, server_id: 102, hub_id: 1, username: 'user02', display_name: '이도윤', network: '업무망', hub_name: '업무망 Hub', department: '데이터 분석팀', project_name: '수요 예측', runtime_seconds: 6840, cpu_cores: 2.8, memory_bytes: 15 * gib, status: 'running', stale: false, resource_stale: false, data_freshness: '2026-09-02T10:29:50+09:00', resource_sampled_at: '2026-09-02T10:29:40+09:00' },
      { id: 3, server_id: 201, hub_id: 2, username: 'user03', display_name: '박서연', network: '개발망', hub_name: '개발망 Hub', department: 'AI 연구팀', project_name: '문서 분류', runtime_seconds: 3960, cpu_cores: 1.8, memory_bytes: 9 * gib, status: 'running', stale: false, resource_stale: false, data_freshness: '2026-09-02T10:29:46+09:00', resource_sampled_at: '2026-09-02T10:29:38+09:00' },
    ],
    usage_trend: [
      { label: '04시', active_users: 1, running_servers: 2, cpu: 18 },
      { label: '08시', active_users: 4, running_servers: 5, cpu: 34 },
      { label: '12시', active_users: 8, running_servers: 10, cpu: 62 },
      { label: '16시', active_users: 6, running_servers: 7, cpu: 49 },
      { label: '20시', active_users: 3, running_servers: 4, cpu: 28 },
    ],
    top_users: [
      { username: 'user01', runtime_seconds: 27600 },
      { username: 'user02', runtime_seconds: 21400 },
      { username: 'user03', runtime_seconds: 16800 },
    ],
    gpu_waste: [],
  }
}

function demoUsage() {
  const buckets = ['2026-09-02T04:00:00+09:00', '2026-09-02T08:00:00+09:00', '2026-09-02T12:00:00+09:00', '2026-09-02T16:00:00+09:00', '2026-09-02T20:00:00+09:00']
  const login = [3, 12, 24, 17, 8]
  const starts = [2, 8, 15, 11, 5]
  const cpu = [1.8, 4.2, 8.6, 6.9, 3.1]
  const trend = []
  buckets.forEach((bucket, index) => {
    trend.push({ bucket, group: 'demo', metric: 'login_count', average: login[index] })
    trend.push({ bucket, group: 'demo', metric: 'server_starts', average: starts[index] })
    trend.push({ bucket, group: 'demo', metric: 'cpu_cores', average: cpu[index] })
  })
  return {
    dau: 48, wau: 132, mau: 276, trend, stale: false,
    top_users: [
      { username: 'user01', runtime_seconds: 27600 },
      { username: 'user02', runtime_seconds: 21400 },
      { username: 'user03', runtime_seconds: 16800 },
    ],
  }
}

function demoLists() {
  const at = '2026-09-02T10:28:00+09:00'
  return {
    hubs: [
      { id: 1, name: '업무망 Hub', network: '업무망', base_url: 'https://jupyter-work.example.invalid', status: 'online', version: '5.3.0', user_count: 128, running_servers: 24, last_success_at: at },
      { id: 2, name: '개발망 Hub', network: '개발망', base_url: 'https://jupyter-dev.example.invalid', status: 'online', version: '5.3.0', user_count: 84, running_servers: 17, last_success_at: at },
      { id: 3, name: '분석망 Hub', network: '분석망', base_url: 'https://jupyter-data.example.invalid', status: 'degraded', version: '5.2.1', user_count: 46, running_servers: 9, last_success_at: '2026-09-02T10:24:00+09:00' },
    ],
    users: [
      { id: 1, username: 'user01', display_name: '김하늘', department: 'AI 플랫폼팀', hub_name: '업무망 Hub', roles: ['user', 'project-owner'], server_status: 'running', server_count: 2, running_server_count: 2, runtime_seconds: 11220, cpu_cores: 3.2, memory_bytes: 18 * 1024 ** 3, last_activity_at: at },
      { id: 2, username: 'user02', display_name: '이도윤', department: '데이터 분석팀', hub_name: '업무망 Hub', roles: ['user'], server_status: 'running', server_count: 1, running_server_count: 1, runtime_seconds: 6840, cpu_cores: 2.8, memory_bytes: 15 * 1024 ** 3, last_activity_at: at },
      { id: 3, username: 'user03', display_name: '박서연', department: 'AI 연구팀', hub_name: '개발망 Hub', roles: ['user'], server_status: 'stopped', server_count: 1, running_server_count: 0, runtime_seconds: 0, cpu_cores: null, memory_bytes: null, last_activity_at: '2026-09-02T09:51:00+09:00' },
    ],
    servers: [
      { id: 1, username: 'user01', hub_name: '업무망 Hub', status: 'running', profile_name: 'CPU Medium', image: 'jupyter/datascience:v25.09', node_name: 'worker-a1', pod_name: 'jupyter-user01', runtime: '3시간 7분', cpu_usage: 44, memory_usage: 57, started_at: '2026-09-02T07:21:00+09:00' },
      { id: 2, username: 'user02', hub_name: '업무망 Hub', status: 'running', profile_name: 'CPU Large', image: 'jupyter/pytorch:v25.09', node_name: 'worker-a2', pod_name: 'jupyter-user02', runtime: '1시간 54분', cpu_usage: 31, memory_usage: 46, started_at: '2026-09-02T08:34:00+09:00' },
      { id: 3, username: 'user03', hub_name: '개발망 Hub', status: 'stopped', profile_name: 'CPU Small', image: 'jupyter/base:v25.09', node_name: '—', pod_name: '—', runtime: '—', cpu_usage: 0, memory_usage: 0 },
    ],
    gpus: [
      { id: 1, hub: '개발망 Hub', network: '개발망', node_name: 'gpu-worker-01', gpu_count: 1, gpu_utilization: 72, vram_bytes: 48 * 1024 ** 3, username: 'demo-researcher', pod_name: 'jupyter-demo-researcher', sampled_at: at, stale: false },
      { id: 2, hub: '개발망 Hub', network: '개발망', node_name: 'gpu-worker-01', gpu_count: 1, gpu_utilization: 4, vram_bytes: 14 * 1024 ** 3, username: 'demo-analyst', pod_name: 'jupyter-demo-analyst', sampled_at: at, stale: false },
    ],
    projects: [
      { id: 1, name: 'RAG 고도화', owner: 'user01', member_count: 12, hubs: ['업무망', '개발망'], cpu_quota: 64, memory_quota_gb: 256, gpu_quota: 0, status: 'active', end_date: '2026-12-31' },
      { id: 2, name: '수요 예측', owner: 'user02', member_count: 8, hubs: ['업무망'], cpu_quota: 32, memory_quota_gb: 128, gpu_quota: 0, status: 'active', end_date: '2026-11-30' },
    ],
    policies: [
      { id: 1, name: '기본 사용자 정책', target_type: '전체', target: '모든 사용자', idle_timeout_minutes: 120, max_runtime_minutes: 480, enabled: true, updated_at: at },
      { id: 2, name: '장시간 분석 정책', target_type: '그룹', target: 'AI 연구팀', idle_timeout_minutes: 240, max_runtime_minutes: 1440, enabled: true, updated_at: at },
    ],
    profiles: [
      { id: 1, name: 'CPU Small', description: '가벼운 분석과 교육', cpu_limit: 2, memory_gb: 8, gpu_count: 0, storage_gb: 20, image_name: 'Base Python', enabled: true },
      { id: 2, name: 'CPU Medium', description: '일반 데이터 분석', cpu_limit: 4, memory_gb: 16, gpu_count: 0, storage_gb: 50, image_name: 'Data Science', enabled: true },
      { id: 3, name: 'CPU Large', description: '대용량 CPU 분석', cpu_limit: 8, memory_gb: 32, gpu_count: 0, storage_gb: 100, image_name: 'PyTorch CPU', enabled: true },
    ],
    images: [
      { id: 1, name: 'Data Science', image: 'registry.example.invalid/jupyter/datascience', version: 'v25.09', lifecycle: 'production', default: true, updated_at: at },
      { id: 2, name: 'PyTorch CPU', image: 'registry.example.invalid/jupyter/pytorch-cpu', version: 'v25.09', lifecycle: 'approved', default: false, updated_at: at },
    ],
    approvals: [
      { id: 1, request_no: 'REQ-20260902-014', requester_name: 'user01', request_type: 'server_action', action: 'stop', server_id: 18, status: 'pending', created_at: '2026-09-02T09:42:00+09:00' },
      { id: 2, request_no: 'REQ-20260902-013', requester_name: 'user03', request_type: 'server_action', action: 'restart', server_id: 27, status: 'pending_review', created_at: '2026-09-02T09:15:00+09:00' },
    ],
    incidents: [
      { id: 1, incident_no: 'INC-20260902-001', severity: 'warning', title: '분석망 Hub 수집 지연', hub_name: '분석망 Hub', affected_users: 3, status: 'investigating', started_at: '2026-09-02T10:20:00+09:00' },
      { id: 2, incident_no: 'INC-20260901-004', severity: 'info', title: '개발망 이미지 Pull 지연', hub_name: '개발망 Hub', affected_users: 2, status: 'resolved', started_at: '2026-09-01T16:12:00+09:00', resolved_at: '2026-09-01T16:31:00+09:00' },
    ],
    audit: [
      { id: 1, created_at: at, actor_name: 'admin', action: 'hub.connection.test', resource_type: 'hub', resource_name: '업무망 Hub', hub_name: '업무망 Hub', ip_address: '127.0.0.1', result: 'success' },
      { id: 2, created_at: '2026-09-02T10:12:00+09:00', actor_name: 'admin', action: 'settings.update', resource_type: 'settings', resource_name: '선택 기능', ip_address: '127.0.0.1', result: 'success' },
    ],
    costs: [
      { id: 1, name: 'AI 플랫폼팀', type: '부서', cpu_hours: 824, memory_gb_hours: 3510, gpu_hours: 0, storage_gb_month: 420, estimated_cost: 1280000, budget_usage: 64, period: '2026-09' },
      { id: 2, name: '데이터 분석팀', type: '부서', cpu_hours: 612, memory_gb_hours: 2480, gpu_hours: 0, storage_gb_month: 310, estimated_cost: 930000, budget_usage: 47, period: '2026-09' },
    ],
    notifications: [
      { id: 1, created_at: at, severity: 'warning', title: '수집 지연 점검 규칙', message: '분석망 Hub 수집 지연 시 운영자가 확인할 수 있도록 등록한 규칙입니다.', channel: 'portal', delivery_status: 'registered' },
      { id: 2, created_at: '2026-09-02T09:46:00+09:00', severity: 'info', title: '승인 대기 점검 규칙', message: '서버 작업 승인 대기 건을 확인하기 위한 수동 운영 규칙입니다.', channel: 'portal', delivery_status: 'registered' },
    ],
    keys: [
      { id: 1, name: '운영 대시보드 조회', prefix: 'jqk_demo_7f2a', permissions: ['dashboard:read'], status: 'active', last_used_at: at, expires_at: '2027-03-01T00:00:00+09:00' },
      { id: 2, name: '개인 자동화', prefix: 'jqk_demo_19bc', permissions: ['profile:read'], status: 'active', last_used_at: '2026-09-01T18:20:00+09:00', expires_at: '2026-12-31T00:00:00+09:00' },
    ],
  }
}

function demoUserDetail(username = 'user01') {
  const gib = 1024 ** 3
  return {
    username,
    user: {
      username, display_name: '김하늘', email: 'user01@example.invalid', department: 'AI 플랫폼팀',
      auth_source: 'keycloak', roles: ['User', 'Project Owner'], active: true,
      last_login_at: '2026-09-02T09:01:00+09:00', last_activity_at: '2026-09-02T10:28:00+09:00',
    },
    hubs: [
      { hub_id: 1, hub_name: '업무망 Hub', network: '업무망', admin: false, active: true, last_activity_at: '2026-09-02T10:28:00+09:00', synced_at: '2026-09-02T10:29:50+09:00' },
      { hub_id: 2, hub_name: '개발망 Hub', network: '개발망', admin: false, active: true, last_activity_at: '2026-09-01T17:42:00+09:00', synced_at: '2026-09-02T10:29:46+09:00' },
    ],
    servers: {
      current: [{ id: 1, hub_name: '업무망 Hub', server_name: '기본 서버', status: 'running', runtime_seconds: 11220, cpu_cores: 3.2, memory_bytes: 18 * gib, gpu_count: 0, node_name: 'worker-a1', pod_name: 'jupyter-user01', image: 'jupyter/datascience:v25.09', last_activity_at: '2026-09-02T10:28:00+09:00' }],
      history: [{ id: 2, hub_name: '개발망 Hub', server_name: '분석 서버', status: 'stopped', runtime_seconds: 6840, cpu_cores: 2, memory_bytes: 8 * gib, gpu_count: 0, node_name: 'worker-d2', pod_name: 'jupyter-user01-analysis', image: 'jupyter/base:v25.09', last_activity_at: '2026-09-01T17:42:00+09:00' }],
      meta: { page: 1, limit: 50, total: 2 },
    },
    timeline: {
      items: [
        { action: 'server.start', title: 'Notebook 서버 시작', description: '업무망 Hub 기본 서버를 시작했습니다.', result: 'success', created_at: '2026-09-02T07:21:00+09:00' },
        { action: 'auth.login', title: 'Keycloak 로그인', description: 'SSO 로그인이 완료되었습니다.', result: 'success', created_at: '2026-09-02T09:01:00+09:00' },
      ],
      meta: { page: 1, limit: 50, total: 2 },
    },
    usage: {
      day: { login_count: 1, server_start_count: 1, runtime_seconds: 11220, cpu_average: 2.6, cpu_peak: 3.8, memory_average: 16 * gib, memory_peak: 20 * gib },
      week: { login_count: 5, server_start_count: 6, runtime_seconds: 68400, cpu_average: 2.3, cpu_peak: 4, memory_average: 14 * gib, memory_peak: 22 * gib },
      month: { login_count: 18, server_start_count: 21, runtime_seconds: 276000, cpu_average: 2.1, cpu_peak: 4, memory_average: 13 * gib, memory_peak: 24 * gib },
    },
    llm_usage: { included: false },
    feature_enabled: { gpu_monitoring: false, llm_usage_monitoring: false },
    data_policy: { metadata_only: true, notebook_content_collected: false, llm_content_collected: false },
  }
}

async function installEmptyListFallbacks(page) {
  const fixtures = demoLists()
  for (const [endpoint, rows] of Object.entries(fixtures)) {
    const matcher = new RegExp(`/api/v1/${endpoint}(?:\\?.*)?$`)
    await page.route(matcher, async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      if (!response.ok()) return route.fulfill({ response })
      const body = await response.json().catch(() => null)
      const current = Array.isArray(body?.data) ? body.data : Array.isArray(body) ? body : null
      if (current === null || current.length > 0) return route.fulfill({ response })
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: rows, meta: { page: 1, page_size: 20, total: rows.length } }),
      })
    })
  }
  await page.route(/\/api\/v1\/users\/([^/?]+)(?:\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    if (response.ok()) {
      const body = await response.json().catch(() => null)
      const detail = body?.data ?? body
      if (detail && typeof detail === 'object' && (detail.user || detail.hubs?.length)) return route.fulfill({ response })
    }
    const match = new URL(route.request().url()).pathname.match(/\/users\/([^/]+)$/)
    const username = match ? decodeURIComponent(match[1]) : 'user01'
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: demoUserDetail(username) }) })
  })
  await page.route(/\/api\/v1\/search(?:\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    if (response.ok()) {
      const body = await response.json().catch(() => null)
      const current = body?.data?.items ?? body?.items
      if (Array.isArray(current) && current.length > 0) return route.fulfill({ response })
    }
    const items = [
      { type: 'user', id: 'user01', title: '김하늘', subtitle: 'user01 · AI 플랫폼팀', path: '/users/user01' },
      { type: 'server', id: '1', title: 'user01', subtitle: '업무망 Hub · jupyter-user01 · running', path: '/servers?search=user01' },
      { type: 'project', id: '1', title: 'RAG 고도화', subtitle: 'active', path: '/projects?search=RAG%20고도화' },
    ]
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: { query: 'user01', items, total: items.length } }) })
  })
}

async function dashboardPayload(context, options, mode, range = 'day') {
  let payload
  try {
    const response = await context.request.get(`${options.baseUrl}/api/v1/dashboard?range=${encodeURIComponent(range)}`)
    if (response.ok()) {
      const json = await response.json()
      payload = structuredClone(json?.data ?? json)
    }
  } catch { /* 고정 데모 데이터로 대체 */ }
  if (!payload || typeof payload !== 'object' || !Array.isArray(payload.live_users) || !payload.live_users.length) payload = demoDashboard()
  payload.stale = mode === 'stale'
  if (Array.isArray(payload.hubs)) {
    payload.hubs = payload.hubs.map((hub, index) => ({
      ...hub,
      status: mode === 'stale' ? (index === payload.hubs.length - 1 ? 'degraded' : 'healthy') : 'healthy',
      stale: mode === 'stale' && index === payload.hubs.length - 1,
      ...(mode === 'stale' ? { last_success_at: index === payload.hubs.length - 1 ? '2026-09-02T08:10:00+09:00' : '2026-09-02T10:20:00+09:00' } : {}),
    }))
  }
  return payload
}

async function waitForPage(page) {
  await page.waitForLoadState('domcontentloaded')
  await page.locator('#main-content').waitFor({ state: 'visible', timeout: 20_000 }).catch(() => {})
  await page.locator('.ant-skeleton').first().waitFor({ state: 'hidden', timeout: 15_000 }).catch(() => {})
  await page.evaluate(async () => { if (document.fonts?.ready) await document.fonts.ready })
  await page.addStyleTag({ content: '*,*::before,*::after{animation-duration:0s!important;transition-duration:0s!important;caret-color:transparent!important}' })
  await page.waitForTimeout(700)
}

async function applyCondition(page, entry) {
  if (entry.file === 'usage-drilldown.webp') {
    await page.getByText('월', { exact: true }).first().click().catch(() => {})
  }
  if (entry.file === 'hub-management.webp') {
    const action = page.getByRole('button', { name: '작업 메뉴 열기' }).first()
    if (await action.count()) {
      await action.click()
      await page.getByText('연결 테스트', { exact: true }).last().click().catch(() => {})
      await page.getByText('연결 테스트 작업을 완료했습니다.').waitFor({ timeout: 5_000 }).catch(() => {})
    }
  }
  if (entry.file === 'ai-copilot.webp') {
    await page.getByRole('button', { name: /JupyterHub 운영 점검 항목/ }).click().catch(() => {})
    await page.getByText(/Hub별 API 상태와 마지막 성공 시각/).waitFor({ timeout: 8_000 }).catch(() => {})
  }
  if (entry.file === 'admin-settings.webp' || entry.file === 'admin-settings-mobile.webp') {
    await page.getByRole('tab', { name: '외부 연동' }).click().catch(() => {})
  }
  if (entry.file === 'profile-menu.webp') {
    await page.getByRole('button', { name: '사용자 메뉴 열기' }).click()
    await page.getByText('서비스 버전', { exact: true }).waitFor()
  }
}

async function writeWebP(page, path) {
  await page.evaluate(() => {
    window.scrollTo(0, 0)
    for (const input of document.querySelectorAll('input[type="password"]')) input.value = ''
    if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  })
  const client = await page.context().newCDPSession(page)
  try {
    const result = await client.send('Page.captureScreenshot', {
      format: 'webp', quality: 82, fromSurface: true, captureBeyondViewport: false,
    })
    const bytes = Buffer.from(result.data, 'base64')
    if (bytes.subarray(0, 4).toString('ascii') !== 'RIFF' || bytes.subarray(8, 12).toString('ascii') !== 'WEBP') {
      throw new Error('Chromium이 유효한 WebP를 반환하지 않았습니다.')
    }
    await mkdir(dirname(path), { recursive: true })
    await writeFile(path, bytes)
  } finally {
    await client.detach()
  }
}

async function main() {
  let options
  try { options = parseArgs(process.argv.slice(2)) } catch (error) { console.error(error.message); usage(); process.exit(2) }
  if (options.help) { usage(); return }
  assertSafeTarget(options)

  const manifest = JSON.parse(await readFile(options.manifest, 'utf8'))
  if (!Array.isArray(manifest) || !manifest.length) throw new Error('manifest가 비어 있거나 배열이 아닙니다.')
  manifest.forEach(validateEntry)
  const entries = options.only ? manifest.filter((entry) => options.only.has(entry.file)) : manifest
  if (!entries.length) throw new Error('--only와 일치하는 manifest 항목이 없습니다.')
  const needsAuth = entries.some((entry) => entry.path !== '/login')

  const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] })
  const context = await browser.newContext({
    viewport: { width: 1600, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'ko-KR', timezoneId: 'Asia/Seoul', colorScheme: 'light',
    reducedMotion: 'reduce', ignoreHTTPSErrors: options.ignoreHTTPSErrors,
  })
  // 캡처 중에는 한 번의 dashboard snapshot을 EventSource 메시지처럼 전달하고
  // 연결을 유지해 Fresh 화면의 "실시간" 상태를 결정적으로 재현한다.
  await context.addInitScript(() => {
    if (typeof globalThis.crypto?.randomUUID !== 'function') {
      Object.defineProperty(globalThis.crypto, 'randomUUID', {
        value: () => {
          const bytes = new Uint8Array(16)
          globalThis.crypto.getRandomValues(bytes)
          bytes[6] = (bytes[6] & 0x0f) | 0x40
          bytes[8] = (bytes[8] & 0x3f) | 0x80
          const hex = [...bytes].map((value) => value.toString(16).padStart(2, '0')).join('')
          return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
        },
      })
    }
    const NativeEventSource = window.EventSource
    class CaptureEventSource extends EventTarget {
      static CONNECTING = 0
      static OPEN = 1
      static CLOSED = 2
      CONNECTING = 0
      OPEN = 1
      CLOSED = 2
      readyState = 0
      withCredentials = true
      onopen = null
      onmessage = null
      onerror = null
      constructor(url) {
        super()
        this.url = String(url)
        if (!this.url.includes('/api/v1/dashboard/live')) return new NativeEventSource(url, { withCredentials: true })
        window.setTimeout(async () => {
          try {
            const response = await fetch(this.url.replace('/dashboard/live', '/dashboard'), { credentials: 'include' })
            if (!response.ok) throw new Error(`HTTP ${response.status}`)
            const body = await response.json()
            this.readyState = 1
            const openEvent = new Event('open')
            this.onopen?.(openEvent)
            this.dispatchEvent(openEvent)
            const messageEvent = new MessageEvent('message', { data: JSON.stringify(body) })
            this.onmessage?.(messageEvent)
            this.dispatchEvent(messageEvent)
          } catch {
            this.readyState = 2
            const errorEvent = new Event('error')
            this.onerror?.(errorEvent)
            this.dispatchEvent(errorEvent)
          }
        }, 25)
      }
      close() { this.readyState = 2 }
    }
    window.EventSource = CaptureEventSource
  })
  const page = await context.newPage()
  let dashboardMock = null
  let originalSettings = null
  const pageErrors = []
  const consoleErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })

  await installEmptyListFallbacks(page)

  await page.route(/\/api\/v1\/dashboard(?:\/live)?(?:\?.*)?$/, async (route) => {
    if (!dashboardMock) return route.continue()
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/live')) {
      return route.fulfill({ status: 200, contentType: 'text/event-stream', body: `data: ${JSON.stringify(dashboardMock)}\n\n` })
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: dashboardMock }) })
  })
  await page.route(/\/api\/v1\/usage(?:\?.*)?$/, async (route) => {
    if (!dashboardMock) return route.continue()
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: demoUsage() }) })
  })
  await page.route(/\/api\/v1\/hubs\/[^/]+\/test$/, (route) => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({ data: { success: true, version: '5.3.0', latency_ms: 28 } }),
  }))
  await page.route('**/api/v1/ai/chat', (route) => route.fulfill({
    status: 200,
    headers: { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' },
    body: [
      'data: {"choices":[{"delta":{"content":"Hub별 API 상태와 마지막 성공 시각, 실행 서버 수를 먼저 확인하세요. "}}]}',
      'data: {"choices":[{"delta":{"content":"이후 stale 세션과 최근 오류를 점검하면 운영 우선순위를 정할 수 있습니다."}}]}',
      'data: [DONE]', '',
    ].join('\n\n'),
  }))

  try {
    const loginEntry = entries.find((entry) => entry.path === '/login')
    if (loginEntry) {
      await page.setViewportSize({ width: 1600, height: 1000 })
      await page.goto(`${options.baseUrl}/login`, { waitUntil: 'domcontentloaded' })
      await page.getByRole('heading', { name: 'jupiq에 로그인' }).waitFor({ timeout: 20_000 })
      await page.getByLabel('사용자 ID').fill('')
      await page.getByLabel('비밀번호').fill('')
      await page.addStyleTag({ content: '.login-story h1.ant-typography{word-break:keep-all;text-wrap:balance}' })
      await page.evaluate(async () => { if (document.fonts?.ready) await document.fonts.ready })
      await writeWebP(page, resolve(options.output, loginEntry.file))
      const unexpectedLoginErrors = consoleErrors.filter((message) => !/status of 401 \(Unauthorized\)/.test(message))
      if (pageErrors.length || unexpectedLoginErrors.length) {
        throw new Error(`${loginEntry.file} 렌더링 오류: ${[...pageErrors, ...unexpectedLoginErrors].join('; ')}`)
      }
      // AuthProvider intentionally probes /auth/me before login. Chromium logs
      // that expected 401 as a resource error even though the UI handles it.
      // Clear only the already-vetted login-page diagnostics so authenticated
      // pages continue to fail on every console or rendering error.
      pageErrors.length = 0
      consoleErrors.length = 0
      console.log(`✓ ${loginEntry.file}`)
    }

    if (needsAuth) {
      if (page.url() !== `${options.baseUrl}/login`) await page.goto(`${options.baseUrl}/login`, { waitUntil: 'domcontentloaded' })
      await login(page, options)
      originalSettings = await readScreenshotFeatureState(page)
      pageErrors.length = 0
      consoleErrors.length = 0
    }

    for (const entry of entries) {
      if (entry.path === '/login') continue
      await configureScreenshotFeatures(page, originalSettings, entry)
      await page.setViewportSize(entry.viewport === 'mobile' ? { width: 390, height: 844 } : { width: 1600, height: 1000 })
      const range = entry.file === 'usage-drilldown.webp' ? 'month' : 'day'
      if (entry.path.startsWith('/dashboard')) {
        dashboardMock = await dashboardPayload(context, options, entry.file === 'data-freshness.webp' ? 'stale' : 'fresh', range)
      } else dashboardMock = null
      await page.goto(new URL(entry.path, `${options.baseUrl}/`).href, { waitUntil: 'domcontentloaded' })
      await waitForPage(page)
      await applyCondition(page, entry)
      await page.waitForTimeout(350)
      if (pageErrors.length || consoleErrors.length) {
        throw new Error(`${entry.file} 렌더링 오류: ${[...pageErrors.splice(0), ...consoleErrors.splice(0)].join('; ')}`)
      }
      await writeWebP(page, resolve(options.output, entry.file))
      console.log(`✓ ${entry.file}`)
      dashboardMock = null
    }
  } finally {
    if (options.restoreSettings && originalSettings) {
      await restoreScreenshotFeatures(page, originalSettings).catch((error) => console.warn(`설정 원복 실패: ${error.message}`))
    }
    await context.close()
    await browser.close()
  }
  console.log(`${entries.length}개 WebP 캡처 완료: ${options.output}`)
}

main().catch((error) => {
  console.error(`캡처 실패: ${error.message}`)
  process.exit(1)
})
