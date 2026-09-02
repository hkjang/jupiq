import type { ApiRecord, ListResult, PageMeta } from '../types'

const API_BASE = (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(/\/$/, '')

interface ErrorEnvelope {
  error?: { code?: string; message?: string } | string
  message?: string
}

export class ApiError extends Error {
  status: number
  code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export function apiUrl(path: string) {
  if (/^https?:\/\//.test(path)) return path
  return `${API_BASE}${path.startsWith('/') ? path : `/${path}`}`
}

async function parseResponse(response: Response): Promise<unknown> {
  if (response.status === 204) return null
  const contentType = response.headers.get('content-type') || ''
  if (contentType.includes('application/json')) return response.json()
  const text = await response.text()
  return text ? { data: text } : null
}

function errorMessage(body: unknown, fallback: string) {
  if (!body || typeof body !== 'object') return fallback
  const envelope = body as ErrorEnvelope
  if (typeof envelope.error === 'string') return envelope.error
  return envelope.error?.message || envelope.message || fallback
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  headers.set('Accept', 'application/json')

  let response: Response
  try {
    response = await fetch(apiUrl(path), { ...init, headers, credentials: 'include' })
  } catch (error) {
    throw new ApiError(error instanceof Error ? error.message : '서버에 연결할 수 없습니다.', 0, 'NETWORK_ERROR')
  }
  const body = await parseResponse(response)
  if (!response.ok) {
    const envelope = body && typeof body === 'object' ? (body as ErrorEnvelope) : undefined
    const code = typeof envelope?.error === 'object' ? envelope.error?.code : undefined
    throw new ApiError(errorMessage(body, `요청에 실패했습니다. (${response.status})`), response.status, code)
  }
  if (body && typeof body === 'object' && 'data' in body) return (body as { data: T }).data
  return body as T
}

export async function requestList<T extends ApiRecord>(path: string): Promise<ListResult<T>> {
  const response = await fetch(apiUrl(path), {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  }).catch((error: unknown) => {
    throw new ApiError(error instanceof Error ? error.message : '서버에 연결할 수 없습니다.', 0, 'NETWORK_ERROR')
  })
  const body = await parseResponse(response)
  if (!response.ok) throw new ApiError(errorMessage(body, `목록을 불러오지 못했습니다. (${response.status})`), response.status)
  if (Array.isArray(body)) return { data: body as T[], meta: { total: body.length } }
  if (body && typeof body === 'object') {
    const envelope = body as { data?: unknown; meta?: PageMeta }
    const rawItems = Array.isArray(envelope.data) ? (envelope.data as T[]) : []
    const items = rawItems.map((item) => {
      const nested = item.data
      return nested && typeof nested === 'object' && !Array.isArray(nested)
        ? { ...(nested as ApiRecord), ...item } as T
        : item
    })
    return { data: items, meta: envelope.meta || { total: items.length } }
  }
  return { data: [], meta: { total: 0 } }
}

export function jsonBody(value: unknown) {
  return JSON.stringify(value)
}

function aiChunkText(value: unknown): string {
  if (!value || typeof value !== 'object') return ''
  const chunk = value as {
    delta?: unknown
    content?: unknown
    error?: unknown
    choices?: Array<{ delta?: { content?: unknown } | string; message?: { content?: unknown }; text?: unknown }>
  }
  if (chunk.error) {
    const message = typeof chunk.error === 'string'
      ? chunk.error
      : typeof chunk.error === 'object' && chunk.error && 'message' in chunk.error
        ? String((chunk.error as { message: unknown }).message)
        : 'AI 공급자가 오류를 반환했습니다.'
    throw new ApiError(message, 502)
  }
  if (typeof chunk.delta === 'string') return chunk.delta
  if (typeof chunk.content === 'string') return chunk.content
  const choice = Array.isArray(chunk.choices) ? chunk.choices[0] : undefined
  if (typeof choice?.delta === 'string') return choice.delta
  if (choice?.delta && typeof choice.delta.content === 'string') return choice.delta.content
  if (choice?.message && typeof choice.message.content === 'string') return choice.message.content
  if (typeof choice?.text === 'string') return choice.text
  return ''
}

function consumeAIEvent(event: string, onChunk: (text: string) => void) {
  const lines = event.split(/\r?\n/)
  const eventType = lines.find((line) => line.startsWith('event:'))?.slice(6).trim()
  const data = lines
    .filter((line) => line.startsWith('data:'))
    .map((line) => line.slice(5).replace(/^ /, ''))
    .join('\n')
    .trim()
  if (!data || data === '[DONE]') return
  if (eventType === 'error') throw new ApiError(data, 502)
  try {
    const text = aiChunkText(JSON.parse(data))
    if (text) onChunk(text)
  } catch (error) {
    if (error instanceof ApiError) throw error
    // Some OpenAI-compatible providers stream plain text in data fields.
    onChunk(data)
  }
}

export async function streamAI(
  payload: ApiRecord,
  onChunk: (text: string) => void,
  signal?: AbortSignal,
) {
  const response = await fetch(apiUrl('/ai/chat'), {
    method: 'POST',
    credentials: 'include',
    headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...payload, stream: true }),
    signal,
  }).catch((error: unknown) => {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(error instanceof Error ? error.message : 'AI API에 연결할 수 없습니다.', 0)
  })
  if (!response.ok) {
    const body = await parseResponse(response)
    throw new ApiError(errorMessage(body, `AI 요청에 실패했습니다. (${response.status})`), response.status)
  }
  if (!response.body) throw new ApiError('스트리밍 응답을 읽을 수 없습니다.', 0)

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { value, done } = await reader.read()
    if (done) {
      buffer += decoder.decode()
      break
    }
    buffer += decoder.decode(value, { stream: true })
    const events = buffer.split(/\r?\n\r?\n/)
    buffer = events.pop() || ''
    for (const event of events) consumeAIEvent(event, onChunk)
  }
  if (buffer.trim()) consumeAIEvent(buffer, onChunk)
}
