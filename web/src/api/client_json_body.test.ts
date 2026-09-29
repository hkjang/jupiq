import { createServer } from 'node:http'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

interface BodyCase {
  name: string
  status: number
  contentType: string
  body: string
}

const brokenErrorCases: BodyCase[] = [
  { name: '끊긴 JSON 오류 본문', status: 403, contentType: 'application/json', body: '{"error":{"code":"forbidden"' },
  { name: 'JSON을 자처한 빈 오류 본문', status: 500, contentType: 'application/json; charset=utf-8', body: '' },
  { name: '공백뿐인 JSON 오류 본문', status: 503, contentType: 'application/json', body: '  \n ' },
]

const brokenSuccessCases: BodyCase[] = [
  { name: '끊긴 JSON 성공 본문', status: 200, contentType: 'application/json', body: '{"data":{"version":"1.8' },
  { name: 'JSON을 자처한 빈 성공 본문', status: 200, contentType: 'application/json; charset=utf-8', body: '' },
  { name: '공백뿐인 JSON 성공 본문', status: 200, contentType: 'application/json', body: '\n\t ' },
  { name: 'JSON 헤더로 온 HTML 오류 페이지', status: 200, contentType: 'application/json', body: '<html><body>502</body></html>' },
]

const intactCases: BodyCase[] = [
  { name: '정상 JSON', status: 200, contentType: 'application/json; charset=utf-8', body: '{"data":{"version":"9.9.9"}}' },
  { name: '본문 없는 204', status: 204, contentType: 'application/json', body: '' },
  { name: '비JSON 본문', status: 200, contentType: 'text/plain; charset=utf-8', body: 'plain text' },
]

describe('JSON을 자처한 본문', () => {
  let client: typeof import('./client')
  let responseCase: BodyCase = brokenErrorCases[0]
  const server = createServer((request, response) => {
    request.resume()
    response.writeHead(responseCase.status, { 'Content-Type': responseCase.contentType })
    response.end(responseCase.status === 204 ? undefined : responseCase.body)
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

  const errorPaths = [
    { name: 'request', fallback: '요청에 실패했습니다.', call: () => client.request('/settings') },
    { name: 'requestList', fallback: '목록을 불러오지 못했습니다.', call: () => client.requestList('/users') },
    {
      name: 'streamAI',
      fallback: 'AI 요청에 실패했습니다.',
      call: () => client.streamAI({}, () => { throw new Error('HTTP 오류에서 청크가 발생했습니다.') }),
    },
  ]

  describe.each(errorPaths)('$name', ({ call, fallback }) => {
    it.each(brokenErrorCases)('$name에서도 HTTP 상태를 잃지 않는다', async (testCase) => {
      responseCase = testCase
      const result = call()
      await expect(result).rejects.toBeInstanceOf(client.ApiError)
      await expect(result).rejects.toMatchObject({
        status: testCase.status,
        code: undefined,
        message: `${fallback} (${testCase.status})`,
      })
    })
  })

  const successPaths = [
    { name: 'request', call: () => client.request('/version') },
    { name: 'requestList', call: () => client.requestList('/users') },
  ]

  describe.each(successPaths)('$name', ({ call }) => {
    it.each(brokenSuccessCases)('$name을 조용히 빈 결과로 바꾸지 않는다', async (testCase) => {
      responseCase = testCase
      const result = call()
      await expect(result).rejects.toBeInstanceOf(client.ApiError)
      await expect(result).rejects.toMatchObject({
        status: testCase.status,
        code: 'INVALID_RESPONSE',
        message: `서버 응답을 해석할 수 없습니다. (${testCase.status})`,
      })
    })
  })

  it('정상 JSON 응답의 data를 그대로 해제한다', async () => {
    responseCase = intactCases[0]
    await expect(client.request('/version')).resolves.toEqual({ version: '9.9.9' })
  })

  it('204 응답은 본문 없이 null을 돌려준다', async () => {
    responseCase = intactCases[1]
    await expect(client.request('/servers/1')).resolves.toBeNull()
  })

  it('비JSON 성공 본문은 원문을 그대로 돌려준다', async () => {
    responseCase = intactCases[2]
    await expect(client.request('/version')).resolves.toBe('plain text')
  })

  it('정상 목록 응답의 meta를 유지한다', async () => {
    responseCase = { name: '정상 목록', status: 200, contentType: 'application/json', body: '{"data":[{"id":1}],"meta":{"total":7}}' }
    await expect(client.requestList('/users')).resolves.toEqual({ data: [{ id: 1 }], meta: { total: 7 } })
  })
})
