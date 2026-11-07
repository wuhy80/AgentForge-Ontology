import { createHash } from 'node:crypto'
import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, OntologySQLRequest, QueryContextModel, QueryFieldInsertionRequest, QueryPathsRequest, QuerySpec } from '../src/lib/types'

// Real CodeMirror and browser layout; all API responses stay inside this fixture.
const categoryId = 'business'
const models: QueryContextModel[] = [
  { schemaId: 'orders', categoryId, category: '业务', name: '订单', fields: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true }] },
  { schemaId: 'customers', categoryId, category: '业务', name: '客户', fields: [{ id: 'name', name: '名称', dataType: 'string', filterable: true, sortable: true }, { id: 'phone', name: '电话', dataType: 'string', filterable: true, sortable: true }] },
  { schemaId: 'teams', categoryId, category: '业务', name: '团队', fields: [{ id: 'team-name', name: '团队名称', dataType: 'string', filterable: true, sortable: true }] },
]
const base = 'SELECT "o"."金额"\nFROM "业务"."订单" AS "o"\nWHERE "o"."金额" > :最低金额\nLIMIT 25;'
const first = base.replace('FROM', ', "c"."名称"\nFROM').replace('WHERE', 'JOIN LINK "o" VIA "业务"."订单客户" DIRECTION OUT AS "c"\nWHERE')
const second = first.replace('FROM', ', "g"."团队名称"\nFROM').replace('WHERE', 'JOIN LINK "c" VIA "业务"."客户团队" DIRECTION OUT AS "g"\nWHERE')
const self = 'SELECT "当前客户"."名称"\nFROM "业务"."客户" AS "当前客户"\nJOIN LINK "当前客户" VIA "业务"."上级客户" DIRECTION OUT AS "上级客户"\nLIMIT 25;'

