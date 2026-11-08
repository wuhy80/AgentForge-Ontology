import type {
  DataCapabilities, DefinitionInput, DefinitionRecord, Instance, Kind, MetadataCapabilities, ExecutionCapabilities, Invocation, ScriptRuntime, SourceToolResult,
  Organization, Page, Preview, Publication, QueryResult, QuerySpec, Values,
  AuditRecord, LoginRequest, PermissionPolicy, PolicyRecord, Role, RoleInput, Session, SimulationRequest, SimulationResult, User, UserInput, UserGroup, UserGroupInput,
  DataSource, DataSourceInput, SourceTableInfo, SourceTable, SourceTestResult, SourceQueryResult, SourceSQLToolResult, SchemaStorageMapping, SourceVersionOption, SourceOverview,
  SchemaLink, LinkInstance,
  QueryContext, OntologySQLRequest, OntologySQLAnalysis, OntologySQLCompletions, OntologySQLFormat, OntologySQLResult,
  QueryPathsRequest, QueryPathsResult, QueryFieldInsertionRequest, QueryFieldInsertionResult,
} from './types'
import type { ConnectionDirectory, CreateConnection, CreatedConnection, OAuthAuthorization, OAuthPreview } from './external-auth'

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly publication?: Publication

  constructor(status: number, code: string, message: string, publication?: Publication) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.publication = publication
  }
}

export interface DefinitionFilters { categoryId?: string; kind?: Kind; view?: 'current' | 'draft' }
type RequestOptions = { method?: string; body?: unknown; etag?: string; idempotencyKey?: string; signal?: AbortSignal }
const isObject = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)

