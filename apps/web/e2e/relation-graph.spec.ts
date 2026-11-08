import { test, expect, type Page } from '@playwright/test'
import type { Definition, DefinitionRecord, QuerySpec } from '../src/lib/types'

// Every API request is intercepted. These tests never access the development
// tenant, source database or real relationship facts.
async function fixture(page: Page) {
  const tenantId = 'relation-graph-fixture'
  const base = { tenantId, categoryId: 'knowledge', groupId: 'content' }
  const schema = (id: string, name: string, fieldId: string, fieldName: string): DefinitionRecord => ({
    status: 'active', etag: `${id}-current`, definition: {
      ...base, id, kind: 'schema', name,
      schema: { mode: 'schema_2_db', storageLayout: 'single', storageBinding: { dataSourceId: 'knowledge-source', table: `kb_${id}`, idColumn: 'id' }, properties: [{ id: fieldId, name: fieldName, dataType: 'string' }] },
    },
  })
  const current: DefinitionRecord[] = [
    schema('notes', '笔记', 'title', '标题'),
    schema('references', '引用记录', 'citation', '原始引用'),
    schema('tags', '标签', 'label', '标签名称'),
    { status: 'active', etag: 'note-references-current', definition: {
      ...base, id: 'note-references', kind: 'link', name: '笔记产生引用',
      link: { sourceSchemaId: 'notes', targetSchemaId: 'references', forwardName: '产生引用', reverseName: '源自笔记', cardinality: '1:N', onDelete: 'restrict', storageBinding: { mode: 'foreign_key', dataSourceId: 'knowledge-source', foreignKeySide: 'target', foreignKeyColumn: 'source_note_id' } },
    } },
    { status: 'active', etag: 'note-tags-current', definition: {
      ...base, groupId: 'organization', id: 'note-tags', kind: 'link', name: '笔记标记标签',
      link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'junction', dataSourceId: 'knowledge-source', table: 'kb_note_tags', sourceColumn: 'note_id', targetColumn: 'tag_id' } },
    } },
  ]
  const draftOnly: DefinitionRecord = { status: 'draft', etag: 'related-notes-draft', definition: {
    ...base, id: 'related-notes', kind: 'link', name: '笔记关联笔记',
    link: { sourceSchemaId: 'notes', targetSchemaId: 'notes', forwardName: '关联笔记', reverseName: '被笔记关联', cardinality: 'N:M', onDelete: 'restrict' },
  } }
  const drafts = new Map<string, DefinitionRecord>([['related-notes', draftOnly]])
  const writes: { method: string; path: string; definition: Definition; csrf?: string; etag?: string }[] = []
  const publications: { id: string; csrf?: string; etag?: string }[] = []
  const queries: QuerySpec[] = []
  const unhandled: string[] = []
  const state = { revoked: false, denyPreview: false, paginatePreview: false }
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'author', tenantId, username: 'author', displayName: '模型作者', enabled: true, administrator: false, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', links: true, maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'knowledge', tenantId, name: '个人知识库' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [
      { id: 'content', tenantId, categoryId: 'knowledge', name: '内容资产' },
      { id: 'organization', tenantId, categoryId: 'knowledge', name: '知识组织' },
    ], hasMore: false })
    if (path === '/api/metadata/definitions' && method === 'GET') return reply({ data: state.revoked ? [] : url.searchParams.get('view') === 'draft' ? [...drafts.values()] : current, hasMore: false })
    if (path === '/api/metadata/definitions' && method === 'POST') {
      const input = request.postDataJSON() as Definition
      const definition = { ...input, id: `created-link-${writes.length + 1}`, tenantId }
      writes.push({ method, path, definition: structuredClone(definition), csrf: request.headers()['x-csrf-token'] })
      const record = { definition, status: 'draft', etag: `saved-${writes.length}` }
      drafts.set(definition.id, record)
      return reply(record, 201)
    }
    const operation = path.match(/^\/api\/metadata\/definitions\/([^/]+)\/(preview|publish)$/)
    if (operation) {
      if (operation[2] === 'preview') return reply({ status: 'ready', operationId: 'preview', statements: [] })
      const draft = drafts.get(operation[1])
      if (!draft) return reply({ error: { code: 'not_found', message: '未找到待发布草稿' } }, 404)
      publications.push({ id: operation[1], csrf: request.headers()['x-csrf-token'], etag: request.headers()['if-match'] })
      const active = { ...draft, status: 'active', etag: `published-${publications.length}` }
      const index = current.findIndex((entry) => entry.definition.id === operation[1])
      if (index >= 0) current[index] = active
      else current.push(active)
      drafts.delete(operation[1])
      return reply({ id: `publication-${publications.length}`, tenantId, definitionId: operation[1], status: 'succeeded' })
    }
    const definition = path.match(/^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/)
    if (definition) {
      if (state.revoked) return reply({ error: { code: 'forbidden', message: '定义权限已撤销' } }, 403)
      if (method === 'PUT' && definition[2]) {
        const input = request.postDataJSON() as Definition
        writes.push({ method, path, definition: structuredClone(input), csrf: request.headers()['x-csrf-token'], etag: request.headers()['if-match'] })
        const record = { definition: input, status: 'draft', etag: `saved-${writes.length}` }
        drafts.set(definition[1], record)
        return reply(record)
      }
      const record = definition[2] ? drafts.get(definition[1]) : current.find((entry) => entry.definition.id === definition[1])
      return record ? reply(record) : reply({ error: { code: 'not_found', message: '未找到草稿' } }, 404)
    }
    if (path === '/api/data/query' && method === 'POST') {
      const spec = request.postDataJSON() as QuerySpec
      queries.push(structuredClone(spec))
      if (state.denyPreview && spec.via) return reply({ error: { code: 'forbidden', message: '关联访问权限已撤销' } }, 403)
      const record = current.find((entry) => entry.definition.id === spec.from.schemaId)!
      const values = spec.from.schemaId === 'notes' ? { title: '关系建模笔记' } : spec.from.schemaId === 'references' ? { citation: spec.page?.cursor ? '[[Ontology 第二页]]' : '[[Ontology]]' } : { label: '知识图谱' }
      const hasMore = state.paginatePreview && !!spec.via && !spec.page?.cursor
      return reply({ fields: record.definition.schema!.properties.filter((field) => spec.select.includes(field.id)), data: [{ id: `${record.definition.id}-${spec.page?.cursor ? 2 : 1}`, schemaId: record.definition.id, etag: 'instance-current', values }], hasMore, ...(hasMore ? { nextCursor: 'related-next-page' } : {}) })
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { state, current, drafts, writes, publications, queries, unhandled }
}

