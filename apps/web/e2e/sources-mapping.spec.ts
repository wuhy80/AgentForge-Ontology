import { test, expect, type Page } from '@playwright/test'
import type { DataSource, DataSourceInput, Definition, DefinitionRecord, SourceCompatibility, SourceTable } from '../src/lib/types'

// All connections, accounts, and tables are isolated HTTP fixtures. No external
// database is contacted or changed by this browser workflow.
async function mappingFixture(page: Page) {
  const tenantId = 'mapping-fixture'
  const sources: DataSource[] = []
  const sourceWrites: DataSourceInput[] = []
  const sourceTests: { input: DataSourceInput; etag: string | undefined }[] = []
  const sourceQueries: { sql: string; etag: string; csrf: string }[] = []
  const definitions: Definition[] = []
  const unhandled: string[] = []
  const compatibility = (input: DataSourceInput): SourceCompatibility => ({ connected: true, compatibilityStatus: 'supported', detectedProduct: input.driver, serverVersion: input.driver === 'mysql' ? '26.7.0' : '17.6', versionLine: input.driver === 'mysql' ? '26.7' : '17', capabilities: ['catalog', 'read', 'query', 'createTable', 'addColumn', 'createIndex'], checkedAt: '2026-11-07T08:00:00Z' })
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
    if (path === '/api/data/source-versions') return reply({ data: [
      { driver: 'mysql', versionLine: '26.7', label: 'MySQL 26.7', status: 'supported' },
      { driver: 'mysql', versionLine: '8.4', label: 'MySQL 8.4', status: 'pending' },
      { driver: 'postgresql', versionLine: '17', label: 'PostgreSQL 17', status: 'supported' },
      { driver: 'postgresql', versionLine: '16', label: 'PostgreSQL 16', status: 'pending' },
    ], hasMore: false })
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
      const source: DataSource = { ...publicInput, id: `source-${sources.length}`, tenantId, etag: 'source-etag', passwordSet: !!password, compatibility: compatibility(input) }
      sources.push(source)
      return reply(source, 201)
    }
    if (/^\/api\/data\/sources\/source-\d\/test$/.test(path)) {
      const input = (request.postDataJSON() || sources.find((source) => path.includes(`/${source.id}/`))) as DataSourceInput
      sourceTests.push({ input: structuredClone(input), etag: request.headers()['if-match'] })
      const checked = compatibility(input)
      if (input.host === 'different-version.invalid') {
        checked.compatibilityStatus = 'mismatch'; checked.serverVersion = '8.4.7'; checked.versionLine = '8.4'; checked.message = '预期MySQL 26.7，实际为8.4。'
      }
      return reply({ success: checked.compatibilityStatus === 'supported', serverVersion: checked.serverVersion, compatibility: checked })
    }
    const sourcePath = path.match(/^\/api\/data\/sources\/(source-\d)$/)
    if (sourcePath && method === 'GET') return reply(sources.find((source) => source.id === sourcePath[1]))
    const overviewPath = path.match(/^\/api\/data\/sources\/(source-\d)\/overview$/)
    if (overviewPath && method === 'GET') {
      const source = sources.find((source) => source.id === overviewPath[1])!
      const metric = (unit: string) => ({ available: false, unit, scope: 'database', basis: '隔离浏览器测试不连接数据库', reason: '此用例未提供数据库指标' })
      return reply({ sourceId: source.id, etag: source.etag, database: source.database, serverVersion: source.compatibility?.serverVersion || '26.7.0', charset: 'utf8mb4', checkedAt: '2026-11-08T02:00:00Z', metrics: { connections: metric('connections'), storageBytes: metric('bytes'), qps: metric('queries/s') } })
    }
    if (/^\/api\/data\/sources\/source-\d\/query$/.test(path)) {
      sourceQueries.push({ sql: request.postDataJSON().sql, etag: request.headers()['if-match'], csrf: request.headers()['x-csrf-token'] })
      return reply({ columns: ['金额', '备注'], rows: [['9007199254740993.0001', '中文记录'], [null, '']], limit: 100, hasMore: false, elapsedMs: 2 })
    }
    const tablePath = path.match(/^\/api\/data\/sources\/(source-\d)\/tables(?:\/([^/]+))?$/)
    if (tablePath) {
      const table = tablePath[1] === 'source-0' ? primary : related
      return tablePath[2] ? reply(table) : reply({ data: [{ namespace: table.namespace, name: table.name, type: 'BASE TABLE' }], hasMore: false })
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { sources, sourceWrites, sourceTests, sourceQueries, definitions, unhandled }
}