function strongETag(value: string): string {
  let tag = value.trim()
  if (tag.startsWith('"') && tag.endsWith('"')) tag = tag.slice(1, -1)
  if (!tag || /["\s\x00-\x1f\x7f]/.test(tag) || tag.startsWith('W/')) {
    throw new ApiError(0, 'invalid_etag', '缺少有效的并发凭证，请重新读取后再提交')
  }
  return `"${tag}"`
}

// Credentials live only in this instance. All calls stay under the same-origin
// development proxy and never follow redirects carrying session credentials.
export class ApiClient {
  private readonly token?: string
  private csrfToken = ''
  private readonly onUnauthorized?: () => void
  private invalidated = false

  constructor(token?: string, onUnauthorized?: () => void) {
    this.token = token
    this.onUnauthorized = onUnauthorized
  }

  private expire(): void {
    if (this.invalidated) return
    this.invalidated = true
    this.csrfToken = ''
    this.onUnauthorized?.()
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    if (this.invalidated) throw new ApiError(401, 'unauthenticated', '身份已失效，请重新连接')
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (this.token) headers.Authorization = `Bearer ${this.token}`
    const method = options.method ?? 'GET'
    if (this.csrfToken && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers['X-CSRF-Token'] = this.csrfToken
    if (options.body !== undefined) headers['Content-Type'] = 'application/json; charset=utf-8'
    if (options.etag !== undefined) headers['If-Match'] = strongETag(options.etag)
    if (options.idempotencyKey !== undefined) headers['Idempotency-Key'] = options.idempotencyKey
    let response: Response
    try {
      response = await fetch(path, {
        method, headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: options.signal, credentials: this.token ? 'omit' : 'same-origin', mode: 'same-origin', redirect: 'error', cache: 'no-store',
      })
    } catch (error) {
      if (isObject(error) && error.name === 'AbortError') throw error
      throw new ApiError(0, 'network_error', '无法连接服务，请检查本地服务和代理后重试；写入请先核对结果')
    }
    // A concurrent 401 invalidates this client's already pending responses too.
    if (response.status === 401) this.expire()
    if (this.invalidated && response.status !== 401) throw new ApiError(401, 'unauthenticated', '身份已失效，请重新连接')
    if (response.status === 204 || response.status === 205) return undefined as T
    let payload: unknown
    try {
      const text = await response.text()
      payload = text ? JSON.parse(text) : undefined
    } catch (error) {
      if (isObject(error) && error.name === 'AbortError') throw error
      if (response.ok) throw new ApiError(502, 'invalid_response', '服务响应格式错误，请核对服务状态')
    }
    if (!response.ok) {
      const envelope = isObject(payload) ? payload : {}
      const detail = isObject(envelope.error) ? envelope.error : envelope
      const code = typeof detail.code === 'string' ? detail.code : `http_${response.status}`
      const message = typeof detail.message === 'string' ? detail.message : `请求失败（HTTP ${response.status}），请核对服务状态`
      const publication = isObject(envelope.publication) && typeof envelope.publication.id === 'string'
        ? envelope.publication as unknown as Publication : undefined
      throw new ApiError(response.status, code, message, publication)
    }
    if (this.invalidated) throw new ApiError(401, 'unauthenticated', '身份已失效，请重新连接')
    if (payload === undefined) throw new ApiError(502, 'invalid_response', '服务未返回预期数据，请核对结果')
    return payload as T
  }

  setCSRFToken(token: string): void { this.csrfToken = token }
  executionCapabilities(signal?: AbortSignal): Promise<ExecutionCapabilities> { return this.request('/api/execution/capabilities', { signal }) }
  invoke(definitionId: string, input: Values, idempotencyKey: string, signal?: AbortSignal): Promise<Invocation> {
    return this.request('/api/execution/invocations', { method: 'POST', body: { definitionId, input }, idempotencyKey, signal })
  }
  invokeDraft(definitionId: string, input: Values, draftEtag: string, idempotencyKey: string, signal?: AbortSignal): Promise<Invocation> {
    return this.request('/api/execution/invocations', { method: 'POST', body: { definitionId, input, draftEtag }, idempotencyKey, signal })
  }
  getInvocation(id: string, signal?: AbortSignal): Promise<Invocation> { return this.request(`/api/execution/invocations/${encodeURIComponent(id)}`, { signal }) }
  sourceTools(definitionId: string, runtime: ScriptRuntime, operation: 'check' | 'format', signal?: AbortSignal): Promise<SourceToolResult> {
    return this.request('/api/execution/source-tools', { method: 'POST', body: { definitionId, runtime, operation }, signal })
  }
  private async readSession(path: string, options: RequestOptions = {}): Promise<Session> {
    const session = await this.request<Session>(path, options)
    this.setCSRFToken(session.csrfToken)
    return session
  }
  login(input: LoginRequest): Promise<Session> {
    return this.readSession('/api/metadata/auth/login', { method: 'POST', body: input })
  }
  getSession(signal?: AbortSignal): Promise<Session> { return this.readSession('/api/metadata/auth/session', { signal }) }
  async logout(): Promise<void> {
    await this.request<void>('/api/metadata/auth/logout', { method: 'POST' })
    this.expire()
  }
  changePassword(currentPassword: string, password: string): Promise<Session> {
    return this.readSession('/api/metadata/auth/password', { method: 'PUT', body: { currentPassword, password } })
  }

  listConnections(signal?: AbortSignal): Promise<ConnectionDirectory> { return this.request('/api/metadata/auth/connections', { signal }) }
  createConnection(input: CreateConnection, signal?: AbortSignal): Promise<CreatedConnection> {
    return this.request('/api/metadata/auth/connections', { method: 'POST', body: input, signal })
  }
  revokeConnection(id: string, signal?: AbortSignal): Promise<void> {
    return this.request(`/api/metadata/auth/connections/${encodeURIComponent(id)}`, { method: 'DELETE', signal })
  }
  previewOAuth(input: OAuthAuthorization, signal?: AbortSignal): Promise<OAuthPreview> {
    return this.request('/api/metadata/auth/oauth/preview', { method: 'POST', body: input, signal })
  }
  authorizeOAuth(input: OAuthAuthorization, approve: boolean, signal?: AbortSignal): Promise<{ redirectUri: string }> {
    return this.request('/api/metadata/auth/oauth/authorize', { method: 'POST', body: { ...input, approve }, signal })
  }

  async listUsers(signal?: AbortSignal): Promise<User[]> {
    return (await this.request<{ data: User[] }>('/api/metadata/admin/users', { signal })).data
  }
  createUser(input: UserInput): Promise<User> { return this.request('/api/metadata/admin/users', { method: 'POST', body: input }) }
  updateUser(id: string, input: UserInput, etag: string): Promise<User> {
    return this.request(`/api/metadata/admin/users/${encodeURIComponent(id)}`, { method: 'PUT', body: input, etag })
  }
  async listRoles(signal?: AbortSignal): Promise<Role[]> {
    return (await this.request<{ data: Role[] }>('/api/metadata/admin/roles', { signal })).data
  }
  createRole(input: RoleInput): Promise<Role> { return this.request('/api/metadata/admin/roles', { method: 'POST', body: input }) }
  updateRole(id: string, input: RoleInput, etag: string): Promise<Role> {
    return this.request(`/api/metadata/admin/roles/${encodeURIComponent(id)}`, { method: 'PUT', body: input, etag })
  }
  async listUserGroups(signal?: AbortSignal): Promise<UserGroup[]> {
    return (await this.request<{ data: UserGroup[] }>('/api/metadata/admin/user-groups', { signal })).data
  }
  createUserGroup(input: UserGroupInput): Promise<UserGroup> {
    return this.request('/api/metadata/admin/user-groups', { method: 'POST', body: input })
  }
  updateUserGroup(id: string, input: UserGroupInput, etag: string): Promise<UserGroup> {
    return this.request(`/api/metadata/admin/user-groups/${encodeURIComponent(id)}`, { method: 'PUT', body: input, etag })
  }
  getPolicy(signal?: AbortSignal): Promise<PolicyRecord> { return this.request('/api/metadata/admin/policy', { signal }) }
  savePolicy(policy: PermissionPolicy, etag: string): Promise<PolicyRecord> {
    return this.request('/api/metadata/admin/policy', { method: 'PUT', body: policy, etag })
  }
  publishPolicy(etag: string): Promise<PolicyRecord> {
    return this.request('/api/metadata/admin/policy/publish', { method: 'POST', etag })
  }
  simulatePolicy(input: SimulationRequest, signal?: AbortSignal): Promise<SimulationResult> {
    return this.request('/api/metadata/admin/simulate', { method: 'POST', body: input, signal })
  }
  async listAudit(signal?: AbortSignal): Promise<AuditRecord[]> {
    return (await this.request<{ data: AuditRecord[] }>('/api/metadata/admin/audit', { signal })).data
  }

  private async listAll<T>(path: string, params: URLSearchParams, signal?: AbortSignal): Promise<T[]> {
    const data: T[] = []
    const cursors = new Set<string>()
    params.set('limit', '200')
    for (let page = 0; page < 100; page++) {
      const result = await this.request<Page<T>>(`${path}?${params}`, { signal })
      if (!isObject(result) || !Array.isArray(result.data) || typeof result.hasMore !== 'boolean') {
        throw new ApiError(502, 'invalid_response', '目录分页响应格式错误')
      }
      data.push(...result.data)
      if (data.length > 20000) throw new ApiError(422, 'pagination_budget', '目录超过当前读取预算，请缩小分类或类型范围')
      if (!result.hasMore) return data
      if (!result.nextCursor || cursors.has(result.nextCursor)) throw new ApiError(502, 'invalid_pagination', '目录续页标记缺失或重复，无法安全读取完整目录')
      cursors.add(result.nextCursor)
      params.set('after', result.nextCursor)
    }
    throw new ApiError(422, 'pagination_budget', '目录超过当前读取预算，请缩小分类或类型范围')
  }

  listCategories(signal?: AbortSignal): Promise<Organization[]> {
    return this.listAll('/api/metadata/categories', new URLSearchParams(), signal)
  }
  listGroups(categoryId: string, signal?: AbortSignal): Promise<Organization[]> {
    return this.listAll('/api/metadata/groups', new URLSearchParams({ categoryId }), signal)
  }
  listDefinitions(filters: DefinitionFilters = {}, signal?: AbortSignal): Promise<DefinitionRecord[]> {
    const params = new URLSearchParams()
    for (const [key, value] of Object.entries(filters)) if (value) params.set(key, value)
    return this.listAll('/api/metadata/definitions', params, signal)
  }
  getCurrent(id: string, signal?: AbortSignal): Promise<DefinitionRecord> {
    return this.request(`/api/metadata/definitions/${encodeURIComponent(id)}`, { signal })
  }
  getDraft(id: string, signal?: AbortSignal): Promise<DefinitionRecord> {
    return this.request(`/api/metadata/definitions/${encodeURIComponent(id)}/draft`, { signal })
  }
  async schemaLinks(schemaId: string, signal?: AbortSignal): Promise<SchemaLink[]> {
    return (await this.request<{ data: SchemaLink[] }>(`/api/metadata/schemas/${encodeURIComponent(schemaId)}/links`, { signal })).data
  }
  createCategory(name: string): Promise<Organization> {
    return this.request('/api/metadata/categories', { method: 'POST', body: { name } })
  }
  createGroup(categoryId: string, name: string): Promise<Organization> {
    return this.request('/api/metadata/groups', { method: 'POST', body: { categoryId, name } })
  }
  async listDataSources(signal?: AbortSignal, selectable = false): Promise<DataSource[]> {
    return (await this.request<Page<DataSource>>(`/api/data/sources${selectable ? '?view=select' : ''}`, { signal })).data
  }
  async listSourceVersions(signal?: AbortSignal): Promise<SourceVersionOption[]> {
    return (await this.request<Page<SourceVersionOption>>('/api/data/source-versions', { signal })).data
  }
  getSchemaStorageMapping(schemaId: string, signal?: AbortSignal): Promise<SchemaStorageMapping> {
    return this.request(`/api/data/schemas/${encodeURIComponent(schemaId)}/storage-mapping`, { signal })
  }
  createDataSource(input: DataSourceInput): Promise<DataSource> {
    return this.request('/api/data/sources', { method: 'POST', body: input })
  }
  getDataSource(id: string, signal?: AbortSignal): Promise<DataSource> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}`, { signal })
  }
  getSourceOverview(id: string, signal?: AbortSignal): Promise<SourceOverview> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/overview`, { signal })
  }
  queryDataSource(id: string, sql: string, etag: string, signal?: AbortSignal, limit?: number): Promise<SourceQueryResult> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/query`, { method: 'POST', body: { sql, ...(limit !== undefined ? { limit } : {}) }, etag, signal })
  }
  analyzeSourceSQL(id: string, sql: string, etag: string, signal?: AbortSignal, limit?: number): Promise<SourceSQLToolResult> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/sql/analyze`, { method: 'POST', body: { sql, ...(limit !== undefined ? { limit } : {}) }, etag, signal })
  }
  formatSourceSQL(id: string, sql: string, etag: string, signal?: AbortSignal, limit?: number): Promise<SourceSQLToolResult> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/sql/format`, { method: 'POST', body: { sql, ...(limit !== undefined ? { limit } : {}) }, etag, signal })
  }
  updateDataSource(id: string, input: DataSourceInput, etag: string): Promise<DataSource> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}`, { method: 'PUT', body: input, etag })
  }
  testDataSource(id: string, input?: DataSourceInput, etag?: string): Promise<SourceTestResult> {
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/test`, { method: 'POST', body: input, etag })
  }
  testDataSourceInput(input: DataSourceInput): Promise<SourceTestResult> {
    return this.request('/api/data/sources/test', { method: 'POST', body: input })
  }
  async listSourceTables(id: string, namespace = '', signal?: AbortSignal): Promise<SourceTableInfo[]> {
    const params = new URLSearchParams()
    if (namespace) params.set('namespace', namespace)
    return (await this.request<Page<SourceTableInfo>>(`/api/data/sources/${encodeURIComponent(id)}/tables?${params}`, { signal })).data
  }
  inspectSourceTable(id: string, table: string, namespace = '', signal?: AbortSignal): Promise<SourceTable> {
    const params = new URLSearchParams()
    if (namespace) params.set('namespace', namespace)
    return this.request(`/api/data/sources/${encodeURIComponent(id)}/tables/${encodeURIComponent(table)}?${params}`, { signal })
  }
  createDefinition(definition: DefinitionInput): Promise<DefinitionRecord> {
    return this.request('/api/metadata/definitions', { method: 'POST', body: definition })
  }
  saveDraft(id: string, definition: DefinitionInput, etag: string): Promise<DefinitionRecord> {
    return this.request(`/api/metadata/definitions/${encodeURIComponent(id)}/draft`, { method: 'PUT', body: definition, etag })
  }
  preview(id: string): Promise<Preview> {
    return this.request(`/api/metadata/definitions/${encodeURIComponent(id)}/preview`, { method: 'POST' })
  }
  publish(id: string, etag: string): Promise<Publication> {
    return this.request(`/api/metadata/definitions/${encodeURIComponent(id)}/publish`, { method: 'POST', etag })
  }
  getPublication(id: string, signal?: AbortSignal): Promise<Publication> {
    return this.request(`/api/metadata/publications/${encodeURIComponent(id)}`, { signal })
  }
  query(spec: QuerySpec, signal?: AbortSignal): Promise<QueryResult> {
    return this.request('/api/data/query', { method: 'POST', body: spec, signal })
  }
  queryContext(filters: { categoryId?: string; schemaId?: string; search?: string; cursor?: string; limit?: number }, signal?: AbortSignal): Promise<QueryContext> {
    const params = new URLSearchParams()
    for (const [key, value] of Object.entries(filters)) if (value !== undefined && value !== '') params.set(key, String(value))
    return this.request(`/api/data/query/context?${params}`, { signal })
  }
  analyzeOntologySQL(request: OntologySQLRequest, signal?: AbortSignal): Promise<OntologySQLAnalysis> {
    return this.request('/api/data/query/ontology-sql/analyze', { method: 'POST', body: request, signal })
  }
  completeOntologySQL(request: OntologySQLRequest, signal?: AbortSignal): Promise<OntologySQLCompletions> {
    return this.request('/api/data/query/ontology-sql/complete', { method: 'POST', body: request, signal })
  }
  formatOntologySQL(request: OntologySQLRequest, signal?: AbortSignal): Promise<OntologySQLFormat> {
    return this.request('/api/data/query/ontology-sql/format', { method: 'POST', body: request, signal })
  }
  queryOntologySQL(request: OntologySQLRequest, signal?: AbortSignal): Promise<OntologySQLResult> {
    return this.request('/api/data/query/ontology-sql', { method: 'POST', body: request, signal })
  }
  planOntologySQLPaths(request: QueryPathsRequest, signal?: AbortSignal): Promise<QueryPathsResult> {
    return this.request('/api/data/query/ontology-sql/paths', { method: 'POST', body: request, signal })
  }
  insertOntologySQLField(request: QueryFieldInsertionRequest, signal?: AbortSignal): Promise<QueryFieldInsertionResult> {
    return this.request('/api/data/query/ontology-sql/insert-field', { method: 'POST', body: request, signal })
  }
  listLinkInstances(linkId: string, direction: 'out' | 'in', instanceId: string, cursor?: string, signal?: AbortSignal): Promise<Page<LinkInstance>> {
    const params = new URLSearchParams({ direction, instanceId, limit: '100' })
    if (cursor) params.set('cursor', cursor)
    return this.request(`/api/data/links/${encodeURIComponent(linkId)}/instances?${params}`, { signal })
  }
  createLinkInstance(linkId: string, sourceId: string, targetId: string): Promise<LinkInstance> {
    return this.request(`/api/data/links/${encodeURIComponent(linkId)}/instances`, { method: 'POST', body: { sourceId, targetId } })
  }
  deleteLinkInstance(linkId: string, id: string, etag: string): Promise<void> {
    return this.request(`/api/data/links/${encodeURIComponent(linkId)}/instances/${encodeURIComponent(id)}`, { method: 'DELETE', etag })
  }
  getInstance(schemaId: string, id: string, signal?: AbortSignal): Promise<Instance> {
    return this.request(`/api/data/schemas/${encodeURIComponent(schemaId)}/instances/${encodeURIComponent(id)}`, { signal })
  }
  createInstance(schemaId: string, values: Values, idempotencyKey: string): Promise<Instance> {
    return this.request(`/api/data/schemas/${encodeURIComponent(schemaId)}/instances`, { method: 'POST', body: { values, idempotencyKey } })
  }
  patchInstance(schemaId: string, id: string, values: Values, etag: string): Promise<Instance> {
    return this.request(`/api/data/schemas/${encodeURIComponent(schemaId)}/instances/${encodeURIComponent(id)}`, { method: 'PATCH', body: { values }, etag })
  }
  deleteInstance(schemaId: string, id: string, etag: string): Promise<void> {
    return this.request(`/api/data/schemas/${encodeURIComponent(schemaId)}/instances/${encodeURIComponent(id)}`, { method: 'DELETE', etag })
  }
  metadataCapabilities(signal?: AbortSignal): Promise<MetadataCapabilities> { return this.request('/api/metadata/capabilities', { signal }) }
  dataCapabilities(signal?: AbortSignal): Promise<DataCapabilities> { return this.request('/api/data/capabilities', { signal }) }
}
