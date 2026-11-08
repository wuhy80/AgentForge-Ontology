import { test, expect, type Page } from '@playwright/test'
import type { Definition, DefinitionRecord } from '../src/lib/types'

// Isolated API fixture: this suite never signs in to or writes a real database.
async function fixture(page: Page) {
  const tenantId = 'model-tabs-fixture'
  const base = { tenantId, groupId: 'production-group', categoryId: 'production' }
  const current: DefinitionRecord[] = [
    { status: 'active', etag: 'equipment-current', definition: {
      ...base, id: 'equipment', kind: 'schema', name: '生产设备', description: '设备基础档案',
      schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name-field', name: '设备名称', dataType: 'string' }], indexes: [{ id: 'name-index', name: '名称索引', unique: false, fieldIds: ['name-field'] }] },
    } },
    { status: 'active', etag: 'report-current', definition: {
      ...base, categoryId: 'quality', groupId: 'quality-group', id: 'report', kind: 'schema', name: '检验报告',
      schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'report-name', name: '报告名称', dataType: 'string' }] },
    } },
    { status: 'active', etag: 'link-current', definition: {
      ...base, id: 'equipment-report', kind: 'link', name: '设备检验报告',
      link: { sourceSchemaId: 'equipment', targetSchemaId: 'report', forwardName: '检验报告', reverseName: '所属设备', cardinality: '1:N', onDelete: 'restrict' },
    } },
    { status: 'active', etag: 'action-current', definition: {
      ...base, categoryId: 'quality', groupId: 'quality-group', id: 'calibrate', kind: 'action', name: '设备校准',
      action: { sideEffects: 'dataWrite', targetSchemaIds: ['equipment'], input: [], output: [] },
    } },
  ]
  const drafts = new Map<string, DefinitionRecord>()
  const writes: Definition[] = []
  const unhandled: string[] = []
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'author', tenantId, username: 'author', displayName: '模型作者', administrator: false, enabled: true, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/schemas/equipment/links') return reply({ data: [], hasMore: false })
    if (path === '/api/data/query' && method === 'POST') return reply({ fields: current[0].definition.schema!.properties, data: [{ id: 'equipment-1', schemaId: 'equipment', etag: 'equipment-1', values: { 'name-field': '设备一' } }], hasMore: false })
    const mappingTarget = path.match(/^\/api\/data\/schemas\/([^/]+)\/storage-mapping$/)
    if (mappingTarget) {
      const record = current.find((entry) => entry.definition.id === mappingTarget[1] && entry.definition.schema)
      if (!record) return reply({ code: 'not_found', message: '未找到对象' }, 404)
      const primary = { dataSourceName: '平台默认存储', driver: 'mysql', namespace: 'fixture_data', table: 'af_s_fixture_equipment', keyColumn: 'id', tenantColumn: 'tenant_id' }
      return reply({ schemaId: record.definition.id, definitionETag: record.etag, managed: true, mode: 'schema_2_db', primary,
        properties: record.definition.schema!.properties.map((property) => ({ ...primary, propertyId: property.id, propertyName: property.name, column: 'f_fixture_name', databaseType: 'varchar(255)', exists: true })),
      })
    }
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'production', tenantId, name: '生产管理' }, { id: 'quality', tenantId, name: '质量管理' }], hasMore: false })
    if (path === '/api/metadata/groups') {
      const categoryId = url.searchParams.get('categoryId')
      return reply({ data: [{ id: `${categoryId}-group`, tenantId, categoryId, name: categoryId === 'production' ? '设备档案' : '质量操作' }], hasMore: false })
    }
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [...drafts.values()] : current, hasMore: false })
    const target = path.match(/^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/)
    if (target) {
      if (method === 'PUT' && target[2]) {
        const definition = request.postDataJSON() as Definition
        writes.push(structuredClone(definition))
        const record: DefinitionRecord = { definition, status: 'draft', etag: `draft-${writes.length}` }
        drafts.set(target[1], record)
        return reply(record)
      }
      const record = target[2] ? drafts.get(target[1]) : current.find((entry) => entry.definition.id === target[1])
      return record ? reply(record) : reply({ error: { code: 'not_found', message: '未找到草稿' } }, 404)
    }
    unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled', message: '未定义的隔离接口' } }, 404)
  })
  return { writes, unhandled }
}

