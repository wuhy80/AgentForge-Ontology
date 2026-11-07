import { test, expect, type Page } from '@playwright/test'
import type { DataSource, DataSourceInput, Definition, DefinitionRecord, SourceTable } from '../src/lib/types'

// All connections, accounts, and tables are isolated HTTP fixtures. No external
// database is contacted or changed by this browser workflow.
async function mappingFixture(page: Page) {
  const tenantId = 'mapping-fixture'
  const sources: DataSource[] = []
  const sourceWrites: DataSourceInput[] = []
  const definitions: Definition[] = []
  const unhandled: string[] = []
  let draft: DefinitionRecord = { etag: 'draft-0', status: 'draft', definition: {
    id: 'equipment-model', tenantId, categoryId: 'category', groupId: 'group', kind: 'schema', name: '设备对象',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name-field', name: '设备名称', dataType: 'string' }], indexes: [] },
  } }
  const primary: SourceTable = { namespace: 'main', name: 'equipment', signature: 'main-snapshot', indexes: [{ name: 'PRIMARY', unique: true, primary: true, columns: ['id'] }], columns: [
    { name: 'id', databaseType: 'char(36)', dataType: 'uuid', nullable: false, primaryKey: true, generated: false, position: 1 },
    { name: 'tenant_id', databaseType: 'varchar(128)', dataType: 'string', nullable: false, primaryKey: false, generated: false, position: 2 },
    { name: 'name', databaseType: 'varchar(255)', dataType: 'string', nullable: false, primaryKey: false, generated: false, position: 3 },
    { name: 'serial_code', databaseType: 'varchar(255)', dataType: 'string', nullable: true, primaryKey: false, generated: false, position: 4 },
  ] }
  const related: SourceTable = { namespace: 'public', name: 'asset_details', signature: 'asset-snapshot', indexes: [{ name: 'asset_equipment', unique: true, primary: false, columns: ['tenant_id', 'equipment_id'] }], columns: [
    { name: 'equipment_id', databaseType: 'uuid', dataType: 'uuid', nullable: false, primaryKey: false, generated: false, position: 1 },
    { name: 'tenant_id', databaseType: 'varchar(128)', dataType: 'string', nullable: false, primaryKey: false, generated: false, position: 2 },
    { name: 'asset_name', databaseType: 'varchar(255)', dataType: 'string', nullable: true, primaryKey: false, generated: false, position: 3 },
  ] }
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'administrator', tenantId, username: 'admin', displayName: '管理员', administrator: true, enabled: true, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db', 'db_2_schema'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'category', tenantId, name: '业务分类' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId: 'category', name: '设备目录' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [draft] : [], hasMore: false })
    if (path === '/api/metadata/definitions/equipment-model/draft') {
      if (method === 'GET') return reply(draft)
      const definition = request.postDataJSON() as Definition
      definitions.push(structuredClone(definition))
      definition.schema!.properties = definition.schema!.properties.map((property, index) => ({ ...property, id: property.id || `new-field-${index}` }))
      draft = { definition, status: 'draft', etag: `draft-${definitions.length}` }
      return reply(draft)
    }
    if (path === '/api/data/sources') {
      if (method === 'GET') return reply({ data: sources, hasMore: false })
      const input = request.postDataJSON() as DataSourceInput
      sourceWrites.push(structuredClone(input))
      const { password, ...publicInput } = input
      const source: DataSource = { ...publicInput, id: `source-${sources.length}`, tenantId, etag: 'source-etag', passwordSet: !!password }
      sources.push(source)
      return reply(source, 201)
    }
    if (/^\/api\/data\/sources\/source-\d\/test$/.test(path)) return reply({ success: true, serverVersion: '16.4' })
    const tablePath = path.match(/^\/api\/data\/sources\/(source-\d)\/tables(?:\/([^/]+))?$/)
    if (tablePath) {
      const table = tablePath[1] === 'source-0' ? primary : related
      return tablePath[2] ? reply(table) : reply({ data: [{ namespace: table.namespace, name: table.name, type: 'BASE TABLE' }], hasMore: false })
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { sources, sourceWrites, definitions, unhandled }
}

test('数据源独立配置后，模型逐属性跨源映射并从数据库读取属性', async ({ page }) => {
  const state = await mappingFixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#sources')
  await expect(page.getByRole('link', { name: '数据源配置', exact: true })).toHaveAttribute('aria-current', 'page')
  for (const [name, driver, database] of [['设备库', 'mysql', 'main'], ['资产库', 'postgresql', 'assets']]) {
    await page.getByRole('button', { name: '添加数据源', exact: true }).click()
    const form = page.getByRole('region', { name: '数据源连接配置' })
    await form.getByLabel('数据源名称', { exact: true }).fill(name)
    await form.getByLabel('数据库类型', { exact: true }).selectOption(driver)
    await form.getByLabel('数据库主机', { exact: true }).fill('fixture.invalid')
    await form.getByLabel('数据库名称', { exact: true }).fill(database)
    await form.getByLabel('数据库用户名', { exact: true }).fill('fixture_reader')
    await form.getByLabel('数据库密码', { exact: true }).fill('fixture-only-password')
    await form.getByRole('button', { name: '保存数据源', exact: true }).click()
    await expect(form.getByLabel('数据库密码', { exact: true })).toHaveValue('')
  }
  expect(state.sourceWrites.map((input) => input.driver)).toEqual(['mysql', 'postgresql'])
  expect(state.sourceWrites[1].port).toBe(5432)
  await page.getByRole('button', { name: '测试已保存连接', exact: true }).click()
  await expect(page.getByText('连接成功 · PostgreSQL 16.4（已保存配置）', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '查看数据库表', exact: true }).click()
  await page.getByRole('button', { name: 'public.asset_details', exact: true }).click()
  await expect(page.getByRole('region', { name: '数据源表结构' }).getByRole('cell', { name: 'asset_name', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '模型管理', exact: true }).click()
  await page.getByRole('button', { name: '设备对象', exact: true }).click()
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(page).toHaveURL(/#models\/equipment-model$/)
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await editor.getByLabel('主数据源', { exact: true }).selectOption('source-0')
  await editor.getByLabel('数据库命名空间', { exact: true }).fill('main')
  await editor.getByLabel('主表选择', { exact: true }).selectOption(JSON.stringify(['main', 'equipment']))
  await editor.getByLabel('主表租户列', { exact: true }).fill('tenant_id')
  await editor.getByRole('tab', { name: '属性配置', exact: true }).click()
  await editor.getByRole('button', { name: '编辑对象属性第1项', exact: true }).click()
  const propertyDialog = page.getByRole('dialog', { name: '编辑属性', exact: true })
  await propertyDialog.getByLabel('属性1字段选择', { exact: true }).selectOption('name')
  await propertyDialog.getByRole('button', { name: '应用修改', exact: true }).click()
  await editor.getByRole('button', { name: '添加属性', exact: true }).click()
  const addDialog = page.getByRole('dialog', { name: '添加属性', exact: true })
  await addDialog.getByLabel('对象属性第2项名称').fill('资产名称')
  await addDialog.getByLabel('允许空值').check()
  await addDialog.getByLabel('属性2数据源', { exact: true }).selectOption('source-1')
  await addDialog.getByLabel('属性2命名空间', { exact: true }).fill('public')
  await addDialog.getByLabel('属性2来源表选择', { exact: true }).selectOption(JSON.stringify(['public', 'asset_details']))
  await addDialog.getByLabel('属性2字段选择', { exact: true }).selectOption('asset_name')
  await addDialog.getByLabel('属性2关联键列', { exact: true }).fill('equipment_id')
  await addDialog.getByLabel('属性2租户列', { exact: true }).fill('tenant_id')
  await addDialog.getByRole('button', { name: '添加属性', exact: true }).click()
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.definitions.length).toBe(1)
  expect(state.definitions[0].schema!.properties[1].mapping).toEqual({ dataSourceId: 'source-1', namespace: 'public', table: 'asset_details', column: 'asset_name', keyColumn: 'equipment_id', tenantColumn: 'tenant_id' })
  await page.reload()
  await editor.getByRole('tab', { name: '属性配置', exact: true }).click()
  await editor.getByRole('button', { name: '编辑对象属性第2项', exact: true }).click()
  await expect(propertyDialog.getByLabel('属性2数据源', { exact: true })).toHaveValue('source-1')
  await propertyDialog.getByRole('button', { name: '取消', exact: true }).click()
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await editor.getByLabel('建模方向', { exact: true }).selectOption('db_2_schema')
  await editor.getByRole('button', { name: '从主表读取属性', exact: true }).click()
  await editor.getByRole('tab', { name: '属性配置', exact: true }).click()
  const properties = editor.getByRole('table', { name: '对象属性列表', exact: true })
  await expect(properties.getByRole('cell', { name: '设备名称', exact: true })).toBeVisible()
  await expect(properties.getByRole('cell', { name: 'serial_code', exact: true }).first()).toBeVisible()
  await expect(properties.getByRole('cell', { name: '资产名称', exact: true })).toBeVisible()
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.definitions.length).toBe(2)
  expect(state.definitions[1].schema!.mode).toBe('db_2_schema')
  expect(state.definitions[1].schema!.properties[0].id).toBe('name-field')
  expect(state.definitions[1].schema!.properties[2].mapping!.dataSourceId).toBe('source-1')
  await page.setViewportSize({ width: 390, height: 844 })
  const overflowing = await page.evaluate(() => [...document.querySelectorAll('main *')].filter((element) => {
    const rect = element.getBoundingClientRect()
    return rect.width > 0 && rect.right > innerWidth + 1 && !element.closest('.table-wrap, [role="tablist"]')
  }).slice(0, 8).map((element) => ({ class: element.className, width: element.getBoundingClientRect().width })))
  expect(overflowing).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await editor.getByRole('button', { name: '返回模型列表', exact: true }).click()
  await page.getByRole('link', { name: '数据源配置', exact: true }).click()
  await expect(page.getByRole('heading', { name: '数据源配置', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
