import { createHash } from 'node:crypto'
import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, OntologySQLRequest, QueryContextModel, QueryPathsRequest, QuerySpec, RowQueryNode } from '../src/lib/types'

// Browser interaction and CodeMirror are real; all service responses are isolated.
const categoryId = 'business', tenantId = 'visual-query-fixture'
const models: QueryContextModel[] = [
  { schemaId: 'orders', categoryId, category: '业务', name: '订单', fields: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true }] },
  { schemaId: 'customers', categoryId, category: '业务', name: '客户', fields: [{ id: 'name', name: '名称', dataType: 'string', filterable: true, sortable: true }] },
  { schemaId: 'teams', categoryId, category: '业务', name: '团队', fields: [{ id: 'name', name: '名称', dataType: 'string', filterable: true, sortable: true }] },
]
const links = [
  { id: 'order-customer', name: '订单客户', sourceSchemaId: 'orders', targetSchemaId: 'customers' },
  { id: 'customer-team', name: '客户团队', sourceSchemaId: 'customers', targetSchemaId: 'teams' },
  { id: 'customer-parent', name: '上级客户', sourceSchemaId: 'customers', targetSchemaId: 'customers' },
]
const modelFor = (schemaId: string) => models.find((model) => model.schemaId === schemaId)!
function queryFor(request: OntologySQLRequest): QuerySpec {
  const root = /FROM "业务"\."([^"]+)" AS "([^"]+)"/.exec(request.text)!
  const nodes: RowQueryNode[] = [{ id: 'n0', schemaId: models.find((model) => model.name === root[1])!.schemaId, alias: root[2] }]
  const joins = Array.from(request.text.matchAll(/JOIN LINK "([^"]+)" VIA "业务"\."([^"]+)" DIRECTION (OUT|IN) AS "([^"]+)"/g), ([, fromAlias, linkName, direction, alias]) => {
    const link = links.find((item) => item.name === linkName)!, from = nodes.find((node) => node.alias === fromAlias)!
    const to = `n${nodes.length}`
    nodes.push({ id: to, alias, schemaId: direction === 'OUT' ? link.targetSchemaId : link.sourceSchemaId })
    return { from: from.id, to, linkId: link.id, direction: direction.toLowerCase() as 'out' | 'in' }
  })
  const reference = (alias: string, name: string) => {
    const node = nodes.find((item) => item.alias === alias)!, field = modelFor(node.schemaId).fields.find((item) => item.name === name)!
    return joins.length ? `${node.id}.${field.id}` : field.id
  }
  const select = Array.from(request.text.split('FROM')[0].matchAll(/"([^"]+)"\."([^"]+)"/g), ([, alias, name]) => reference(alias, name))
  const predicate = /WHERE\s*\(*"([^"]+)"\."([^"]+)"\s*>=\s*:([\p{L}\p{N}_]+)/u.exec(request.text)
  const where = predicate ? { op: 'and', args: [{ op: 'gte', field: reference(predicate[1], predicate[2]), value: request.parameters?.[predicate[3]] ?? '0' }] } : undefined
  return { from: { schemaId: nodes[0].schemaId }, ...(joins.length ? { rows: { nodes, joins } } : {}), select, ...(where ? { where } : {}), page: { limit: Number(/LIMIT (\d+)/.exec(request.text)?.[1] ?? 25) } }
}
function resultFor(query: QuerySpec) {
  const fields = query.select.map((reference) => {
    const [nodeId, fieldId] = query.rows ? reference.split('.') : ['n0', reference]
    const node = query.rows?.nodes.find((item) => item.id === nodeId) ?? { schemaId: query.from.schemaId!, alias: modelFor(query.from.schemaId!).name }
    const field = modelFor(node.schemaId).fields.find((item) => item.id === fieldId)!
    return { ...field, id: reference, name: query.rows ? `${node.alias}.${field.name}` : field.name }
  })
  const values = (index: number) => Object.fromEntries(fields.map((field) => [field.id, field.dataType === 'decimal' ? '9007199254740993.000001' : `客户${index}`]))
  return query.rows ? { data: [], fields, rowMode: true, rows: [1, 2].map((index) => ({ id: `row-${index}`, values: values(index), entities: query.rows!.nodes.map((node) => ({ nodeId: node.id, schemaId: node.schemaId, instanceId: node.id === 'n0' ? 'same-root' : `${node.id}-${index}` })) })), hasMore: !query.page.cursor, ...(!query.page.cursor ? { nextCursor: 'row-cursor' } : {}) } : { data: [{ id: 'root', schemaId: query.from.schemaId, values: values(1), etag: 'instance' }], fields, hasMore: false }
}
async function fixture(page: Page) {
  const definitions: DefinitionRecord[] = models.map((model) => ({ etag: model.schemaId, status: 'active', definition: { id: model.schemaId, tenantId, categoryId, groupId: 'group', kind: 'schema', name: model.name, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields } } }))
  const state = { queries: [] as QuerySpec[], paths: [] as QueryPathsRequest[], errors: [] as string[], unexpected: [] as string[], deny: false, denyContext: false }
  page.on('pageerror', (error) => state.errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (method === 'GET') {
      if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '查询用户', administrator: false, enabled: true, roleIds: [], etag: 'user' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture' })
      if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
      if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'], rowQueries: true, maxRowJoins: 4 })
      if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
      if (path === '/api/metadata/categories') return reply({ data: [{ id: categoryId, tenantId, name: '业务' }], hasMore: false })
      if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', categoryId, tenantId, name: '业务模型' }], hasMore: false })
      if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : definitions, hasMore: false })
      if (path.startsWith('/api/metadata/definitions/')) {
        const link = links.find((item) => item.id === path.split('/').at(-1))
        if (link) return reply({ etag: link.id, status: 'active', definition: { id: link.id, tenantId, categoryId, groupId: 'group', kind: 'link', name: link.name, link } })
      }
      if (path === '/api/data/query/context') return state.denyContext ? reply({ error: { code: 'forbidden', message: '模型查询权限已撤销' } }, 403) : reply({ models: models.filter((model) => !url.searchParams.get('schemaId') || model.schemaId === url.searchParams.get('schemaId')) })
    }
    if (method === 'POST' && path === '/api/data/query') {
      if (state.deny) return reply({ error: { code: 'forbidden', message: '关联查询权限已撤销' } }, 403)
      const query = request.postDataJSON() as QuerySpec; state.queries.push(query); return reply(resultFor(query))
    }
    if (method === 'POST' && path.startsWith('/api/data/query/ontology-sql')) {
      const body = request.postDataJSON() as OntologySQLRequest
      const response = { requestId: body.requestId, textHash: createHash('sha256').update(body.text).digest('hex') }
      if (path.endsWith('/complete')) return reply({ ...response, items: [] })
      if (path.endsWith('/format')) return reply({ ...response, text: body.text, diagnostics: [] })
      const query = queryFor(body)
      if (path.endsWith('/analyze')) {
        const parameters = Array.from(body.text.matchAll(/:([\p{L}\p{N}_]+)/gu), ([, name]) => ({ name, dataType: 'decimal', precision: 24, scale: 6, nullable: false, provided: body.parameters?.[name] !== undefined, ranges: [] }))
        return reply({ ...response, diagnostics: [], parameters, fields: resultFor(query).fields, scope: query.rows, ready: parameters.every((item) => item.provided), ...(parameters.every((item) => item.provided) ? { query } : {}) })
      }
      if (path.endsWith('/paths')) {
        const input = body as QueryPathsRequest; state.paths.push(input)
        const nodes = query.rows?.nodes ?? [{ id: 'n0', schemaId: query.from.schemaId!, alias: modelFor(query.from.schemaId!).name }]
        const from = nodes.find((node) => links.some((link) => link.sourceSchemaId === node.schemaId && link.targetSchemaId === input.targetSchemaId))!
        const link = links.find((item) => item.sourceSchemaId === from.schemaId && item.targetSchemaId === input.targetSchemaId)!
        const target = modelFor(input.targetSchemaId), alias = nodes.some((node) => node.alias === target.name) ? `${target.name}_2` : target.name
        const text = body.text.replace(/\n(WHERE|LIMIT)/, `\nJOIN LINK "${from.alias}" VIA "业务"."${link.name}" DIRECTION OUT AS "${alias}"\n$1`)
        return reply({ ...response, diagnostics: [], truncated: false, candidates: [{ id: 'path', fromAlias: from.alias, steps: [{ linkId: link.id, name: link.name, category: '业务', direction: 'out', fromAlias: from.alias, toAlias: alias, target }], text }] })
      }
      if (path === '/api/data/query/ontology-sql') { state.queries.push(query); return reply({ ...response, ...resultFor(query), query }) }
    }
    state.unexpected.push(`${method} ${path}`)
    return reply({ error: { code: 'unexpected_request', message: '隔离用例未定义此接口' } }, 404)
  })
  return state
}
async function addCustomer(page: Page) {
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await page.getByRole('checkbox', { name: '金额', exact: true }).check()
  await page.getByRole('button', { name: '添加关联', exact: true }).click()
  const target = page.getByRole('combobox', { name: '关联目标模型', exact: true })
  await expect(target.getByRole('option', { name: '业务 / 客户', exact: true })).toBeAttached()
  await target.selectOption('customers')
  await page.getByRole('button', { name: '查找关联路径', exact: true }).click()
  await page.getByRole('button', { name: '采用关联路径 1', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: '客户.名称', exact: true })).toBeVisible()
}
const isolated = (state: Awaited<ReturnType<typeof fixture>>) => { expect(state.errors).toEqual([]); expect(state.unexpected).toEqual([]) }
async function setSQL(page: Page, text: string) { await page.getByLabel('OntologySQL 编辑器', { exact: true }).click(); await page.keyboard.press('ControlOrMeta+A'); await page.keyboard.insertText(text) }
async function editorText(page: Page) { return (await page.getByLabel('OntologySQL 编辑器', { exact: true }).locator('.cm-line').allTextContents()).join('\n') }

