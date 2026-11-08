import { test, expect, type Page } from '@playwright/test'
import type { Definition, DefinitionRecord, LinkInstance, QuerySpec } from '../src/lib/types'

// This UI contract test intercepts every API request and never touches the
// user's database. Real SQL and authorization are covered by Data integration.
async function fixture(page: Page) {
  const tenantId = 'link-runtime-fixture'
  const base = { tenantId, categoryId: 'knowledge', groupId: 'group' }
  const current: DefinitionRecord[] = [
    { status: 'active', etag: 'notes-current', definition: { ...base, id: 'notes', kind: 'schema', name: '笔记', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'title', name: '标题', dataType: 'string' }] } } },
    { status: 'active', etag: 'tags-current', definition: { ...base, id: 'tags', kind: 'schema', name: '标签', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'label', name: '标签名称', dataType: 'string' }] } } },
    { status: 'active', etag: 'link-current', definition: { ...base, id: 'note-tags', kind: 'link', name: '笔记标记标签', link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict' } } },
  ]
  const drafts = new Map<string, DefinitionRecord>()
  const queries: QuerySpec[] = []
  const writes: Definition[] = []
  const creates: { sourceId: string; targetId: string }[] = []
  const deletes: string[] = []
  let edges: LinkInstance[] = []
  const unhandled: string[] = []
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
    if (path === '/api/data/query/context') return reply({ models: [] })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'knowledge', tenantId, name: '个人知识库' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId: 'knowledge', name: '知识结构' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [...drafts.values()] : current, hasMore: false })
    if (path === '/api/metadata/definitions/note-tags/preview') return reply({ status: 'ready', operationId: 'preview', statements: ['CREATE TABLE platform_link (...)'] })
    if (path === '/api/metadata/definitions/note-tags/publish') {
      const draft = drafts.get('note-tags')!
      current[2] = { ...draft, status: 'active', etag: 'link-published' }; drafts.delete('note-tags')
      return reply({ id: 'publication', tenantId, definitionId: 'note-tags', status: 'succeeded' })
    }
    const definition = path.match(/^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/)
    if (definition) {
      if (method === 'PUT' && definition[2]) {
        const body = request.postDataJSON() as Definition
        writes.push(structuredClone(body))
        const record = { definition: body, etag: 'draft-current', status: 'draft' }
        drafts.set(definition[1], record)
        return reply(record)
      }
      const record = definition[2] ? drafts.get(definition[1]) : current.find((entry) => entry.definition.id === definition[1])
      return record ? reply(record) : reply({ error: { code: 'not_found', message: '未找到草稿' } }, 404)
    }
    const links = path.match(/^\/api\/metadata\/schemas\/(notes|tags)\/links$/)
    if (links) return reply({ data: [{ linkRef: { id: 'note-tags' }, direction: links[1] === 'notes' ? 'out' : 'in', name: links[1] === 'notes' ? '标记标签' : '标记笔记', sourceSchemaId: links[1], targetSchemaId: links[1] === 'notes' ? 'tags' : 'notes', cardinality: 'N:M' }] })
    if (path === '/api/data/query') {
      const spec = request.postDataJSON() as QuerySpec
      queries.push(structuredClone(spec))
      const schema = current.find((entry) => entry.definition.id === spec.from.schemaId)!.definition.schema!
      const fields = schema.properties.filter((property) => spec.select.includes(property.id))
      const instance = spec.from.schemaId === 'notes' ? { id: 'note-1', schemaId: 'notes', etag: 'note-current', values: { title: '关系数据库建模' } } : { id: 'tag-1', schemaId: 'tags', etag: 'tag-current', values: { label: '知识图谱' } }
      return reply({ fields, data: !spec.via || edges.length ? [instance] : [], hasMore: false })
    }
    if (path === '/api/data/links/note-tags/instances') {
      if (method === 'POST') {
        expect(request.headers()['x-csrf-token']).toBe('fixture-csrf')
        const body = request.postDataJSON() as { sourceId: string; targetId: string }
        creates.push(body)
        const edge = { ...body, id: 'edge-1', linkId: 'note-tags', etag: 'edge-current' }
        edges = [edge]
        return reply(edge, 201)
      }
      return reply({ data: edges, hasMore: false })
    }
    if (method === 'DELETE' && path === '/api/data/links/note-tags/instances/edge-1') {
      deletes.push(request.headers()['if-match'])
      edges = []
      return route.fulfill({ status: 204 })
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { queries, writes, creates, deletes, unhandled }
}

