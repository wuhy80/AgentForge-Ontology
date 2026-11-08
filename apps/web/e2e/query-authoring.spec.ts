import { createHash } from 'node:crypto'
import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, OntologySQLRequest, QueryContextModel } from '../src/lib/types'

// Every API request is answered in memory. This suite uses the real CodeMirror
// editor while never contacting, signing in to, or writing a real database.
async function fixture(page: Page) {
  const tenantId = 'query-authoring-fixture'
  const categoryId = 'business'
  const models: QueryContextModel[] = [
    { schemaId: 'orders', categoryId, category: '业务', name: '订单', fields: [
      { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: true, filterable: true, sortable: true },
      { id: 'remark', name: '备注', dataType: 'text', nullable: true, filterable: true, sortable: false },
      { id: 'body', name: '正文', dataType: 'longtext', nullable: true, filterable: false, sortable: false },
    ] },
    { schemaId: 'suppliers', categoryId, category: '业务', name: '供应商', fields: [
      { id: 'code', name: '供应商编码', dataType: 'string', nullable: false, filterable: true, sortable: true },
    ] },
  ]
  const definitions: DefinitionRecord[] = models.map((model) => ({ status: 'active', etag: `${model.schemaId}-etag`, definition: {
    id: model.schemaId, tenantId, categoryId, groupId: 'business-group', kind: 'schema', name: model.name,
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields },
  } }))
  const state = { unhandled: [] as string[], writes: [] as string[], queries: [] as string[], errors: [] as string[] }
  page.on('pageerror', (error) => state.errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (method === 'GET') {
      if (path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
      if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '查询用户', administrator: false, enabled: true, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
      if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
      if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
      if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
      if (path === '/api/metadata/categories') return reply({ data: [{ id: categoryId, tenantId, name: '业务' }], hasMore: false })
      if (path === '/api/metadata/groups') return reply({ data: [{ id: 'business-group', tenantId, categoryId, name: '业务模型' }], hasMore: false })
      if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : definitions, hasMore: false })
      if (path === '/api/data/query/context') return reply({ models })
    }
    if (method === 'POST' && path.startsWith('/api/data/query/ontology-sql')) {
      const body = request.postDataJSON() as OntologySQLRequest
      const response = { requestId: body.requestId, textHash: createHash('sha256').update(body.text).digest('hex') }
      if (path.endsWith('/analyze')) {
        const model = body.text.includes('FROM "业务"."供应商"') ? models[1] : models[0]
        return reply({ ...response, diagnostics: [], parameters: [], fields: model.fields, ready: true, query: { from: { schemaId: model.schemaId }, select: model.fields.map((field) => field.id), page: { limit: 25 } } })
      }
      if (path.endsWith('/complete')) return reply({ ...response, items: [] })
      if (path.endsWith('/format')) return reply({ ...response, diagnostics: [], text: body.text })
      if (path.endsWith('/paths')) return reply({ ...response, diagnostics: [], candidates: [], truncated: false })
      state.queries.push(path)
      return reply({ ...response, data: [], fields: [], hasMore: false, query: { from: { schemaId: models[0].schemaId }, select: ['amount'], page: { limit: 25 } } })
    }
    if (!['GET', 'HEAD'].includes(method)) state.writes.push(`${method} ${path}`)
    state.unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled_fixture_request', message: '未定义的隔离接口' } }, 404)
  })
  return state
}

const sql = 'SELECT "o"."金额"\nFROM "业务"."订单" AS "o"\nLIMIT 25;'
const withRemark = 'SELECT "o"."金额", "o"."备注"\nFROM "业务"."订单" AS "o"\nLIMIT 25;'
const orderTemplate = 'SELECT "订单"."金额", "订单"."备注"\nFROM "业务"."订单" AS "订单"\nLIMIT 25;'
const supplierTemplate = 'SELECT "供应商"."供应商编码"\nFROM "业务"."供应商" AS "供应商"\nLIMIT 25;'
const modelButton = (page: Page, name: string) => page.getByRole('complementary', { name: '模型目录', exact: true }).getByRole('button', { name: new RegExp(`^${name}\\s*\\d+\\s*属性$`) })
const fieldButton = (page: Page, name: string) => page.getByRole('complementary', { name: '模型字段', exact: true }).getByRole('button', { name: `插入字段 ${name}`, exact: true })
async function editorText(page: Page) { return (await page.getByLabel('OntologySQL 编辑器', { exact: true }).locator('.cm-line').allTextContents()).join('\n') }
async function setSQL(page: Page, value: string) {
  await page.getByLabel('OntologySQL 编辑器', { exact: true }).click()
  await page.keyboard.press('ControlOrMeta+A')
  await page.keyboard.insertText(value)
  await expect.poll(() => editorText(page)).toBe(value)
}
function noBusinessRequests(state: Awaited<ReturnType<typeof fixture>>) {
  expect(state.queries).toEqual([])
  expect(state.writes).toEqual([])
  expect(state.unhandled).toEqual([])
  expect(state.errors).toEqual([])
}