test('从空白可视化添加两条Link，按角色筛选，精确数值与绑定在两种模式间完整保留', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query'); await addCustomer(page)
  expect(state.queries).toEqual([])
  await page.getByRole('checkbox', { name: '客户.名称', exact: true }).check()
  await page.getByRole('button', { name: '添加关联', exact: true }).click()
  const target = page.getByRole('combobox', { name: '关联目标模型', exact: true })
  await expect(target.getByRole('option', { name: '业务 / 团队', exact: true })).toBeAttached(); await target.selectOption('teams')
  await page.getByRole('button', { name: '查找关联路径', exact: true }).click(); await page.getByRole('button', { name: '采用关联路径 1', exact: true }).click()
  await page.getByRole('checkbox', { name: '团队.名称', exact: true }).check()
  await page.getByRole('button', { name: '添加条件', exact: true }).click()
  await page.getByRole('combobox', { name: '筛选字段', exact: true }).selectOption('n0.amount')
  await page.getByRole('combobox', { name: '筛选运算符', exact: true }).selectOption('gte')
  await page.getByLabel('比较值', { exact: true }).fill('9007199254740993.000001')
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(page.getByRole('table', { name: '关联行查询结果', exact: true }).locator('tbody tr')).toHaveCount(2)
  const query = state.queries[0]; expect(query.rows?.joins).toHaveLength(2); expect(query.where?.args?.[0].value).toBe('9007199254740993.000001')
  await page.getByRole('button', { name: '下一页', exact: true }).click(); await expect.poll(() => state.queries.length).toBe(2)
  expect(state.queries[1]).toEqual({ ...query, page: { limit: 25, cursor: 'row-cursor' } })
  await page.getByRole('tab', { name: 'OntologySQL', exact: true }).click()
  await expect.poll(() => editorText(page)).toContain('JOIN LINK "客户" VIA "业务"."客户团队" DIRECTION OUT AS "团队"')
  await expect(page.locator('#parameter-条件1')).toHaveValue('9007199254740993.000001')
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await expect(page.getByLabel('比较值', { exact: true })).toHaveValue('9007199254740993.000001')
  await expect(page.getByRole('checkbox', { name: '团队.名称', exact: true })).toBeChecked()
  expect(state.queries).toHaveLength(2); isolated(state)
})