function queryFor(text: string): QuerySpec {
  const isSelf = text.includes('AS "上级客户"'), hasTeam = text.includes('AS "g"')
  const nodes = isSelf ? [{ id: 'n0', schemaId: 'customers', alias: '当前客户' }, { id: 'n1', schemaId: 'customers', alias: '上级客户' }] : [{ id: 'n0', schemaId: 'orders', alias: 'o' }, { id: 'n1', schemaId: 'customers', alias: 'c' }, ...(hasTeam ? [{ id: 'n2', schemaId: 'teams', alias: 'g' }] : [])]
  return { from: { schemaId: nodes[0].schemaId }, rows: { nodes, joins: [{ from: 'n0', to: 'n1', linkId: isSelf ? 'parent-customer' : 'order-customer', direction: 'out' }, ...(hasTeam ? [{ from: 'n1', to: 'n2', linkId: 'customer-team', direction: 'out' as const }] : [])] }, select: isSelf ? ['n0.name'] : ['n0.amount', 'n1.name', ...(hasTeam ? ['n2.team-name'] : []), ...(text.includes('"c"."电话"') ? ['n1.phone'] : [])], page: { limit: 25 } }
}
async function fixture(page: Page) {
  const tenantId = 'query-path-fixture'
  const definitions: DefinitionRecord[] = models.map((model) => ({ status: 'active', etag: `${model.schemaId}-etag`, definition: { id: model.schemaId, tenantId, categoryId, groupId: 'group', kind: 'schema', name: model.name, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields } } }))
  const state = { paths: [] as QueryPathsRequest[], fields: [] as QueryFieldInsertionRequest[], runs: [] as OntologySQLRequest[], pages: [] as QuerySpec[], unhandled: [] as string[], writes: [] as string[], errors: [] as string[] }
  page.on('pageerror', (error) => state.errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (method === 'GET') {
      if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '查询用户', administrator: false, enabled: true, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
      if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
      if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'], rowQueries: true, maxRowJoins: 4, queryPathPlanning: true })
      if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
      if (path === '/api/metadata/categories') return reply({ data: [{ id: categoryId, tenantId, name: '业务' }], hasMore: false })
      if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId, name: '业务模型' }], hasMore: false })
      if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : definitions, hasMore: false })
      if (path === '/api/data/query/context') return reply({ models })
    }
    if (method === 'POST' && path === '/api/data/query') { state.pages.push(request.postDataJSON() as QuerySpec); return reply({ data: [], rowMode: true, rows: [], fields: [], hasMore: false }) }
    if (method === 'POST' && path.startsWith('/api/data/query/ontology-sql')) {
      const body = request.postDataJSON() as OntologySQLRequest
      const response = { requestId: body.requestId, textHash: createHash('sha256').update(body.text).digest('hex') }
      const isRows = body.text.includes('JOIN LINK'), query = isRows ? queryFor(body.text) : { from: { schemaId: 'orders' }, select: ['amount'], page: { limit: 25 } }
      const fields = isRows ? query.select.map((id) => { const [nodeId, fieldId] = id.split('.'), node = query.rows!.nodes.find((item) => item.id === nodeId)!, field = models.find((model) => model.schemaId === node.schemaId)!.fields.find((item) => item.id === fieldId)!; return { ...field, id, name: `${node.alias}.${field.name}` } }) : models[0].fields
      if (path.endsWith('/analyze')) {
        const parameters = body.text.includes(':最低金额') ? [{ name: '最低金额', dataType: 'decimal', precision: 24, scale: 6, nullable: false, provided: body.parameters?.['最低金额'] !== undefined, ranges: [] }] : []
        return reply({ ...response, diagnostics: [], parameters, fields, ready: parameters.every((parameter) => parameter.provided), scope: query.rows, query: parameters.every((parameter) => parameter.provided) ? query : undefined })
      }
      if (path.endsWith('/complete')) return reply({ ...response, items: [] })
      if (path.endsWith('/format')) return reply({ ...response, diagnostics: [], text: body.text })
      if (path.endsWith('/paths')) {
        const pathRequest = body as QueryPathsRequest; state.paths.push(pathRequest)
        const customer = { id: 'customer-path', fromAlias: 'o', steps: [{ linkId: 'order-customer', name: '订单客户', category: '业务', direction: 'out', fromAlias: 'o', toAlias: 'c', target: models[1] }], text: first }
        return reply({ ...response, diagnostics: [], truncated: false, candidates: pathRequest.targetSchemaId === 'teams' ? [{ id: 'team-path', fromAlias: 'c', steps: [{ linkId: 'customer-team', name: '客户团队', category: '业务', direction: 'out', fromAlias: 'c', toAlias: 'g', target: models[2] }], text: second }] : [customer, { ...customer, id: 'billing-path', steps: [{ ...customer.steps[0], linkId: 'billing-customer', name: '订单收货客户', toAlias: '收货客户' }], text: first.replaceAll('"c"', '"收货客户"').replace('订单客户', '订单收货客户') }] })
      }
      if (path.endsWith('/insert-field')) {
        const edit = body as QueryFieldInsertionRequest; state.fields.push(edit)
        const field = models.find((model) => model.schemaId === edit.schemaId)!.fields.find((item) => item.id === edit.fieldId)!
        const reference = `"${edit.alias}"."${field.name}"`, from = body.text.indexOf('\nFROM')
        const text = body.text.includes(reference) ? body.text : `${body.text.slice(0, from)}, ${reference}${body.text.slice(from)}`
        return reply({ ...response, diagnostics: [], text, from: text.indexOf(reference), to: text.indexOf(reference) + reference.length })
      }
      if (path === '/api/data/query/ontology-sql') {
        state.runs.push(body)
        return reply({ ...response, query, data: [], rowMode: true, rows: [1, 2].map((number) => ({ id: `row-${number}`, values: { 'n0.amount': '9007199254740993.000001', 'n1.name': `客户${number}`, 'n1.phone': `1000${number}`, 'n2.team-name': '研发团队' }, entities: [{ nodeId: 'n0', schemaId: 'orders', instanceId: 'same-order' }, { nodeId: 'n1', schemaId: 'customers', instanceId: `customer-${number}` }, { nodeId: 'n2', schemaId: 'teams', instanceId: 'team-1' }] })), fields, hasMore: true, nextCursor: 'joined-page-cursor' })
      }
    }
    if (!['GET', 'HEAD'].includes(method)) state.writes.push(`${method} ${path}`)
    state.unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled_fixture_request', message: '未定义的隔离接口' } }, 404)
  })
  return state
}
const modelButton = (page: Page, name: string) => page.getByRole('complementary', { name: '模型目录', exact: true }).getByRole('button', { name: new RegExp(`^${name}\\s*\\d+\\s*属性$`) })
const fieldButton = (page: Page, name: string) => page.getByRole('complementary', { name: '模型字段', exact: true }).getByRole('button', { name: `插入字段 ${name}`, exact: true })
async function editorText(page: Page) { return (await page.getByLabel('OntologySQL 编辑器', { exact: true }).locator('.cm-line').allTextContents()).join('\n') }
async function setSQL(page: Page, value: string) { await page.getByLabel('OntologySQL 编辑器', { exact: true }).click(); await page.keyboard.press('ControlOrMeta+A'); await page.keyboard.insertText(value); await expect.poll(() => editorText(page)).toBe(value) }
function isolated(state: Awaited<ReturnType<typeof fixture>>) { expect(state.writes).toEqual([]); expect(state.unhandled).toEqual([]); expect(state.errors).toEqual([]) }