test('指导位于顶部、字段位于右侧，双击模型生成查询并按实际别名插入去重字段', async ({ page }) => {
  const state = await fixture(page)
  await page.goto('/#query')
  await expect(modelButton(page, '订单')).toBeVisible()
  const guide = await page.getByRole('region', { name: '查询指导', exact: true }).boundingBox()
  const directory = await page.getByRole('complementary', { name: '模型目录', exact: true }).boundingBox()
  const center = await page.getByRole('region', { name: 'OntologySQL 查询', exact: true }).boundingBox()
  const fields = await page.getByRole('complementary', { name: '模型字段', exact: true }).boundingBox()
  expect(guide!.y + guide!.height).toBeLessThanOrEqual(center!.y)
  expect(directory!.x + directory!.width).toBeLessThanOrEqual(center!.x)
  expect(center!.x + center!.width).toBeLessThanOrEqual(fields!.x)
  await modelButton(page, '供应商').click()
  await expect(fieldButton(page, '供应商编码')).toBeVisible()
  expect((await editorText(page)).trim()).toBe('')
  await modelButton(page, '订单').dblclick()
  await expect.poll(() => editorText(page)).toBe(orderTemplate)
  await setSQL(page, sql)
  await fieldButton(page, '备注').click()
  expect(await editorText(page)).toBe(sql)
  await fieldButton(page, '备注').dblclick()
  await expect.poll(() => editorText(page)).toBe(withRemark)
  await fieldButton(page, '备注').dblclick()
  await expect.poll(() => editorText(page)).toBe(withRemark)
  expect((await editorText(page)).match(/"o"\."备注"/g)).toHaveLength(1)
  await page.getByLabel('OntologySQL 编辑器', { exact: true }).press('ControlOrMeta+z')
  await expect.poll(() => editorText(page)).toBe(sql)
  await fieldButton(page, '备注').dblclick()
  await fieldButton(page, '正文').dblclick()
  await expect.poll(() => editorText(page)).toBe(withRemark.replace('"o"."备注"', '"o"."备注", "o"."正文"'))
  noBusinessRequests(state)
})

test('键盘生成与插入可用，其他模型替换需确认且可以撤销恢复原稿', async ({ page }) => {
  const state = await fixture(page)
  await page.goto('/#query')
  await modelButton(page, '订单').focus()
  await page.keyboard.press('Shift+Enter')
  await expect.poll(() => editorText(page)).toBe(orderTemplate)
  await setSQL(page, sql)
  await fieldButton(page, '备注').focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => editorText(page)).toBe(withRemark)
  await modelButton(page, '供应商').dblclick()
  await page.getByRole('button', { name: '改为单模型查询', exact: true }).click()
  await expect(page.getByRole('button', { name: '替换 SQL', exact: true })).toBeVisible()
  expect(await editorText(page)).toBe(withRemark)
  await page.getByRole('button', { name: '保留原稿', exact: true }).click()
  expect(await editorText(page)).toBe(withRemark)
  await modelButton(page, '供应商').focus()
  await page.keyboard.press('Shift+Enter')
  await page.getByRole('button', { name: '改为单模型查询', exact: true }).click()
  await page.getByRole('button', { name: '替换 SQL', exact: true }).click()
  await expect.poll(() => editorText(page)).toBe(supplierTemplate)
  await page.getByRole('button', { name: '撤销生成或插入', exact: true }).click()
  await expect.poll(() => editorText(page)).toBe(withRemark)
  expect(await editorText(page)).not.toContain('JOIN')
  noBusinessRequests(state)
})

test('390px可展开指导、模型和字段面板，插入后保持页面无横向溢出', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const state = await fixture(page)
  await page.goto('/#query')
  await expect(page.getByRole('button', { name: '模型目录', exact: true })).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('button', { name: '模型字段', exact: true })).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('button', { name: '查询指导', exact: true })).toHaveAttribute('aria-expanded', 'false')
  await page.getByRole('button', { name: '查询指导', exact: true }).click()
  await expect(page.getByRole('region', { name: '查询指导', exact: true })).toBeVisible()
  await page.getByText('语法速查', { exact: true }).click()
  await page.getByRole('button', { name: '模型目录', exact: true }).click()
  await modelButton(page, '订单').dblclick()
  await expect.poll(() => editorText(page)).toBe(orderTemplate)
  await expect(fieldButton(page, '正文')).toBeVisible()
  await fieldButton(page, '正文').focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => editorText(page)).toContain('"订单"."正文"')
  await page.getByRole('button', { name: '模型字段', exact: true }).click()
  await expect(page.getByRole('complementary', { name: '模型字段', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '模型字段', exact: true }).click()
  await expect(fieldButton(page, '正文')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  noBusinessRequests(state)
})