test('数据源表格提供测试、编辑和管理，管理查看字段索引并执行只读SQL，宽窄屏均可用', async ({ page }) => {
  const state = await mappingFixture(page)
  state.sources.push({ id: 'source-0', tenantId: 'mapping-fixture', name: '独立业务库', driver: 'mysql', host: 'fixture.invalid', port: 3306, database: 'main', username: 'reader', tlsMode: 'disable', tenantIsolation: 'dedicated', enabled: true, passwordSet: true, etag: 'source-etag' })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#sources')
  const directory = page.getByRole('region', { name: '已配置的数据源' })
  const row = directory.getByRole('row', { name: /独立业务库/ })
  await expect(directory.getByRole('columnheader', { name: '操作', exact: true })).toBeVisible()
  await row.getByRole('button', { name: '测试', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('连接成功')
  await expect(page.getByRole('region', { name: '数据源连接配置' })).toHaveCount(0)
  await row.getByRole('button', { name: '编辑 独立业务库', exact: true }).click()
  await expect(page.getByLabel('数据库主机', { exact: true })).toHaveValue('fixture.invalid')
  await page.getByRole('button', { name: '关闭连接编辑' }).click()
  await row.getByRole('button', { name: '管理', exact: true }).click()
  await expect(page.getByRole('region', { name: '数据库基础信息' })).toContainText('main')
  await page.getByRole('button', { name: 'main.equipment', exact: true }).click()
  await expect(page.getByRole('region', { name: '数据源表结构' }).getByRole('cell', { name: 'PRIMARY', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'SQL控制台', exact: true }).click()
  await page.getByRole('button', { name: '填入查询', exact: true }).click()
  await page.getByRole('menuitem', { name: '当前表查询', exact: true }).click()
  await expect(page.getByLabel('SQL 语句')).toHaveText('SELECT * FROM `main`.`equipment` LIMIT 100;')
  await page.getByRole('button', { name: '执行 SQL', exact: true }).click()
  const results = page.getByRole('region', { name: 'SQL 查询结果' })
  await expect(results.getByRole('cell', { name: '9007199254740993.0001', exact: true })).toBeVisible()
  await expect(results.getByRole('cell', { name: 'NULL', exact: true })).toBeVisible()
  await expect(results.getByRole('cell', { name: '空字符串', exact: true })).toBeVisible()
  expect(state.sourceQueries).toEqual([{ sql: 'SELECT * FROM `main`.`equipment` LIMIT 100;', etag: '"source-etag"', csrf: 'fixture-csrf' }])
  for (const width of [1920, 1440, 760, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  await page.getByLabel('SQL 语句').fill('SELECT name FROM equipment LIMIT 2;')
  await expect(results).toHaveCount(0)
  await page.getByRole('button', { name: '返回数据源列表' }).click()
  await expect(directory).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

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
  await page.getByRole('button', { name: '测试当前配置', exact: true }).click()
  await expect(page.getByText('当前配置检查通过 · PostgreSQL 17.6', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '查看数据库表', exact: true }).click()
  await page.getByRole('button', { name: 'public.asset_details', exact: true }).click()
  await expect(page.getByRole('region', { name: '数据源表结构' }).getByRole('cell', { name: 'asset_name', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
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

test('数据源版本选择核验当前表单，版本不匹配不误报成功，窄屏可读', async ({ page }) => {
  const state = await mappingFixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#sources')
  await page.getByRole('button', { name: '添加数据源', exact: true }).click()
  const form = page.getByRole('region', { name: '数据源连接配置' })
  const check = page.getByRole('region', { name: '数据库版本检查' })
  await expect(form.getByLabel('数据库版本', { exact: true })).toHaveValue('auto')
  await expect(form.getByRole('option', { name: 'MySQL 8.4（待适配）', exact: true })).toBeDisabled()
  await form.getByLabel('数据库版本', { exact: true }).selectOption('26.7')
  await form.getByLabel('数据源名称', { exact: true }).fill('版本核验库')
  await form.getByLabel('数据库主机', { exact: true }).fill('current-version.invalid')
  await form.getByLabel('数据库名称', { exact: true }).fill('fixtures')
  await form.getByLabel('数据库用户名', { exact: true }).fill('fixture_reader')
  await form.getByLabel('数据库密码', { exact: true }).fill('fixture-only-password')
  await form.getByRole('button', { name: '保存数据源', exact: true }).click()
  await expect(check.getByLabel('兼容状态')).toHaveText('兼容')
  await expect(check.getByLabel('实际数据库版本')).toHaveText('26.7.0')
  expect(state.sourceWrites[0]).toMatchObject({ versionPolicy: 'pinned', expectedVersionLine: '26.7' })
  await form.getByLabel('数据库主机', { exact: true }).fill('different-version.invalid')
  await expect(check.getByLabel('兼容状态')).toHaveText('待检测')
  await expect(check.getByLabel('实际数据库版本')).toHaveText('尚未识别')
  await form.getByRole('button', { name: '测试当前配置', exact: true }).click()
  await expect(check.getByLabel('兼容状态')).toHaveText('版本不匹配')
  await expect(check.getByLabel('实际数据库版本')).toHaveText('8.4.7')
  await expect(page.getByRole('alert')).toContainText('预期MySQL 26.7，实际为8.4。')
  await expect(page.getByRole('status')).toHaveCount(0)
  expect(state.sourceTests).toHaveLength(1)
  expect(state.sourceTests[0]).toMatchObject({ input: { host: 'different-version.invalid', versionPolicy: 'pinned', expectedVersionLine: '26.7' }, etag: '"source-etag"' })
  expect(state.sourceTests[0].input).not.toHaveProperty('password')
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(check.getByLabel('实际数据库版本')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  await form.getByLabel('数据库类型', { exact: true }).selectOption('postgresql')
  await expect(form.getByLabel('数据库版本', { exact: true })).toHaveValue('auto')
  await expect(form.getByRole('option', { name: 'PostgreSQL 17', exact: true })).toBeEnabled()
  await expect(check.getByLabel('兼容状态')).toHaveText('待检测')
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
