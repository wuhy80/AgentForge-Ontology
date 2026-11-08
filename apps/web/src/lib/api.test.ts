import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from './api'
import type { DefinitionInput, PermissionPolicy, Publication, QuerySpec, SavedQueryInput, Session, UserGroup, UserGroupInput, UserInput } from './types'

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
  it('保存查询编译不携带实参，CRUD使用强ETag，运行单独传参数与游标', async () => {
    const input: SavedQueryInput = { name: '金额筛选', source: { kind: 'ontology-sql', text: 'SELECT 金额 FROM 订单 WHERE 金额 >= :最低金额', defaultCategoryId: 'category' }, query: { from: { schemaId: 'orders' }, select: ['amount'], where: { op: 'gte', field: 'amount', parameter: '最低金额' }, page: { limit: 25 } }, parameters: [{ name: '最低金额', dataType: 'decimal', nullable: false, precision: 24, scale: 6 }] }
    const fetcher = vi.fn().mockImplementation(async () => json({ ...input, id: 'query / one', etag: 'strong' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient(), signal = new AbortController().signal
    await api.compileSavedQuery(input.source.text!, 'category', signal)
    await api.createSavedQuery(input, signal)
    await api.updateSavedQuery('query / one', { ...input, name: '新名称' }, 'strong', signal)
    await api.executeSavedQuery('query / one', { 最低金额: '9007199254740993.000001' }, 'signed-cursor', signal)
    await api.deleteSavedQuery('query / one', 'strong', signal)
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ text: input.source.text, defaultCategoryId: 'category' })
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual(input)
    expect(fetcher.mock.calls[2][1]).toMatchObject({ method: 'PUT', headers: { 'If-Match': '"strong"' }, signal })
    expect(fetcher.mock.calls[3][0]).toBe('/api/data/saved-queries/query%20%2F%20one/execute')
    expect(JSON.parse(fetcher.mock.calls[3][1].body)).toEqual({ parameters: { 最低金额: '9007199254740993.000001' }, cursor: 'signed-cursor' })
    expect(fetcher.mock.calls[4][1]).toMatchObject({ method: 'DELETE', headers: { 'If-Match': '"strong"' } })
    await expect(api.deleteSavedQuery('query / one', 'W/"weak"')).rejects.toMatchObject({ code: 'invalid_etag' })
    expect(fetcher).toHaveBeenCalledTimes(5)
  })
  it('保存定义只提交枚举稳定引用，剔除服务端派生选项和摘要且不修改本地对象', async () => {
    const fetcher = vi.fn().mockImplementation(async () => json({ status: 'draft', etag: 'saved', definition: {} }))
    vi.stubGlobal('fetch', fetcher)
    const field = { id: 'state', name: '状态', dataType: 'string' as const, enumId: 'enum', enumValues: [{ code: 'READY', label: '就绪' }], enumEtag: 'private-enum-etag' }
    const definition: DefinitionInput = { kind: 'schema', name: '设备', categoryId: 'category', groupId: 'group', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [field] } }
    const api = new ApiClient('test')
    await api.createDefinition(definition)
    await api.saveDraft('model', { ...definition, kind: 'function', schema: undefined, function: { sideEffects: 'none', input: [field], output: [field] } }, 'current')
    expect(JSON.parse(fetcher.mock.calls[0][1].body).schema.properties[0]).toEqual({ id: 'state', name: '状态', dataType: 'string', enumId: 'enum' })
    expect(JSON.parse(fetcher.mock.calls[1][1].body).function).toMatchObject({ input: [{ id: 'state', enumId: 'enum' }], output: [{ id: 'state', enumId: 'enum' }] })
    for (const call of fetcher.mock.calls) {
      expect(call[1].body).not.toContain('enumValues')
      expect(call[1].body).not.toContain('enumEtag')
    }
    expect(field.enumValues[0].label).toBe('就绪')
    expect(field.enumEtag).toBe('private-enum-etag')
  })
  it('manages personal connections and OAuth consent without persisting bearer credentials', async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(json({ data: [], resource: 'http://127.0.0.1:18084/mcp', issuer: 'http://127.0.0.1:18080' }))
      .mockResolvedValueOnce(json({ connection: { id: 'connection', name: 'dsh', scopes: ['ontology:discover'], createdAt: 1, expiresAt: 2, kind: 'personal' }, accessToken: 'mcp_secret' }, 201))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(json({ client: { clientId: 'client', name: 'dsh', redirectUris: ['https://client.example/cb'] }, scopes: ['ontology:discover'], resource: 'http://127.0.0.1:18084/mcp' }))
      .mockResolvedValueOnce(json({ redirectUri: 'https://client.example/cb?code=one&state=two' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('csrf')
    const request = { client_id: 'client', redirect_uri: 'https://client.example/cb', response_type: 'code', code_challenge_method: 'S256', code_challenge: 'A'.repeat(43), scope: 'ontology:discover', state: 'two', resource: 'http://127.0.0.1:18084/mcp' }
    await api.listConnections(); await api.createConnection({ name: 'dsh', scopes: ['ontology:discover'], expiresInSeconds: 3600 }); await api.revokeConnection('connection')
    await api.previewOAuth(request); await api.authorizeOAuth(request, true)
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual(['/api/metadata/auth/connections', '/api/metadata/auth/connections', '/api/metadata/auth/connections/connection', '/api/metadata/auth/oauth/preview', '/api/metadata/auth/oauth/authorize'])
    expect(JSON.parse(fetcher.mock.calls[4][1].body)).toMatchObject({ ...request, approve: true })
    expect(fetcher.mock.calls[4][1].credentials).toBe('same-origin')
    expect(fetcher.mock.calls[4][1].body).not.toContain('mcp_secret')
  })

  it('数据库查询和静态SQL工具均携带配置凭证、CSRF及实际行数上限', async () => {
    const fetcher = vi.fn().mockImplementation(async () => json({ valid: true, sql: 'SELECT * FROM orders;', dialect: 'mysql', message: '检查通过' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('session-csrf')
    const signal = new AbortController().signal
    const sql = "SELECT amount FROM orders WHERE memo = '中文;订单' LIMIT 90;"
    await api.queryDataSource('source/one', sql, 'source-etag', signal, 20)
    await api.analyzeSourceSQL('source/one', sql, 'source-etag', signal, 20)
    await api.formatSourceSQL('source/one', sql, 'source-etag', signal, 20)
    for (const [index, suffix] of ['query', 'sql/analyze', 'sql/format'].entries()) {
      const [path, options] = fetcher.mock.calls[index]
      expect(path).toBe(`/api/data/sources/source%2Fone/${suffix}`)
      expect(options).toMatchObject({ method: 'POST', signal })
      expect(options.headers['If-Match']).toBe('"source-etag"')
      expect(options.headers['X-CSRF-Token']).toBe('session-csrf')
      expect(JSON.parse(options.body)).toEqual({ sql, limit: 20 })
    }
  })
  it('读取后端版本选项，测试当前编辑来源携带If-Match并省略保留密码', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(page([{ driver: 'mysql', versionLine: '26.7', status: 'supported', label: 'MySQL 26.7' }]))
      .mockResolvedValueOnce(json({ success: true, serverVersion: '26.7.0', compatibility: { compatibilityStatus: 'supported' } }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('session-csrf')
    await expect(api.listSourceVersions()).resolves.toEqual([{ driver: 'mysql', versionLine: '26.7', status: 'supported', label: 'MySQL 26.7' }])
    await api.testDataSource('source / 一', { name: '业务库', driver: 'mysql', host: 'edited-host', port: 3306, database: 'business', username: 'reader', tlsMode: 'require', tenantIsolation: 'column', enabled: true, versionPolicy: 'pinned', expectedVersionLine: '26.7' }, 'source-etag')
    expect(fetcher.mock.calls[0][0]).toBe('/api/data/source-versions')
    expect(fetcher.mock.calls[1][0]).toBe('/api/data/sources/source%20%2F%20%E4%B8%80/test')
    expect(fetcher.mock.calls[1][1].headers).toMatchObject({ 'X-CSRF-Token': 'session-csrf', 'If-Match': '"source-etag"' })
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toMatchObject({ host: 'edited-host', versionPolicy: 'pinned', expectedVersionLine: '26.7' })
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).not.toHaveProperty('password')
  })

  it('查询正反关系目录并按稳定ID读写关系事实，写入携带CSRF与强ETag', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(json({ data: [] }))
      .mockResolvedValueOnce(json({ data: [], hasMore: false }))
      .mockResolvedValueOnce(json({ id: 'edge' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('session-csrf')
    await api.schemaLinks('对象 / 一')
    await api.listLinkInstances('关系 / 一', 'in', '实例 & 一', 'opaque-cursor')
    await api.createLinkInstance('关系 / 一', 'source', 'target')
    await api.deleteLinkInstance('关系 / 一', '边 / 一', 'edge-etag')
    expect(fetcher.mock.calls[0][0]).toBe('/api/metadata/schemas/%E5%AF%B9%E8%B1%A1%20%2F%20%E4%B8%80/links')
    const url = new URL(fetcher.mock.calls[1][0], 'http://local')
    expect(url.searchParams.get('direction')).toBe('in')
    expect(url.searchParams.get('instanceId')).toBe('实例 & 一')
    expect(url.searchParams.get('cursor')).toBe('opaque-cursor')
    expect(fetcher.mock.calls[2][1]).toMatchObject({ method: 'POST', body: JSON.stringify({ sourceId: 'source', targetId: 'target' }), headers: { 'X-CSRF-Token': 'session-csrf' } })
    expect(fetcher.mock.calls[3][1]).toMatchObject({ method: 'DELETE', headers: { 'X-CSRF-Token': 'session-csrf', 'If-Match': '"edge-etag"' } })
  })
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

  it('读取写能力并通过公开 upsert 和四个批量接口保留精度、幂等键和并发凭证', async () => {
    const fetcher = vi.fn().mockImplementation(async () => json({ schemaId: 'schema/id' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient(), signal = new AbortController().signal
    api.setCSRFToken('csrf')
    await api.writeCapabilities('schema/id', signal)
    const input = { match: { id: 'row' }, values: { amount: '9007199254740993.0001' }, conflictPolicy: 'check' as const, idempotencyKey: 'intent' }
    await api.upsertInstance('schema/id', input, '"etag"', signal)
    for (const operation of ['bulk_create', 'bulk_update', 'bulk_delete', 'bulk_upsert'] as const) {
      await api.bulkWrite('schema/id', operation, { atomic: true, idempotencyKey: 'bulk-intent', items: [{ clientKey: 'one', id: 'row', ifMatch: '"etag"', values: input.values }] }, signal)
    }
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual(['/api/data/schemas/schema%2Fid/write-capabilities', '/api/data/schemas/schema%2Fid/instances/upsert',
      ...['bulk-create', 'bulk-update', 'bulk-delete', 'bulk-upsert'].map((suffix) => `/api/data/schemas/schema%2Fid/instances/${suffix}`)])
    expect(fetcher.mock.calls[1][1].headers).toMatchObject({ 'If-Match': '"etag"', 'X-CSRF-Token': 'csrf' })
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual(input)
    expect(JSON.parse(fetcher.mock.calls[3][1].body).items[0]).toMatchObject({ ifMatch: '"etag"', values: input.values })
    for (const [, options] of fetcher.mock.calls) expect(options).toMatchObject({ signal, credentials: 'same-origin', cache: 'no-store' })
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

describe('cookie sessions and identity administration', () => {
  const session: Session = { user: { id: 'user', tenantId: 'tenant', username: 'operator', displayName: '管理员', administrator: true, enabled: true, roleIds: [], etag: 'user-etag' }, expiresAt: 1800000000, csrfToken: 'csrf-one' }

  it('uses same-origin cookies by default and installs the login CSRF token for unsafe requests', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(json(session)).mockResolvedValueOnce(json({ id: 'category' })).mockResolvedValueOnce(json(session))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    await expect(api.login({ tenant: 'tenant', username: 'operator', password: 'a-private-password' })).resolves.toEqual(session)
    await api.createCategory('生产')
    await api.getSession()
    expect(fetcher.mock.calls[0][0]).toBe('/api/metadata/auth/login')
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ tenant: 'tenant', username: 'operator', password: 'a-private-password' })
    expect(fetcher.mock.calls[0][1].headers['X-CSRF-Token']).toBeUndefined()
    expect(fetcher.mock.calls[1][1].headers['X-CSRF-Token']).toBe('csrf-one')
    expect(fetcher.mock.calls[2][1].headers['X-CSRF-Token']).toBeUndefined()
    for (const [path, options] of fetcher.mock.calls) {
      expect(options.credentials).toBe('same-origin')
      expect(options.headers.Authorization).toBeUndefined()
      expect(path).not.toContain('private-password')
      expect(options).toMatchObject({ redirect: 'error', cache: 'no-store', mode: 'same-origin' })
    }
  })

  it('rotates CSRF after password changes and invalidates the session after logout', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(json({ ...session, csrfToken: 'csrf-two' })).mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetcher)
    const reset = vi.fn(), api = new ApiClient(undefined, reset)
    api.setCSRFToken('csrf-one')
    await api.changePassword('old-password', 'new-password')
    expect(fetcher.mock.calls[0][1].headers['X-CSRF-Token']).toBe('csrf-one')
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ currentPassword: 'old-password', password: 'new-password' })
    await api.logout()
    expect(fetcher.mock.calls[1][1].headers['X-CSRF-Token']).toBe('csrf-two')
    expect(reset).toHaveBeenCalledTimes(1)
    await expect(api.getSession()).rejects.toMatchObject({ status: 401 })
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('uses administration routes, typed bodies and strong optimistic concurrency for updates and publication', async () => {
    const fetcher = vi.fn().mockImplementation(async () => json({ data: [], current: { rules: [] }, etag: 'next', state: 'next' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient()
    api.setCSRFToken('csrf')
    const user: UserInput = { username: 'operator', displayName: '用户', enabled: false, administrator: false, roleIds: ['role'] }
    await api.listUsers(); await api.createUser(user); await api.updateUser('user / one', user, 'user-etag')
    await api.listRoles(); await api.createRole({ name: '采购', description: '采购数据' }); await api.updateRole('role', { name: '采购', description: '' }, 'role-etag')
    const policy: PermissionPolicy = { rules: [{ id: 'rule', roleId: 'role', effect: 'allow', operation: 'instance.query', resourceId: 'schema', readFields: ['field'], writeFields: [] }] }
    await api.getPolicy(); await api.savePolicy(policy, 'policy-etag'); await api.publishPolicy('saved-etag')
    await api.simulatePolicy({ userId: 'user', request: { operation: 'instance.query', resourceId: 'schema' }, draft: true }); await api.listAudit()
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual([
      '/api/metadata/admin/users', '/api/metadata/admin/users', '/api/metadata/admin/users/user%20%2F%20one',
      '/api/metadata/admin/roles', '/api/metadata/admin/roles', '/api/metadata/admin/roles/role',
      '/api/metadata/admin/policy', '/api/metadata/admin/policy', '/api/metadata/admin/policy/publish',
      '/api/metadata/admin/simulate', '/api/metadata/admin/audit',
    ])
    expect(fetcher.mock.calls[2][1].headers['If-Match']).toBe('"user-etag"')
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual(user)
    expect(fetcher.mock.calls[5][1].headers['If-Match']).toBe('"role-etag"')
    expect(fetcher.mock.calls[7][1].headers['If-Match']).toBe('"policy-etag"')
    expect(JSON.parse(fetcher.mock.calls[7][1].body)).toEqual(policy)
    expect(fetcher.mock.calls[8][1].headers['If-Match']).toBe('"saved-etag"')
  })

  it('clears cookie identity on any 401 and never reuses pending successful responses', async () => {
    let finish!: (response: Response) => void
    const fetcher = vi.fn().mockReturnValueOnce(new Promise<Response>((resolve) => { finish = resolve }))
      .mockResolvedValueOnce(json({ code: 'unauthenticated', message: '身份过期' }, 401))
    vi.stubGlobal('fetch', fetcher)
    const reset = vi.fn(), api = new ApiClient(undefined, reset)
    const users = api.listUsers()
    await expect(api.getSession()).rejects.toMatchObject({ status: 401 })
    finish(json({ data: [session.user] }))
    await expect(users).rejects.toMatchObject({ status: 401 })
    expect(reset).toHaveBeenCalledTimes(1)
  })

  it('manages identity groups using cookie CSRF, encoded paths and strong If-Match', async () => {
    const input: UserGroupInput = { name: '采购成员', description: '通过组授予采购角色', enabled: true, roleIds: ['purchaser'], userIds: ['operator'] }
    const group: UserGroup = { ...input, id: 'group / one', etag: 'group-etag' }
    const updated: UserGroupInput = { ...input, enabled: false, roleIds: [], userIds: [] }
    const fetcher = vi.fn().mockResolvedValueOnce(json({ data: [group] }))
      .mockResolvedValueOnce(json(group)).mockResolvedValueOnce(json({ ...group, ...updated, etag: 'next-etag' }))
    vi.stubGlobal('fetch', fetcher)
    const api = new ApiClient(), signal = new AbortController().signal
    api.setCSRFToken('csrf-group')
    await expect(api.listUserGroups(signal)).resolves.toEqual([group])
    await expect(api.createUserGroup(input)).resolves.toEqual(group)
    await expect(api.updateUserGroup(group.id, updated, '"group-etag"')).resolves.toMatchObject({ ...updated, etag: 'next-etag' })
    expect(fetcher.mock.calls.map(([path]) => path)).toEqual([
      '/api/metadata/admin/user-groups', '/api/metadata/admin/user-groups', '/api/metadata/admin/user-groups/group%20%2F%20one',
    ])
    expect(fetcher.mock.calls[0][1]).toMatchObject({ method: 'GET', signal })
    expect(fetcher.mock.calls[0][1].headers['X-CSRF-Token']).toBeUndefined()
    expect(fetcher.mock.calls[1][1]).toMatchObject({ method: 'POST', headers: { 'X-CSRF-Token': 'csrf-group' } })
    expect(fetcher.mock.calls[1][1].headers['If-Match']).toBeUndefined()
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual(input)
    expect(fetcher.mock.calls[2][1]).toMatchObject({ method: 'PUT', headers: { 'X-CSRF-Token': 'csrf-group', 'If-Match': '"group-etag"' } })
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual(updated)
    for (const [, options] of fetcher.mock.calls) {
      expect(options).toMatchObject({ credentials: 'same-origin', mode: 'same-origin', redirect: 'error', cache: 'no-store' })
      expect(options.headers.Authorization).toBeUndefined()
    }
    await expect(api.updateUserGroup(group.id, input, 'W/"weak"')).rejects.toMatchObject({ code: 'invalid_etag' })
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('preserves cancellation when loading identity groups', async () => {
    const aborted = new DOMException('cancelled', 'AbortError'), signal = new AbortController().signal
    const fetcher = vi.fn().mockRejectedValue(aborted)
    vi.stubGlobal('fetch', fetcher)
    await expect(new ApiClient().listUserGroups(signal)).rejects.toBe(aborted)
    expect(fetcher.mock.calls[0][1].signal).toBe(signal)
  })
})