test('分类关系图选择关系并在侧栏保存、预览和发布，保留并发与CSRF凭证', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models')
  await page.getByRole('button', { name: '关系图', exact: true }).click()
  await expect(page).toHaveURL(/#models\?view=graph$/)
  const graph = page.getByRole('region', { name: '关系建模画布', exact: true })
  await expect(graph).toBeVisible()
  await expect(graph.getByRole('button', { name: '选择关系：笔记产生引用（已生效）', exact: true })).toBeVisible()
  await expect(graph.getByText('源自笔记', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: '编辑关系：笔记产生引用', exact: true }).click()
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('笔记产生引用')
  await expect(editor.getByRole('combobox', { name: /^来源模型/ })).toBeDisabled()
  await expect(editor.getByRole('combobox', { name: /^目标模型/ })).toBeDisabled()
  await expect(editor.getByRole('combobox', { name: /^关系基数/ })).toBeDisabled()
  await editor.getByLabel('业务名称', { exact: true }).fill('笔记引用关系')
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ method: 'PUT', csrf: 'fixture-csrf', etag: '"note-references-current"', definition: { id: 'note-references', name: '笔记引用关系', link: { sourceSchemaId: 'notes', targetSchemaId: 'references', cardinality: '1:N', storageBinding: { mode: 'foreign_key', foreignKeyColumn: 'source_note_id' } } } })
  await editor.getByRole('button', { name: '预览计划', exact: true }).click()
  await expect(editor.getByText(/没有需要执行的存储语句/)).toBeVisible()
  await editor.getByRole('button', { name: '发布定义', exact: true }).click()
  await expect.poll(() => state.publications.length).toBe(1)
  expect(state.publications[0]).toMatchObject({ id: 'note-references', csrf: 'fixture-csrf', etag: '"saved-1"' })
  await expect(editor.getByText('发布成功，当前定义已生效。', { exact: true })).toBeVisible()
  await expect(graph.getByRole('button', { name: '选择关系：笔记引用关系（已生效）', exact: true })).toBeVisible()
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