test('连续选择两个 Link 保留参数，按别名插入字段，关联行及续页携带完整计划', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query'); await expect(modelButton(page, '客户')).toBeVisible(); await setSQL(page, base)
  await page.getByRole('combobox', { name: '最低金额赋值方式', exact: true }).selectOption('value')
  await page.locator('#parameter-最低金额').fill('10.000001')
  await modelButton(page, '客户').dblclick()
  await expect(page.getByRole('button', { name: '使用路径 2', exact: true })).toBeVisible()
  expect(await editorText(page)).toBe(base); expect(state.runs).toEqual([])
  await page.getByRole('button', { name: '使用路径 1', exact: true }).click(); await expect.poll(() => editorText(page)).toBe(first)
  await expect(page.locator('#parameter-最低金额')).toHaveValue('10.000001')
  await modelButton(page, '团队').dblclick(); await page.getByRole('button', { name: '使用路径 1', exact: true }).click()
  await expect.poll(() => editorText(page)).toBe(second)
  await expect(page.locator('#parameter-最低金额')).toHaveValue('10.000001')
  expect(state.paths[1].parameters?.['最低金额']).toBe('10.000001')
  await page.getByRole('group', { name: '查询中的模型角色', exact: true }).getByRole('button', { name: 'c', exact: true }).click()
  await fieldButton(page, '电话').dblclick(); await expect.poll(() => editorText(page)).toContain('"c"."电话"')
  expect(state.fields[0]).toMatchObject({ schemaId: 'customers', fieldId: 'phone', alias: 'c' })
  expect(state.runs).toEqual([])
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  const table = page.getByRole('table', { name: '关联行查询结果', exact: true })
  await expect(table.getByText('9007199254740993.000001', { exact: true })).toHaveCount(2)
  await expect(page.getByRole('button', { name: '关联数据', exact: true })).toHaveCount(0)
  await table.locator('summary').first().click(); await expect(table.getByText('same-order', { exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect.poll(() => state.pages.length).toBe(1)
  expect(state.pages[0]).toEqual({ ...queryFor(await editorText(page)), page: { limit: 25, cursor: 'joined-page-cursor' } })
  isolated(state)
})

test('同模型的两个查询角色需选择别名，字段插入及原生撤销保留角色身份', async ({ page }) => {
  const state = await fixture(page); await page.goto('/#query'); await expect(modelButton(page, '客户')).toBeVisible(); await setSQL(page, self); await modelButton(page, '客户').click()
  const roles = page.getByRole('combobox', { name: '字段所属查询别名', exact: true }); await expect(roles).toBeVisible()
  await fieldButton(page, '电话').dblclick(); expect(state.fields).toEqual([])
  await expect(page.getByText('此模型在查询中有多个角色，请先选择字段所属的查询别名。', { exact: true })).toBeVisible()
  await roles.selectOption('上级客户'); await fieldButton(page, '电话').dblclick()
  await expect.poll(() => editorText(page)).toContain('"上级客户"."电话"')
  expect(state.fields[0]).toMatchObject({ alias: '上级客户', schemaId: 'customers', fieldId: 'phone' })
  await page.getByLabel('OntologySQL 编辑器', { exact: true }).press('ControlOrMeta+z'); await expect.poll(() => editorText(page)).toBe(self)
  expect(state.runs).toEqual([]); isolated(state)
})

test('390px下路径预览、SQL展开及关联结果不使页面横向溢出', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 }); const state = await fixture(page); await page.goto('/#query')
  await page.getByRole('button', { name: '模型目录', exact: true }).click(); await expect(modelButton(page, '客户')).toBeVisible(); await setSQL(page, base)
  await page.getByRole('combobox', { name: '最低金额赋值方式', exact: true }).selectOption('value'); await page.locator('#parameter-最低金额').fill('0')
  await modelButton(page, '客户').dblclick(); await page.getByRole('region', { name: '关联路径选择', exact: true }).getByText('查看生成的 SQL', { exact: true }).first().click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '使用路径 1', exact: true }).click(); await expect.poll(() => editorText(page)).toBe(first)
  await expect(page.locator('#parameter-最低金额')).toHaveValue('0')
  await page.getByRole('button', { name: '运行查询', exact: true }).click(); await expect(page.getByRole('table', { name: '关联行查询结果', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true); isolated(state)
})