test('配置关系存储后发布，正反向查询与增删关系，并适配390px', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models/note-tags')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByRole('combobox', { name: /^来源模型/ })).toBeDisabled()
  await editor.getByRole('tab', { name: '数据映射', exact: true }).click()
  await expect(editor.getByRole('tabpanel', { name: '数据映射', exact: true }).getByText(/当前只有关系定义/)).toBeVisible()
  await editor.getByLabel('关系存储方式').selectOption('managed')
  await page.setViewportSize({ width: 390, height: 844 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0].link?.storageBinding).toEqual({ mode: 'managed' })
  await editor.getByRole('button', { name: '发布定义', exact: true }).click()
  await expect(editor.getByText('发布成功，当前定义已生效。')).toBeVisible()
  await expect(editor.getByLabel('关系存储方式')).toBeDisabled()

  await page.goto('/#query')
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await page.getByLabel('查询模型', { exact: true }).selectOption('notes')
  await page.getByRole('checkbox', { name: '标题', exact: true }).check()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await page.getByRole('button', { name: '关联数据', exact: true }).click()
  let panel = page.getByRole('dialog', { name: '关联数据', exact: true })
  await panel.getByRole('checkbox', { name: '关联字段：标签名称' }).check()
  await panel.getByRole('button', { name: '查询关联对象', exact: true }).click()
  await expect(panel.getByText('没有匹配的关联对象。')).toBeVisible()
  await panel.getByRole('button', { name: '维护对象关系', exact: true }).click()
  await panel.getByRole('button', { name: '读取可关联对象', exact: true }).click()
  await panel.getByLabel('选择要关联的对象').selectOption('tag-1')
  await panel.getByRole('button', { name: '添加关系', exact: true }).click()
  await expect.poll(() => state.creates.length).toBe(1)
  expect(state.creates[0]).toEqual({ sourceId: 'note-1', targetId: 'tag-1' })
  await panel.getByRole('button', { name: '查询关联对象', exact: true }).click()
  await expect(panel.getByRole('region', { name: '关联查询结果' }).getByText('知识图谱', { exact: true })).toBeVisible()
  expect(state.queries.find((spec) => spec.via?.direction === 'out')).toMatchObject({ from: { schemaId: 'tags' }, via: { linkId: 'note-tags', direction: 'out', instanceId: 'note-1' } })
  expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  await panel.getByRole('button', { name: '关闭对话框', exact: true }).click()

  await page.getByLabel('查询模型', { exact: true }).selectOption('tags')
  await page.getByRole('checkbox', { name: '标签名称', exact: true }).check()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await page.getByRole('button', { name: '关联数据', exact: true }).click()
  panel = page.getByRole('dialog', { name: '关联数据', exact: true })
  await panel.getByRole('checkbox', { name: '关联字段：标题' }).check()
  await panel.getByRole('button', { name: '查询关联对象', exact: true }).click()
  await expect(panel.getByRole('region', { name: '关联查询结果' }).getByText('关系数据库建模', { exact: true })).toBeVisible()
  expect(state.queries.find((spec) => spec.via?.direction === 'in')).toMatchObject({ from: { schemaId: 'notes' }, via: { linkId: 'note-tags', direction: 'in', instanceId: 'tag-1' } })
  await panel.getByRole('button', { name: '维护对象关系', exact: true }).click()
  await panel.getByRole('button', { name: '读取已有关系', exact: true }).click()
  await panel.getByRole('button', { name: '移除关系', exact: true }).click()
  await panel.getByRole('button', { name: '确认移除关系', exact: true }).click()
  await expect.poll(() => state.deletes.length).toBe(1)
  expect(state.deletes).toEqual(['"edge-current"'])
  await panel.getByRole('button', { name: '查询关联对象', exact: true }).click()
  await expect(panel.getByText('没有匹配的关联对象。')).toBeVisible()
  expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
