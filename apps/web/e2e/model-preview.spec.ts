import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, QueryResult, QuerySpec, SchemaLink } from '../src/lib/types'

// Every API request stays inside this fixture. Preview must never write business data.
async function fixture(page: Page) {
  const tenantId = 'model-preview-fixture'
  const base = { tenantId, groupId: 'knowledge-group', categoryId: 'knowledge' }
  const current: DefinitionRecord[] = [
    { status: 'active', etag: 'notes-current', definition: { ...base, id: 'notes', kind: 'schema', name: '笔记', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: 'title', name: '标题', dataType: 'string' }, { id: 'body', name: '正文', dataType: 'longtext' },
    ] } } },
    { status: 'active', etag: 'tags-current', definition: { ...base, id: 'tags', kind: 'schema', name: '标签', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: 'label', name: '标签名称', dataType: 'string' },
    ] } } },
    { status: 'active', etag: 'collections-current', definition: { ...base, id: 'collections', kind: 'schema', name: '知识集合', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: 'collection-name', name: '集合名称', dataType: 'string' },
    ] } } },
    { status: 'active', etag: 'note-tags-current', definition: { ...base, id: 'note-tags', kind: 'link', name: '笔记标签', link: {
      sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'managed' },
    } } },
    { status: 'active', etag: 'collection-notes-current', definition: { ...base, id: 'collection-notes', kind: 'link', name: '集合笔记', link: {
      sourceSchemaId: 'collections', targetSchemaId: 'notes', forwardName: '收录笔记', reverseName: '所属集合', cardinality: '1:N', onDelete: 'restrict', storageBinding: { mode: 'managed' },
    } } },
    { status: 'active', etag: 'unbound-current', definition: { ...base, id: 'unbound', kind: 'link', name: '推荐标签', link: {
      sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '推荐标签', reverseName: '推荐笔记', cardinality: 'N:M', onDelete: 'restrict',
    } } },
  ]
  const links: SchemaLink[] = [
    { linkRef: { id: 'note-tags' }, direction: 'out', sourceSchemaId: 'notes', targetSchemaId: 'tags', name: '标记标签', cardinality: 'N:M' },
    { linkRef: { id: 'collection-notes' }, direction: 'in', sourceSchemaId: 'notes', targetSchemaId: 'collections', name: '所属集合', cardinality: 'N:1' },
    { linkRef: { id: 'unbound' }, direction: 'out', sourceSchemaId: 'notes', targetSchemaId: 'tags', name: '推荐标签', cardinality: 'N:M' },
  ]
  const queries: QuerySpec[] = []
  const writes: string[] = []
  const unhandled: string[] = []
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (method !== 'GET' && !(path === '/api/data/query' && method === 'POST')) {
      writes.push(`${method} ${path}`)
      return reply({ error: { code: 'read_only_fixture', message: '预览不允许业务写入' } }, 405)
    }
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '预览读者', administrator: false, enabled: true, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'], links: true })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'knowledge', tenantId, name: '知识管理' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'knowledge-group', tenantId, categoryId: 'knowledge', name: '知识目录' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : current, hasMore: false })
    if (path === '/api/metadata/schemas/notes/links') return reply({ data: links, hasMore: false })
    const target = path.match(/^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/)
    if (target) {
      const record = target[2] ? undefined : current.find((entry) => entry.definition.id === target[1])
      return record ? reply(record) : reply({ error: { code: 'not_found', message: '未找到草稿' } }, 404)
    }
    if (path === '/api/data/query' && method === 'POST') {
      const spec = request.postDataJSON() as QuerySpec
      queries.push(structuredClone(spec))
      const definition = current.find((entry) => entry.definition.id === spec.from.schemaId)?.definition
      if (!definition?.schema || spec.via?.linkId === 'unbound') return reply({ error: { code: 'invalid_query', message: '未映射关系不可查询' } }, 400)
      const fields = spec.select.map((id) => definition.schema!.properties.find((property) => property.id === id)!).filter(Boolean)
      let rows: { id: string; values: Record<string, string> }[]
      if (!spec.via) rows = spec.page.cursor ? [{ id: 'note-3', values: { title: '第三篇笔记', body: '第三篇正文' } }] : [
        { id: 'note-1', values: { title: '数据库建模', body: '第一篇正文' } }, { id: 'note-2', values: { title: '查询设计', body: '第二篇正文' } },
      ]
      else if (spec.via.linkId === 'note-tags') rows = [{ id: `tag-${spec.via.instanceId}-${spec.page.cursor ? '2' : '1'}`, values: { label: `${spec.via.instanceId} 标签${spec.page.cursor ? '第二页' : '第一页'}` } }]
      else rows = [{ id: `collection-${spec.via.instanceId}-${spec.page.cursor ? '2' : '1'}`, values: { 'collection-name': `${spec.via.instanceId} 集合${spec.page.cursor ? '第二页' : '第一页'}` } }]
      const result: QueryResult = {
        fields,
        data: rows.map((row) => ({ id: row.id, schemaId: definition.id, etag: 'row-current', values: Object.fromEntries(fields.map((field) => [field.id, row.values[field.id]])) })),
        hasMore: !spec.page.cursor,
        ...(!spec.page.cursor ? { nextCursor: spec.via ? `${spec.via.linkId}-${spec.via.instanceId}-page-2` : 'notes-page-2' } : {}),
      }
      return reply(result)
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { queries, writes, unhandled }
}