test('反向自关联保留两个角色，改名不改变字段身份，403清除关联结果', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query')
  await setSQL(page, 'SELECT "本人"."名称", "下级"."名称"\nFROM "业务"."客户" AS "本人"\nJOIN LINK "本人" VIA "业务"."上级客户" DIRECTION IN AS "下级"\nLIMIT 25;')
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: '下级.名称', exact: true })).toBeChecked()
  await page.getByLabel('模型角色 2', { exact: true }).fill('直属下级')
  await page.getByRole('button', { name: '运行查询', exact: true }).click(); await expect.poll(() => state.queries.length).toBe(1)
  expect(state.queries[0].rows?.joins[0].direction).toBe('in'); expect(state.queries[0].select).toEqual(['n0.name', 'n1.name'])
  state.deny = true; await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('关联查询权限已撤销')
  await expect(page.getByRole('table')).toHaveCount(0); await expect(page.getByRole('checkbox', { name: '直属下级.名称', exact: true })).toHaveCount(0)
  isolated(state)
})

test('390px下关联配置和结果无横向溢出，移除关联前确认结果粒度', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 }); const state = await fixture(page); await page.goto('/#query'); await addCustomer(page)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '运行查询', exact: true }).click(); await expect(page.getByRole('table', { name: '关联行查询结果', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '移除最后一个关联', exact: true }).click()
  await expect(page.getByText(/移除后返回单模型对象列表/)).toBeVisible()
  await page.getByRole('button', { name: '确认移除关联', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: '金额', exact: true })).toBeChecked()
  await expect(page.getByRole('button', { name: '下一页', exact: true })).toBeDisabled()
  isolated(state)
})

