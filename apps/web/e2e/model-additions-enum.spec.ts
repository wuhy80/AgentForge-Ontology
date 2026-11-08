import { expect, test, type Page } from '@playwright/test'
import type { Definition, DefinitionRecord } from '../src/lib/types'

// Isolated HTTP responses exercise the real workspace without any business DB.
async function fixture(page: Page) {
  const tenantId = 'model-evolution-fixture'
  const values = [{ code: 'READY', label: '就绪' }, { code: 'OLD', label: '旧状态', deprecated: true }]
  const definitions: DefinitionRecord[] = [
    { status: 'active', etag: 'model-current', definition: { id: 'model', tenantId, categoryId: 'category', groupId: 'group', kind: 'schema', name: '业务记录', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name', name: '名称', dataType: 'string' }], indexes: [{ id: 'name-index', name: '名称索引', fieldIds: ['name'], unique: false }] } } },
    { status: 'active', etag: 'state-current', definition: { id: 'state', tenantId, categoryId: 'category', groupId: 'group', kind: 'enum', name: '业务状态', enum: { values } } },
  ]
  const drafts = new Map<string, DefinitionRecord>()
  const saves: Definition[] = [], errors: string[] = [], unexpected: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ json, status })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'editor', tenantId, username: 'editor', displayName: '模型编辑者', administrator: true, enabled: true, roleIds: [], etag: 'user' }, csrfToken: 'fixture-csrf', expiresAt: Math.floor(Date.now() / 1000) + 3600 })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'], schemaMigrations: { addFields: true, addIndexes: true, destructive: false }, enumReferences: true })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python', 'javascript', 'go'] })
    if (path === '/api/data/sources' || path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'category', tenantId, name: '业务' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId: 'category', name: '基础模型' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [...drafts.values()] : definitions, hasMore: false })
    if (path === '/api/data/schemas/model/storage-mapping') return reply({ schemaId: 'model', definitionETag: 'model-current', properties: [] })
    const match = path.match(/^\/api\/metadata\/definitions\/([^/]+)(?:\/(draft|preview))?$/)
    if (match) {
      const [, id, operation] = match
      if (operation === 'draft' && method === 'PUT') {
        const definition = request.postDataJSON() as Definition
        saves.push(structuredClone(definition))
        definition.schema?.properties.forEach((field, position) => { field.id ||= `field-${position}`; if (field.enumId) { field.enumValues = values; field.enumEtag = 'state-current' } })
        definition.schema?.indexes?.forEach((index, position) => { index.id ||= `index-${position}` })
        const saved: DefinitionRecord = { definition, status: 'draft', hasCurrent: true, etag: `draft-${saves.length}` }
        drafts.set(id, saved)
        return reply(saved)
      }
      if (operation === 'draft') return drafts.has(id) ? reply(drafts.get(id)) : reply({ error: { code: 'not_found', message: '暂无草稿' } }, 404)
      if (operation === 'preview' && method === 'POST') return reply({ operationId: 'preview', status: 'prepared', statements: ['ALTER TABLE business ADD COLUMN state VARCHAR(255);', 'UPDATE business SET state = ?;'] })
      const current = definitions.find((entry) => entry.definition.id === id)
      if (current) return reply(current)
    }
    unexpected.push(`${method} ${path}`)
    return reply({ error: { code: 'unexpected_fixture_request', message: '隔离用例未定义此接口' } }, 404)
  })
  return { saves, errors, unexpected }
}

for (const width of [1440, 390]) {
  test(`追加枚举字段、默认值与索引并核对迁移预览 ${width}px`, async ({ page }) => {
    const state = await fixture(page)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/#models/model')
    await page.getByRole('button', { name: '添加属性', exact: true }).click()
    let dialog = page.getByRole('dialog')
    await dialog.getByLabel('对象属性第2项名称').fill('状态')
    await dialog.getByLabel('对象属性第2项枚举').selectOption('state')
    await dialog.getByLabel('允许空值', { exact: true }).uncheck()
    await dialog.getByLabel('对象属性第2项默认值方式').selectOption('value')
    const select = dialog.getByRole('combobox', { name: '对象属性第2项默认值', exact: true })
    await expect(select.locator('option[value="OLD"]')).toBeDisabled()
    await select.selectOption('READY')
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    await dialog.getByRole('button', { name: '添加属性', exact: true }).click()
    await page.getByRole('button', { name: '保存草稿', exact: true }).click()
    await expect.poll(() => state.saves.length).toBe(1)
    expect(state.saves[0].schema?.properties[1]).toMatchObject({ enumId: 'state', default: 'READY', nullable: false })
    await page.getByRole('button', { name: '编辑对象属性第2项', exact: true }).click()
    dialog = page.getByRole('dialog')
    await expect(dialog.getByLabel('对象属性第2项类型')).toBeEnabled()
    await dialog.getByRole('button', { name: '取消', exact: true }).click()
    await page.getByRole('tab', { name: '索引信息', exact: true }).click()
    await expect(page.getByLabel('索引1第一个属性')).toBeDisabled()
    await page.getByRole('button', { name: '添加索引', exact: true }).click()
    await page.getByLabel('索引2名称').fill('状态索引')
    await page.getByLabel('索引2第一个属性').selectOption('field-1')
    await page.getByRole('button', { name: '保存草稿', exact: true }).click()
    await expect.poll(() => state.saves.length).toBe(2)
    expect(state.saves[1].schema?.properties[1]).not.toHaveProperty('enumValues')
    expect(state.saves[1].schema?.properties[1]).not.toHaveProperty('enumEtag')
    await page.getByRole('button', { name: '预览计划', exact: true }).click()
    await expect(page.getByText(/ALTER TABLE business/)).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(state.errors).toEqual([])
    expect(state.unexpected).toEqual([])
  })

  test(`已有枚举编码锁定，新增值保存后仍可编辑 ${width}px`, async ({ page }) => {
    const state = await fixture(page)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/#models/state')
    await expect(page.getByLabel('枚举值1编码')).toBeDisabled()
    await page.getByRole('button', { name: '添加值', exact: true }).click()
    await page.getByLabel('枚举值3编码').fill('RUNNING')
    await page.getByLabel('枚举值3标签').fill('运行中')
    await page.getByRole('button', { name: '保存草稿', exact: true }).click()
    await expect.poll(() => state.saves.length).toBe(1)
    await expect(page.getByLabel('枚举值1编码')).toBeDisabled()
    await expect(page.getByLabel('枚举值3编码')).toBeEnabled()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(state.errors).toEqual([])
    expect(state.unexpected).toEqual([])
  })
}