test('数据预览自动读取当前数据，按记录查询双向 Link 并独立分页且保持只读', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models/notes')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByRole('tab', { name: '数据预览', exact: true })).toBeVisible()
  expect(state.queries).toEqual([])
  await editor.getByRole('tab', { name: '数据预览', exact: true }).click()
  const preview = editor.getByRole('region', { name: '模型数据预览', exact: true })
  await expect(preview.getByRole('table', { name: '对象数据预览', exact: true })).toBeVisible()
  const out = preview.getByRole('region', { name: '关联预览：标记标签（正向）', exact: true })
  const incoming = preview.getByRole('region', { name: '关联预览：所属集合（反向）', exact: true })
  await expect(out.getByRole('cell', { name: 'note-1 标签第一页', exact: true })).toBeVisible()
  await expect(incoming.getByRole('cell', { name: 'note-1 集合第一页', exact: true })).toBeVisible()
  expect(state.queries.find((query) => !query.via)).toEqual({ from: { schemaId: 'notes' }, select: ['title'], page: { limit: 10 } })
  expect(state.queries.find((query) => query.via?.linkId === 'note-tags')).toEqual({ from: { schemaId: 'tags' }, via: { linkId: 'note-tags', direction: 'out', instanceId: 'note-1' }, select: ['label'], page: { limit: 5 } })
  expect(state.queries.find((query) => query.via?.linkId === 'collection-notes')).toEqual({ from: { schemaId: 'collections' }, via: { linkId: 'collection-notes', direction: 'in', instanceId: 'note-1' }, select: ['collection-name'], page: { limit: 5 } })
  const unbound = preview.getByRole('region', { name: '关联预览：推荐标签（正向）', exact: true })
  await expect(unbound.getByText('尚未配置关系数据映射', { exact: true })).toBeVisible()
  await expect(unbound.getByRole('link', { name: '配置“推荐标签”', exact: true })).toHaveAttribute('href', '#models/unbound')
  expect(state.queries.filter((query) => query.via?.linkId === 'unbound')).toEqual([])

  await out.getByRole('button', { name: '关联下一页', exact: true }).click()
  await expect(out.getByRole('cell', { name: 'note-1 标签第二页', exact: true })).toBeVisible()
  await expect(incoming.getByRole('cell', { name: 'note-1 集合第一页', exact: true })).toBeVisible()
  expect(state.queries.at(-1)?.page).toEqual({ limit: 5, cursor: 'note-tags-note-1-page-2' })
  await incoming.getByRole('button', { name: '关联下一页', exact: true }).click()
  await expect(incoming.getByRole('cell', { name: 'note-1 集合第二页', exact: true })).toBeVisible()
  expect(state.queries.at(-1)?.page).toEqual({ limit: 5, cursor: 'collection-notes-note-1-page-2' })
  await expect(preview.getByRole('table', { name: '对象数据预览', exact: true }).getByRole('cell', { name: '数据库建模', exact: true })).toBeVisible()

  await preview.getByRole('button', { name: '预览记录 note-2', exact: true }).click()
  await expect(out.getByRole('cell', { name: 'note-2 标签第一页', exact: true })).toBeVisible()
  await expect(incoming.getByRole('cell', { name: 'note-2 集合第一页', exact: true })).toBeVisible()
  await expect(preview.getByText('note-1 标签第二页', { exact: true })).toHaveCount(0)
  expect([...new Set(state.queries.filter((query) => query.via?.instanceId === 'note-2').map((query) => query.via?.linkId))].sort()).toEqual(['collection-notes', 'note-tags'])
  expect(state.queries.filter((query) => query.via?.instanceId === 'note-2').every((query) => !query.page.cursor)).toBe(true)
  await preview.getByRole('button', { name: '预览下一页', exact: true }).click()
  await expect(preview.getByRole('button', { name: '预览记录 note-3', exact: true })).toBeVisible()
  await expect(out.getByRole('cell', { name: 'note-3 标签第一页', exact: true })).toBeVisible()
  expect(state.queries.find((query) => !query.via && query.page.cursor)?.page).toEqual({ limit: 10, cursor: 'notes-page-2' })
  await expect(preview.getByRole('button', { name: '预览下一页', exact: true })).toBeDisabled()
  await expect(preview.getByRole('button', { name: '预览上一页', exact: true })).toBeEnabled()
  await expect(preview.getByRole('button', { name: /新增|保存|删除|创建关系/ })).toHaveCount(0)
  expect(state.writes).toEqual([])
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

test('数据和关联预览在390px保持页面宽度，字段调整不会写入或自动请求数据', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const state = await fixture(page)
  await page.goto('/#models/notes')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await editor.getByRole('tab', { name: '数据预览', exact: true }).click()
  const preview = editor.getByRole('region', { name: '模型数据预览', exact: true })
  await expect(preview.getByRole('cell', { name: 'note-1 标签第一页', exact: true })).toBeVisible()
  await expect(preview.getByRole('cell', { name: 'note-1 集合第一页', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await preview.locator('summary').filter({ hasText: /^预览字段/ }).click()
  const queryCount = state.queries.length
  await preview.getByLabel('预览字段：正文', { exact: true }).check()
  expect(state.queries).toHaveLength(queryCount)
  await expect(preview.getByRole('table', { name: '对象数据预览', exact: true })).toHaveCount(0)
  await expect(preview.getByText('选择上方记录后，在这里预览它的关联数据。', { exact: true })).toBeVisible()
  await preview.getByRole('button', { name: '加载预览', exact: true }).click()
  await expect(preview.getByRole('table', { name: '对象数据预览', exact: true }).getByRole('cell', { name: '第一篇正文', exact: true })).toBeVisible()
  expect(state.queries.filter((query) => !query.via).at(-1)?.select).toEqual(['title', 'body'])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(state.writes).toEqual([])
  expect(state.unhandled).toEqual([])
})