test('关系图显式查看草稿、自关联及新建关系，390px可完成配置', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models?view=graph')
  const graph = page.getByRole('region', { name: '关系建模画布', exact: true })
  await expect(graph).toBeVisible()
  await expect(graph.getByRole('button', { name: '选择关系：笔记关联笔记（草稿）', exact: true })).toHaveCount(0)
  await page.getByRole('checkbox', { name: '显示草稿', exact: true }).check()
  await expect(graph.getByRole('button', { name: '选择关系：笔记关联笔记（草稿）', exact: true })).toBeVisible()
  const source = graph.getByLabel('笔记的关系来源连接点', { exact: true })
  const target = graph.getByLabel('标签的关系目标连接点', { exact: true })
  const sourceBox = await source.boundingBox()
  const targetBox = await target.boundingBox()
  expect(sourceBox).not.toBeNull()
  expect(targetBox).not.toBeNull()
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, sourceBox!.y + sourceBox!.height / 2)
  await page.mouse.down()
  await page.mouse.move(targetBox!.x + targetBox!.width / 2, targetBox!.y + targetBox!.height / 2, { steps: 16 })
  await page.mouse.up()
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByRole('combobox', { name: /^来源模型/ })).toHaveValue('notes')
  await expect(editor.getByRole('combobox', { name: /^目标模型/ })).toHaveValue('tags')
  await editor.getByLabel('业务名称', { exact: true }).fill('笔记推荐标签')
  await editor.getByRole('combobox', { name: /^分组/ }).selectOption('organization')
  await editor.getByRole('combobox', { name: /^来源模型/ }).selectOption('notes')
  await editor.getByRole('combobox', { name: /^目标模型/ }).selectOption('tags')
  await editor.getByLabel('正向业务名称', { exact: true }).fill('推荐标签')
  await editor.getByLabel('反向业务名称', { exact: true }).fill('推荐笔记')
  await editor.getByRole('combobox', { name: /^关系基数/ }).selectOption('N:M')
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ method: 'POST', csrf: 'fixture-csrf', definition: { name: '笔记推荐标签', categoryId: 'knowledge', groupId: 'organization', link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '推荐标签', reverseName: '推荐笔记', cardinality: 'N:M' } } })
  await expect(editor.getByRole('tab', { name: '业务定义', exact: true })).toBeVisible()
  await editor.getByRole('tab', { name: '数据映射', exact: true }).click()
  await expect(editor.getByLabel('关系存储方式', { exact: true })).toHaveValue('')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

