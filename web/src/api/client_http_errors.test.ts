import { createServer } from 'node:http'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

const cases = [
  { name: 'forbidden', status: 403, body: { error: { code: 'forbidden', message: '권한이 없습니다.' }, message: '후순위' }, code: 'forbidden', message: '권한이 없습니다.' },
  { name: 'invalid_query', status: 400, body: { error: { code: 'invalid_query', message: '잘못된 질의입니다.' } }, code: 'invalid_query', message: '잘못된 질의입니다.' },
  { name: 'code 없는 객체', status: 403, body: { error: { message: '객체 오류' }, message: '후순위' }, message: '객체 오류' },
  { name: '문자열 error', status: 403, body: { error: '문자열 오류', message: '후순위' }, message: '문자열 오류' },
  { name: '최상위 message', status: 403, body: { message: '최상위 오류' }, message: '최상위 오류' },
  { name: '빈 객체 message', status: 403, body: { error: { message: '' }, message: '대체 오류' }, message: '대체 오류' },
  { name: '빈 문자열 error', status: 403, body: { error: '', message: '후순위' }, message: '' },
  { name: '정보 없는 JSON', status: 403, body: {} },
  { name: 'null JSON', status: 403, body: null },
  { name: '비JSON 본문', status: 403, text: 'upstream unavailable' },
  { name: '빈 본문', status: 403, text: '' },
]

describe('실제 HTTP 오류 응답', () => {
  let client: typeof import('./client')
  let responseCase: (typeof cases)[number] = cases[0]
  const server = createServer((request, response) => {
    request.resume()
    response.writeHead(responseCase.status, {
      'Content-Type': 'text' in responseCase ? 'text/plain' : 'application/json',
    })
    response.end('text' in responseCase ? responseCase.text : JSON.stringify(responseCase.body))
  })

  beforeAll(async () => {
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject)
      server.listen(0, '127.0.0.1', resolve)
    })
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('HTTP 서버 포트가 없습니다.')
    vi.stubEnv('VITE_API_BASE_URL', `http://127.0.0.1:${address.port}/api/v1`)
    vi.resetModules()
    client = await import('./client')
  })

  afterAll(async () => {
    vi.unstubAllEnvs()
    vi.resetModules()
    await new Promise<void>((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve())
    })
  })

  const paths = [
    { name: 'request', fallback: '요청에 실패했습니다.', call: () => client.request('/settings') },
    { name: 'requestList', fallback: '목록을 불러오지 못했습니다.', call: () => client.requestList('/users') },
    { name: 'streamAI', fallback: 'AI 요청에 실패했습니다.', call: () => client.streamAI({}, () => { throw new Error('HTTP 오류에서 청크가 발생했습니다.') }) },
  ]

  describe.each(paths)('$name', ({ call, fallback }) => {
    it.each(cases)('$name의 status/code/message를 보존한다', async (testCase) => {
      responseCase = testCase
      const result = call()
      await expect(result).rejects.toBeInstanceOf(client.ApiError)
      await expect(result).rejects.toMatchObject({
        status: testCase.status,
        code: 'code' in testCase ? testCase.code : undefined,
        message: 'message' in testCase ? testCase.message : `${fallback} (${testCase.status})`,
      })
    })
  })
})