test('对象页签保留编辑、整份保存并导航相关关系和跨分类动作', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/#models/equipment')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByRole('tab', { name: '属性配置', selected: true, exact: true })).toBeVisible()
  await expect(editor.getByRole('tab')).toHaveCount(6)
  await expect(editor.getByRole('tabpanel')).toHaveCount(1)
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await editor.getByLabel('业务名称', { exact: true }).fill('生产设备台账')
  await editor.getByRole('tab', { name: '属性配置', exact: true }).click()
  const properties = editor.getByRole('table', { name: '对象属性列表', exact: true })
  await expect(properties.getByRole('cell', { name: '设备名称', exact: true })).toBeVisible()
  await expect(editor.getByLabel('对象属性第1项名称')).toHaveCount(0)
  await editor.getByRole('button', { name: '编辑对象属性第1项', exact: true }).click()
  const propertyDialog = page.getByRole('dialog', { name: '编辑属性', exact: true })
  await expect(propertyDialog.getByLabel('对象属性第1项名称')).toHaveValue('设备名称')
  await expect(propertyDialog.getByLabel('对象属性第1项类型')).toBeDisabled()
  const mapping = propertyDialog.getByRole('region', { name: '属性实际数据库映射', exact: true })
  for (const value of ['平台默认存储', 'fixture_data', 'af_s_fixture_equipment', 'f_fixture_name', 'varchar(255)']) {
    await expect(mapping.getByText(value, { exact: true })).toBeVisible()
  }
  await propertyDialog.getByLabel('对象属性第1项名称').fill('设备中文名称')
  await propertyDialog.getByRole('button', { name: '应用修改', exact: true }).click()
  await expect(propertyDialog).toBeHidden()
  await expect(properties.getByRole('cell', { name: '设备中文名称', exact: true })).toBeVisible()
  await expect(editor.getByText('查看当前生效的数据库映射', { exact: true })).toHaveCount(0)
  await expect(editor.getByRole('table', { name: '当前生效的数据库映射', exact: true })).toHaveCount(0)
  await editor.getByRole('tab', { name: '索引信息', exact: true }).click()
  await expect(editor.getByLabel('索引1名称')).toHaveValue('名称索引')
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('生产设备台账')
  await editor.getByRole('tab', { name: '动作信息', exact: true }).click()
  await expect(editor.getByRole('link', { name: '设备校准', exact: true })).toHaveAttribute('href', '#models/calibrate')
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ name: '生产设备台账', schema: { properties: [{ id: 'name-field', name: '设备中文名称' }], indexes: [{ id: 'name-index', name: '名称索引', fieldIds: ['name-field'] }] } })
  await editor.getByRole('tab', { name: 'Link 信息', exact: true }).click()
  await editor.getByRole('button', { name: '图形', exact: true }).click()
  const localGraph = editor.getByRole('region', { name: '关系建模画布', exact: true })
  await expect(localGraph.getByRole('button', { name: '选择关系：设备检验报告（已生效）', exact: true })).toBeVisible()
  await expect(localGraph.getByText('来自 质量管理', { exact: true })).toBeVisible()
  await expect(editor.getByRole('table', { name: '关联 Link', exact: true })).toHaveCount(0)
  await localGraph.getByRole('button', { name: '适配画布', exact: true }).click()
  await editor.getByRole('button', { name: '列表', exact: true }).click()
  await editor.getByRole('link', { name: '设备检验报告', exact: true }).click()
  await expect(page).toHaveURL(/#models\/equipment-report$/)
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('设备检验报告')
  await page.goBack()
  await expect(page).toHaveURL(/#models\/equipment$/)
  await page.setViewportSize({ width: 390, height: 844 })
  for (const name of ['基础信息', '属性配置', '索引信息', 'Link 信息', '动作信息', '数据预览']) {
    await editor.getByRole('tab', { name, exact: true }).click()
    await expect(editor.getByRole('tabpanel', { name, exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name}在390px下不应撑宽页面`).toBe(true)
  }
  await editor.getByRole('tab', { name: '动作信息', exact: true }).click()
  await editor.getByRole('link', { name: '设备校准', exact: true }).click()
  await expect(page).toHaveURL(/#models\/calibrate$/)
  await expect(page.getByLabel('当前分类', { exact: true })).toHaveValue('quality')
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('设备校准')
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