test('数据预览只在显式操作后按当前态正反查询，撤权清理旧结果', async ({ page }) => {
  const state = await fixture(page)
  const active = state.current.find((entry) => entry.definition.id === 'note-references')!
  state.state.paginatePreview = true
  state.drafts.set('note-references', { ...active, status: 'draft', etag: 'renamed-draft', definition: { ...active.definition, name: '待发布引用关系', link: { ...active.definition.link!, forwardName: '草稿产生引用' } } })
  await page.goto('/#models/note-references')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('待发布引用关系')
  await editor.getByRole('tab', { name: '数据预览', exact: true }).click()
  await expect(editor.getByText('草稿尚未发布；此处查询当前生效关系和对象的数据。', { exact: true })).toBeVisible()
  await expect(editor.getByRole('combobox', { name: /^查询方向/ }).locator('option:checked')).toHaveText('正向：笔记 → 产生引用 → 引用记录')
  expect(state.queries).toEqual([])
  await expect(editor.getByRole('button', { name: '查询关联记录', exact: true })).toBeDisabled()
  await editor.getByRole('button', { name: '读取起点记录', exact: true }).click()
  await editor.getByRole('combobox', { name: /^起点记录/ }).selectOption('notes-1')
  await editor.getByRole('button', { name: '查询关联记录', exact: true }).click()
  const result = editor.getByRole('table', { name: '关系数据预览', exact: true })
  await expect(result.getByRole('cell', { name: '[[Ontology]]', exact: true })).toBeVisible()
  expect(state.queries[0]).toMatchObject({ from: { schemaId: 'notes' }, page: { limit: 10 } })
  expect(state.queries[1]).toMatchObject({ from: { schemaId: 'references' }, via: { linkId: 'note-references', direction: 'out', instanceId: 'notes-1' }, page: { limit: 5 } })
  await editor.getByRole('button', { name: '关联下一页', exact: true }).click()
  await expect(result.getByRole('cell', { name: '[[Ontology 第二页]]', exact: true })).toBeVisible()
  expect(state.queries[2]).toMatchObject({ from: { schemaId: 'references' }, via: { linkId: 'note-references', direction: 'out', instanceId: 'notes-1' }, page: { limit: 5, cursor: 'related-next-page' } })
  await editor.getByRole('combobox', { name: /^查询方向/ }).selectOption('in')
  await expect(result).toHaveCount(0)
  await editor.getByRole('button', { name: '读取起点记录', exact: true }).click()
  await editor.getByRole('combobox', { name: /^起点记录/ }).selectOption('references-1')
  await editor.getByRole('button', { name: '查询关联记录', exact: true }).click()
  await expect(result.getByRole('cell', { name: '关系建模笔记', exact: true })).toBeVisible()
  expect(state.queries.find((spec) => spec.via?.direction === 'in')).toMatchObject({ from: { schemaId: 'notes' }, via: { linkId: 'note-references', direction: 'in', instanceId: 'references-1' } })
  state.state.denyPreview = true
  await editor.getByRole('button', { name: '查询关联记录', exact: true }).click()
  await expect(editor.getByRole('alert')).toContainText('关联访问权限已撤销')
  await expect(result).toHaveCount(0)
  await expect(editor.getByRole('combobox', { name: /^起点记录/ })).toHaveCount(0)
  expect(state.writes).toEqual([])
  expect(state.unhandled).toEqual([])
})

test('关系详情显式展示外部只读映射并在目录撤权后清理关系图', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models/note-references')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(page.getByRole('region', { name: '关系建模画布', exact: true })).toBeVisible()
  await editor.getByRole('tab', { name: '数据映射', exact: true }).click()
  await expect(editor.getByLabel('关系存储方式', { exact: true })).toBeDisabled()
  await expect(editor.getByLabel('关系存储方式', { exact: true })).toHaveValue('foreign_key')
  await expect(editor.getByLabel('外键列', { exact: true })).toBeDisabled()
  await expect(editor.getByLabel('外键列', { exact: true })).toHaveValue('source_note_id')
  await expect(editor.getByText(/读取已有数据库关系，不修改源表或关系数据/)).toBeVisible()
  await page.getByRole('button', { name: '打开分类关系图', exact: true }).click()
  const graph = page.getByRole('region', { name: '关系建模画布', exact: true })
  await expect(graph.getByRole('button', { name: '选择关系：笔记产生引用（已生效）', exact: true })).toBeVisible()
  state.state.revoked = true
  await page.getByRole('button', { name: '刷新目录', exact: true }).click()
  await expect(graph.getByRole('button', { name: '选择关系：笔记产生引用（已生效）', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '编辑关系：笔记产生引用', exact: true })).toHaveCount(0)
  expect(state.writes).toEqual([])
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