test('四条Link的SQL完整转换，达到预算后停用添加关联', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query')
  const sql = 'SELECT "角色0"."名称", "角色4"."名称"\nFROM "业务"."客户" AS "角色0"\n' + [0, 1, 2, 3].map((index) => `JOIN LINK "角色${index}" VIA "业务"."上级客户" DIRECTION OUT AS "角色${index + 1}"`).join('\n') + '\nLIMIT 25;'
  await setSQL(page, sql); await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await expect(page.getByLabel('模型角色 5', { exact: true })).toHaveValue('角色4')
  await expect(page.getByRole('button', { name: '添加关联', exact: true })).toBeDisabled()
  await page.getByRole('tab', { name: 'OntologySQL', exact: true }).click(); await expect.poll(() => editorText(page)).toBe(sql)
  isolated(state)
})

test('切换SQL时元数据授权拒绝会清除可视化旧结果与字段候选', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query'); await addCustomer(page)
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(page.getByRole('table', { name: '关联行查询结果', exact: true })).toBeVisible()
  state.denyContext = true
  await page.getByRole('tab', { name: 'OntologySQL', exact: true }).click()
  await expect(page.getByRole('button', { name: '重新读取查询权限', exact: true })).toBeVisible()
  await expect(page.getByRole('table')).toHaveCount(0)
  await expect(page.getByRole('checkbox', { name: '客户.名称', exact: true })).toHaveCount(0)
  await expect(page.getByRole('tab', { name: '可视化查询', exact: true })).toHaveAttribute('aria-selected', 'true')
  isolated(state)
})
