import { describe, expect, it, vi } from 'vitest'
import { request, requestList, streamAI } from './client'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('API client', () => {
  it('data envelope를 해제한다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ data: { version: '1.2.3' } })))
    await expect(request<{ version: string }>('/version')).resolves.toEqual({ version: '1.2.3' })
  })

  it('generic resource의 data를 평탄화하되 공통 필드를 우선한다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ data: [{ id: 7, name: '운영 정책', status: 'active', data: { name: '잘못된 이름', idle_timeout_minutes: 120 } }], meta: { total: 1 } })))
    const result = await requestList('/policies')
    expect(result.data[0]).toMatchObject({ id: 7, name: '운영 정책', idle_timeout_minutes: 120 })
    expect(result.meta.total).toBe(1)
  })

  it('서버 오류 메시지를 ApiError로 전달한다', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ error: { code: 'DENIED', message: '권한이 없습니다.' } }, 403)))
    await expect(request('/settings')).rejects.toMatchObject({ status: 403, code: 'DENIED', message: '권한이 없습니다.' })
  })

  it('표준 OpenAI SSE와 단순 delta 형식을 함께 처리한다', async () => {
    const encoded = new TextEncoder().encode([
      'data: {"choices":[{"delta":{"content":"안녕"}}]}',
      '',
      'data: {"delta":"하세요"}',
      '',
      'data: {"choices":[{"delta":{}}],"usage":{"total_tokens":3}}',
      '',
      'data: [DONE]',
      '',
    ].join('\n'))
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new ReadableStream({
      start(controller) { controller.enqueue(encoded); controller.close() },
    }), { status: 200, headers: { 'Content-Type': 'text/event-stream' } })))
    const chunks: string[] = []
    await streamAI({ messages: [{ role: 'user', content: '인사' }] }, (chunk) => chunks.push(chunk))
    expect(chunks).toEqual(['안녕', '하세요'])
  })
})
