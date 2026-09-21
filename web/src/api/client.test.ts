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

  it.each([
    ['event: error\ndata: provider failed\n\n', 'provider failed'],
    ['data: {"error":"provider failed"}\n\n', 'provider failed'],
    ['data: {"error":{"message":"provider failed"}}\n\n', 'provider failed'],
  ])('EOF 전 오류를 취소하고 잠금을 해제한다: %s', async (event, message) => {
    const cancel = vi.fn()
    const body = new ReadableStream({
      start(controller) { controller.enqueue(new TextEncoder().encode(event)) },
      cancel,
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body)))

    await expect(streamAI({}, vi.fn())).rejects.toMatchObject({ name: 'ApiError', message, status: 502 })
    expect(cancel).toHaveBeenCalledOnce()
    expect(body.locked).toBe(false)
  })

  it('취소 실패가 원래 공급자 오류를 덮어쓰지 않고 잠금을 해제한다', async () => {
    const cancel = vi.fn().mockRejectedValue(new Error('cancel failed'))
    const body = new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('event: error\ndata: provider failed\n\n'))
      },
      cancel,
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body)))

    await expect(streamAI({}, vi.fn())).rejects.toMatchObject({ name: 'ApiError', message: 'provider failed', status: 502 })
    expect(cancel).toHaveBeenCalledOnce()
    expect(body.locked).toBe(false)
  })

  it.each([new Error('read failed'), new DOMException('aborted', 'AbortError')])(
    '읽기 실패 원본을 유지하고 잠금을 해제한다: %s', async (error) => {
      const body = new ReadableStream({
        pull(controller) { controller.error(error) },
      })
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body)))

      await expect(streamAI({}, vi.fn())).rejects.toBe(error)
      expect(body.locked).toBe(false)
    },
  )

  it('UTF-8 분할 청크와 DONE 뒤 마지막 버퍼를 출력하고 정상 EOF에서 잠금을 해제한다', async () => {
    const encoded = new TextEncoder().encode(
      'data: {"delta":"안녕"}\n\ndata: [DONE]\n\ndata: {"delta":"끝"}',
    )
    let offset = 0
    const cancel = vi.fn()
    const body = new ReadableStream({
      pull(controller) {
        if (offset < encoded.length) controller.enqueue(encoded.slice(offset, ++offset))
        else controller.close()
      },
      cancel,
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body)))
    const chunks: string[] = []

    await streamAI({}, (chunk) => chunks.push(chunk))
    expect(chunks).toEqual(['안녕', '끝'])
    expect(cancel).not.toHaveBeenCalled()
    expect(body.locked).toBe(false)
  })

})
