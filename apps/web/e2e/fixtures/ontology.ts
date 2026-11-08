import type { Page } from '@playwright/test'
import type { DefinitionRecord, Organization } from '../../src/lib/types'

// Every service request is intercepted. The fixture never uses a development
// account, queries business records, runs a callable or writes real definitions.
export async function ontologyFixture(page: Page) {
  const tenantId = 'ontology-explorer-fixture'
  const base = { tenantId, categoryId: 'knowledge', groupId: 'content' }
  const categories: Organization[] = [
    { id: 'knowledge', tenantId, name: '个人知识库' },
    { id: 'other', tenantId, name: '公共知识' },
  ]
  const groups: Organization[] = [
    { id: 'content', tenantId, categoryId: 'knowledge', name: '内容资产' },
    { id: 'organization', tenantId, categoryId: 'knowledge', name: '知识组织' },
    { id: 'computation', tenantId, categoryId: 'knowledge', name: '计算能力' },
    { id: 'shared', tenantId, categoryId: 'other', name: '公共能力' },
  ]
  const schema = (id: string, name: string): DefinitionRecord => ({ status: 'active', etag: `${id}-current`, definition: {
    ...base, id, kind: 'schema', name, description: id === 'notes' ? '记录知识内容与状态' : '整理知识主题',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: `${id}-title`, name: id === 'notes' ? '标题' : '标签名称', dataType: 'text' },
      ...(id === 'notes' ? [{ id: 'notes-status', name: '状态', dataType: 'string' as const, enumId: 'status' }] : []),
    ] },
  } })
  const link = (id: string, name: string, targetSchemaId: string, forwardName: string, reverseName: string): DefinitionRecord => ({ status: 'active', etag: `${id}-current`, definition: {
    ...base, groupId: 'organization', id, kind: 'link', name,
    link: { sourceSchemaId: 'notes', targetSchemaId, forwardName, reverseName, cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'junction', table: `kb_${id}`, sourceColumn: 'source_id', targetColumn: 'target_id' } },
  } })
  const current: DefinitionRecord[] = [
    schema('notes', '笔记'), schema('tags', '标签'),
    link('note-tags', '笔记标记标签', 'tags', '标记标签', '标记笔记'),
    link('recommended-tags', '笔记推荐标签', 'tags', '推荐标签', '被笔记推荐'),
    link('note-reference', '笔记引用笔记', 'notes', '引用笔记', '被笔记引用'),
    { status: 'active', etag: 'status-current', definition: { ...base, groupId: 'organization', id: 'status', kind: 'enum', name: '笔记状态', enum: { values: [{ code: '9007199254740993', label: '已整理' }, { code: 'old', label: '旧状态', deprecated: true }] } } },
    { status: 'active', etag: 'summarize-current', definition: { ...base, groupId: 'computation', id: 'summarize', kind: 'action', name: '生成笔记摘要', action: { sideEffects: 'none', targetSchemaIds: ['notes'], functionId: 'summary-function', input: [{ id: 'input-title', name: '标题', dataType: 'string' }], output: [{ id: 'output-summary', name: '摘要', dataType: 'longtext' }] } } },
    { status: 'active', etag: 'normalize-current', definition: { ...base, groupId: 'computation', id: 'normalize', kind: 'function', name: '规范标题', function: { sideEffects: 'none', runtime: { language: 'javascript', entrypoint: 'main', source: 'function main(input) { /* PRIVATE_SOURCE_MARKER */ return input; }', timeoutSeconds: 5 } } } },
    { status: 'active', etag: 'summary-function-current', definition: { ...base, categoryId: 'other', groupId: 'shared', id: 'summary-function', kind: 'function', name: '摘要函数', function: { sideEffects: 'none', input: [{ id: 'title', name: '标题', dataType: 'string' }], output: [{ id: 'summary', name: '摘要', dataType: 'longtext' }], runtime: { language: 'python', entrypoint: 'main', source: 'def main(input):\n    return input # PRIVATE_SOURCE_MARKER', timeoutSeconds: 5 } } } },
    { ...schema('teams', '团队'), definition: { ...schema('teams', '团队').definition, categoryId: 'other', groupId: 'shared' } },
  ]
  const draft: DefinitionRecord = { ...schema('draft-only', '未发布的秘密模型'), status: 'draft', etag: 'draft-only' }
  const state = {
    current, categories, groups,
    denyKnowledge: false,
    delayKnowledge: undefined as Promise<void> | undefined,
    heldKnowledge: 0,
    directoryRequests: [] as { categoryId: string | null; view: string | null; after: string | null }[],
    definitionReads: [] as string[],
    businessRequests: [] as string[],
    unexpected: [] as string[],
    errors: [] as string[],
  }
  page.on('pageerror', (error) => state.errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = async (json: unknown, status = 200) => { await route.fulfill({ status, json }).catch(() => {}) }
    if (method !== 'GET') {
      state.businessRequests.push(`${method} ${path}`)
      return reply({ error: { code: 'readonly_fixture', message: '只读浏览不应提交业务请求' } }, 405)
    }
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '本体浏览用户', enabled: true, administrator: false, roleIds: [], etag: 'reader-current' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'], links: true })
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python', 'javascript', 'go'], synchronous: true })
    if (path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: categories, hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: groups.filter((group) => !url.searchParams.get('categoryId') || group.categoryId === url.searchParams.get('categoryId')), hasMore: false })
    if (path === '/api/metadata/definitions') {
      const categoryId = url.searchParams.get('categoryId'), view = url.searchParams.get('view'), after = url.searchParams.get('after')
      state.directoryRequests.push({ categoryId, view, after })
      if (categoryId === 'knowledge' && state.denyKnowledge) return reply({ error: { code: 'forbidden', message: '本体目录权限已撤销' } }, 403)
      const records = (view === 'draft' ? [draft] : current).filter((record) => !categoryId || record.definition.categoryId === categoryId)
      const start = after ? Number(after.replace('page-', '')) : 0
      const data = structuredClone(records.slice(start, start + 3)), hasMore = start + 3 < records.length
      if (categoryId === 'knowledge' && state.delayKnowledge) { state.heldKnowledge++; await state.delayKnowledge }
      return reply({ data, hasMore, ...(hasMore ? { nextCursor: `page-${start + 3}` } : {}) })
    }
    const definition = /^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/.exec(path)
    if (definition) {
      const id = decodeURIComponent(definition[1])
      state.definitionReads.push(id)
      const record = definition[2] ? id === draft.definition.id ? draft : undefined : current.find((item) => item.definition.id === id)
      return record ? reply(structuredClone(record)) : reply({ error: { code: 'not_found', message: '当前定义不存在' } }, 404)
    }
    state.unexpected.push(`${method} ${path}`)
    return reply({ error: { code: 'unexpected_request', message: '隔离用例没有定义此接口' } }, 404)
  })
  return state
}
