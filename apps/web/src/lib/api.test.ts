import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from './api'
import type { Publication, QuerySpec } from './types'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const page = (data: unknown[], hasMore = false, nextCursor = '') => json({ data, hasMore, nextCursor })

afterEach(() => { vi.unstubAllGlobals() })

describe('ApiClient transport and complete directories', () => {
  it('follows all pages using same-origin paths and never redirects credentials', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(page([{ id: 'first' }], true, 'first'))
      .mockResolvedValueOnce(page([{ id: 'second' }]))
    vi.stubGlobal('fetch', fetcher)
    const signal = new AbortController().signal
    const result = await new ApiClient('in-memory-test-token').listDefinitions({ categoryId: '分类 & 一', view: 'draft', kind: 'schema' }, signal)
    expect(result).toEqual([{ id: 'first' }, { id: 'second' }])
    expect(fetcher).toHaveBeenCalledTimes(2)
    for (const [path, options] of fetcher.mock.calls) {
      expect(path).toMatch(/^\/api\/metadata\/definitions\?/)
      expect(new URL(path, 'http://local').searchParams.get('categoryId')).toBe('分类 & 一')
      expect(new URL(path, 'http://local').searchParams.get('view')).toBe('draft')
      expect(path).not.toContain('in-memory-test-token')
      expect(options).toMatchObject({ signal, redirect: 'error', credentials: 'omit', mode: 'same-origin', cache: 'no-store' })
      expect(options.headers.Authorization).toBe('Bearer in-memory-test-token')
    }
    expect(new URL(fetcher.mock.calls[1][0], 'http://local').searchParams.get('after')).toBe('first')
  })

  it('does not silently truncate a directory that exceeds its page budget', async () => {
    let n = 0
    const fetcher = vi.fn().mockImplementation(async () => page([{ id: ++n }], true, String(n)))
    vi.stubGlobal('fetch', fetcher)
    await expect(new ApiClient('test').listCategories()).rejects.toMatchObject({ code: 'pagination_budget' })
    expect(fetcher).toHaveBeenCalledTimes(100)
  })

  it.each([
    [{ data: [], hasMore: true }, 'invalid_pagination'],
    [{ data: [], hasMore: 'false' }, 'invalid_response'],
    [null, 'invalid_response'],
  ])('rejects malformed directory continuation', async (body, code) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(body)))
    await expect(new ApiClient('test').listCategories()).rejects.toMatchObject({ code })
  })

  it('rejects a repeated continuation instead of looping', async () => {
    const fetcher = vi.fn().mockImplementation(async () => page([], true, 'same'))
    vi.stubGlobal('fetch', fetcher)
    await expect(new ApiClient('test').listGroups('category')).rejects.toMatchObject({ code: 'invalid_pagination' })
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('preserves explicit cancellation', async () => {
    const aborted = new DOMException('cancelled', 'AbortError')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(aborted))
    await expect(new ApiClient('test').getCurrent('id')).rejects.toBe(aborted)
  })

  it('does not expose a transport exception that may contain a credential', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('upstream contained private-token')))
    const error = await new ApiClient('private-token').metadataCapabilities().catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).code).toBe('network_error')
    expect((error as ApiError).message).not.toContain('private-token')
  })
})

describe('ApiClient writes and errors', () => {
  it('quotes strong If-Match once and accepts an empty 204 response', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient('test')
    await expect(api.deleteInstance('schema', 'entity', 'etag-value')).resolves.toBeUndefined()
    await expect(api.deleteInstance('schema', 'entity', '"already-quoted"')).resolves.toBeUndefined()
    expect(fetcher.mock.calls[0][1].headers['If-Match']).toBe('"etag-value"')
    expect(fetcher.mock.calls[1][1].headers['If-Match']).toBe('"already-quoted"')
    await expect(api.deleteInstance('schema', 'entity', 'W/"weak"')).rejects.toMatchObject({ code: 'invalid_etag' })
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('retains publication operation on a recoverable 503', async () => {
    const publication: Publication = { id: 'operation', tenantId: 'tenant', definitionId: 'definition', status: 'activation_pending' }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ publication, error: { code: 'activation_pending', message: '等待恢复' } }, 503)))
    await expect(new ApiClient('test').publish('definition', 'digest')).rejects.toMatchObject({ status: 503, code: 'activation_pending', message: '等待恢复', publication })
  })

  it('preserves the conflict status without trying to publish again', async () => {
    const fetcher = vi.fn().mockResolvedValue(json({ code: 'precondition_failed', message: '请重新读取' }, 412))
    vi.stubGlobal('fetch', fetcher)
    await expect(new ApiClient('test').publish('definition', 'digest')).rejects.toMatchObject({ status: 412, code: 'precondition_failed' })
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('preserves precision, null and false in instance bodies and query descriptions', async () => {
    const fetcher = vi.fn().mockImplementation(async () => json({ id: 'entity' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient('test')
    const values = { exact: '9007199254740993.0000', absent: null, enabled: false }
    await api.createInstance('schema', values, 'stable-intent')
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ values, idempotencyKey: 'stable-intent' })
    const spec: QuerySpec = { from: { schemaId: 'schema' }, select: ['exact'], where: { op: 'eq', field: 'exact', value: values.exact }, page: { limit: 10, cursor: 'opaque' } }
    await api.query(spec)
    expect(fetcher.mock.calls[1][0]).toBe('/api/data/query')
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual(spec)
  })

  it('turns malformed successful JSON into a clear error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>proxy error</html>', { status: 200 })))
    await expect(new ApiClient('test').dataCapabilities()).rejects.toMatchObject({ status: 502, code: 'invalid_response' })
  })

  it('invalidates pending reads after 401 and invokes the global reset only once', async () => {
    let finishRead!: (value: Response) => void
    const pending = new Promise<Response>((resolve) => { finishRead = resolve })
    const fetcher = vi.fn().mockReturnValueOnce(pending).mockResolvedValueOnce(json({ code: 'unauthenticated', message: '身份过期' }, 401))
    vi.stubGlobal('fetch', fetcher)
    const reset = vi.fn()
    const api = new ApiClient('test', reset)
    const read = api.getCurrent('definition')
    await expect(api.getDraft('definition')).rejects.toMatchObject({ status: 401 })
    finishRead(json({ definition: { name: '不应重新显示的旧结果' }, etag: 'old', status: 'active' }))
    await expect(read).rejects.toMatchObject({ status: 401 })
    await expect(api.listCategories()).rejects.toMatchObject({ status: 401 })
    expect(reset).toHaveBeenCalledTimes(1)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })
})
