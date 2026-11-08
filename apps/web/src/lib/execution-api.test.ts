import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiClient } from './api'
import { initialRuntime } from './runtime-language'

afterEach(() => vi.unstubAllGlobals())

describe('Execution 请求协议', () => {
  it('通过同源代理发送稳定参数ID、CSRF和幂等键，支持202状态及结果核对', async () => {
    const running = { id: 'run / 一', definitionId: 'function', status: 'running' }
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ invocation: true, runtimes: ['python'], synchronous: true }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(running), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...running, status: 'succeeded', output: { result: '9007199254740993' } }), { status: 200 }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('csrf-session')
    await api.executionCapabilities()
    expect(await api.invoke('function', { field: '9007199254740993' }, 'request-key')).toEqual(running)
    await api.getInvocation('run / 一')
    expect(fetcher.mock.calls[0][0]).toBe('/api/execution/capabilities')
    expect(fetcher.mock.calls[1][0]).toBe('/api/execution/invocations')
    expect(fetcher.mock.calls[1][1]).toMatchObject({ method: 'POST', credentials: 'same-origin', mode: 'same-origin', redirect: 'error', cache: 'no-store', headers: { 'Idempotency-Key': 'request-key', 'X-CSRF-Token': 'csrf-session' } })
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({ definitionId: 'function', input: { field: '9007199254740993' } })
    expect(fetcher.mock.calls[2][0]).toBe('/api/execution/invocations/run%20%2F%20%E4%B8%80')
  })

  it('Go 源码工具发送定义身份、未保存源码、操作及 CSRF，并传递取消信号', async () => {
    const result = { source: initialRuntime('go').source, diagnostics: [] }
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify(result), { status: 200 }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('csrf-session')
    const controller = new AbortController()
    expect(await api.sourceTools('saved-definition', initialRuntime('go'), 'format', controller.signal)).toEqual(result)
    expect(fetcher.mock.calls[0][0]).toBe('/api/execution/source-tools')
    expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': 'csrf-session' }, signal: controller.signal })
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ definitionId: 'saved-definition', runtime: initialRuntime('go'), operation: 'format' })
  })
})
