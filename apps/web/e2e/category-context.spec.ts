import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, Organization } from '../src/lib/types'

// Browser interaction coverage with an isolated in-memory HTTP fixture. This
// suite never signs into or writes to the developer's real databases.
async function fixture(page: Page, administrator = false) {
  const tenantId = 'browser-fixture'
  const categories: Organization[] = [{ id: 'category-a', tenantId, name: '生产管理' }, { id: 'category-b', tenantId, name: '质量管理' }]
  const groups: Organization[] = categories.map((category, index) => ({ id: `group-${index}`, tenantId, categoryId: category.id, name: index ? '质检模型' : '生产模型' }))
  const current: DefinitionRecord[] = categories.map((category, index) => ({
    definition: { id: `schema-${index}`, tenantId, categoryId: category.id, groupId: groups[index].id, kind: 'schema', name: index ? '质量设备' : '生产设备', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: `field-${index}`, name: '设备名称', dataType: 'string' }] } },
    etag: `etag-${index}`, status: 'active',
  }))
  const drafts: DefinitionRecord[] = []
  const writes: Record<string, unknown>[] = []
  let queries = 0
  const account = { user: { id: 'fixture-user', tenantId, username: 'operator', displayName: '操作员', administrator, enabled: true, roleIds: [], etag: 'user-etag' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' }
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname
    const method = route.request().method()
    const data = method === 'POST' ? route.request().postDataJSON() as Record<string, unknown> | null : null
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply(account)
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema','enum','link','action','function'], lifecycle: ['draft','active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 200, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') {
      if (method === 'GET') return reply({ data: categories, hasMore: false })
      const category = { id: `category-${categories.length}`, tenantId, name: String(data?.name) }
      categories.push(category)
      return reply(category, 201)
    }
    if (path === '/api/metadata/groups') {
      if (method === 'GET') return reply({ data: groups.filter((group) => group.categoryId === url.searchParams.get('categoryId')), hasMore: false })
      const group = { id: `group-${groups.length}`, tenantId, categoryId: String(data?.categoryId), name: String(data?.name) }
      groups.push(group)
      return reply(group, 201)
    }
    if (path === '/api/metadata/definitions') {
      if (method === 'GET') return reply({ data: url.searchParams.get('view') === 'draft' ? drafts : current, hasMore: false })
      writes.push(data!)
      const record = { definition: { ...data, id: `draft-${drafts.length}`, tenantId }, etag: 'saved-draft', status: 'draft' } as DefinitionRecord
      drafts.push(record)
      return reply(record, 201)
    }
    const definition = path.match(/^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/)
    if (method === 'GET' && definition) {
      const record = (definition[2] ? drafts : current).find((item) => item.definition.id === definition[1])
      return record ? reply(record) : reply({ error: { code: 'not_found', message: '未找到定义' } }, 404)
    }
    if (path === '/api/data/query') {
      queries++
      const schemaId = (data?.from as { schemaId: string }).schemaId
      const record = current.find((item) => item.definition.id === schemaId)!
      const field = record.definition.schema!.properties[0]
      return reply({ data: [{ id: 'instance', schemaId, etag: 'instance-etag', values: { [field.id]: schemaId === 'schema-0' ? '生产泵一号' : '质检泵一号' } }], fields: [field], hasMore: true, nextCursor: 'fixture-cursor' })
    }
    return reply({ error: { code: 'unhandled_fixture_request', message: `未定义的浏览器模拟接口：${method} ${path}` } }, 404)
  })
  return { writes, current, groups, queryCount: () => queries }
}

test('本体建模分组独立导航五类定义及数据源，刷新、返回与未保存保护保留类型', async ({ page }) => {
  const state = await fixture(page, true)
  for (const [kind, name] of [['enum', '设备状态'], ['link', '设备关联'], ['action', '设备操作'], ['function', '设备计算']] as const) {
    state.current.push({ etag: `${kind}-etag`, status: 'active', definition: {
      id: `${kind}-item`, tenantId: 'browser-fixture', categoryId: 'category-a', groupId: 'group-0', kind, name,
      ...(kind === 'enum' ? { enum: { values: [{ code: 'running', label: '运行中' }] } } : {}),
      ...(kind === 'link' ? { link: { sourceSchemaId: 'schema-0', targetSchemaId: 'schema-0', forwardName: '关联设备', reverseName: '来源设备', cardinality: '1:N', onDelete: 'restrict' } as const } : {}),
      ...(kind === 'action' ? { action: { input: [], output: [], sideEffects: 'none' } as const } : {}),
      ...(kind === 'function' ? { function: { input: [], output: [], sideEffects: 'none' } as const } : {}),
    } })
  }
  await page.goto('/#models')
  const navigation = page.getByRole('navigation', { name: '工作台导航' })
  const modeling = navigation.getByRole('group', { name: '本体建模', exact: true })
  await expect(modeling.getByRole('link')).toHaveText(['对象模型', '枚举', '关系', '动作', '函数', '数据源配置'])
  await expect(page.getByRole('group', { name: '定义类型', exact: true })).toHaveCount(0)
  for (const [name, record] of [['对象模型', '生产设备'], ['枚举', '设备状态'], ['关系', '设备关联'], ['动作', '设备操作'], ['函数', '设备计算']]) {
    await modeling.getByRole('link', { name, exact: true }).click()
    await expect(page.getByRole('heading', { name, exact: true })).toBeVisible()
    await expect(modeling.getByRole('link', { name, exact: true })).toHaveAttribute('aria-current', 'page')
    await expect(page.getByRole('table', { name: '按分组排列的业务模型' }).locator('.model-list-row')).toHaveCount(1)
    await expect(page.getByRole('button', { name: record, exact: true })).toBeVisible()
  }
  await modeling.getByRole('link', { name: '对象模型', exact: true }).click()
  await expect(page.getByRole('heading', { name: '对象模型', exact: true })).toBeVisible()
  await page.screenshot({ path: '../../.cache/model-navigation-desktop.png', fullPage: true })
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 844 })
    await expect(modeling.getByRole('link', { name: '数据源配置', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (width === 390) await page.screenshot({ path: '../../.cache/model-navigation-mobile.png', fullPage: true })
  }
  await page.setViewportSize({ width: 1440, height: 1000 })
  await modeling.getByRole('link', { name: '枚举', exact: true }).click()
  await page.getByRole('button', { name: '设备状态', exact: true }).click()
  await expect(page).toHaveURL(/#models\/enum-item$/)
  await expect(page.getByRole('region', { name: '模型编辑', exact: true })).toBeVisible()
  await page.reload()
  await expect(modeling.getByRole('link', { name: '枚举', exact: true })).toHaveAttribute('aria-current', 'page')
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await editor.getByRole('button', { name: '返回模型列表', exact: true }).click()
  await expect(page).toHaveURL(/#models\?kind=enum$/)
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  await expect(editor.getByRole('combobox', { name: '类型', exact: true })).toHaveValue('enum')
  await editor.getByLabel('业务名称', { exact: true }).fill('尚未保存的枚举')
  page.once('dialog', (dialog) => dialog.dismiss())
  await modeling.getByRole('link', { name: '函数', exact: true }).click()
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('尚未保存的枚举')
  page.once('dialog', (dialog) => dialog.accept())
  await modeling.getByRole('link', { name: '函数', exact: true }).click()
  await expect(page.getByRole('heading', { name: '函数', exact: true })).toBeVisible()
  await modeling.getByRole('link', { name: '数据源配置', exact: true }).click()
  await expect(page.getByRole('heading', { name: '数据源配置', exact: true })).toBeVisible()
  await modeling.getByRole('link', { name: '枚举', exact: true }).click()
  await page.getByLabel('当前分类', { exact: true }).selectOption('category-b')
  await expect(modeling.getByRole('link', { name: '枚举', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('button', { name: '设备状态', exact: true })).toHaveCount(0)
  expect(state.writes).toEqual([])
})

test('统一管理页创建目录，模型只选择当前分类下的分组', async ({ page }) => {
  const state = await fixture(page)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto('/')
  await expect(page.getByRole('group', { name: '本体建模', exact: true }).getByRole('link', { name: '数据源配置', exact: true })).toHaveCount(0)
  await page.getByRole('link', { name: '分类与分组', exact: true }).click()
  await page.getByLabel('新分类名称').fill('售后管理')
  await page.getByRole('button', { name: '创建分类', exact: true }).click()
  await expect(page.getByLabel('当前分类', { exact: true }).locator('option:checked')).toHaveText('售后管理')
  await page.getByLabel('新分组名称').fill('售后模型')
  await page.getByRole('button', { name: '创建分组', exact: true }).click()
  await expect(page.getByText('售后模型', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
  await expect(page.getByRole('button', { name: '管理分类', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  const dialog = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(dialog.getByRole('combobox', { name: '分类', exact: true })).toHaveCount(0)
  await dialog.getByLabel('业务名称', { exact: true }).fill('售后设备')
  const group = dialog.getByRole('combobox', { name: '分组', exact: true })
  await group.selectOption({ label: '售后模型' })
  await expect(group.locator('option')).toHaveText(['选择分组', '售后模型'])
  await dialog.getByRole('tab', { name: '属性配置', exact: true }).click()
  await dialog.getByRole('button', { name: '编辑对象属性第1项', exact: true }).click()
  const propertyDialog = page.getByRole('dialog', { name: '编辑属性', exact: true })
  await propertyDialog.getByLabel('对象属性第1项名称').fill('设备名称')
  await propertyDialog.getByRole('button', { name: '应用修改', exact: true }).click()
  await dialog.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ categoryId: 'category-2', groupId: 'group-2', name: '售后设备' })
  expect(errors).toEqual([])
})

test('顶部分类跨页面共享、刷新恢复，切换清除查询结果与续页且不自动执行', async ({ page }) => {
  const state = await fixture(page)
  await page.goto('/#query')
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await page.getByRole('checkbox', { name: '设备名称', exact: true }).check()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(page.getByRole('region', { name: '查询结果' }).getByText('生产泵一号', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '下一页', exact: true })).toBeEnabled()
  await page.getByRole('combobox', { name: '当前分类', exact: true }).selectOption('category-b')
  await expect(page.getByRole('region', { name: '查询结果' }).getByText('生产泵一号', { exact: true })).toHaveCount(0)
  await page.getByRole('tab', { name: '可视化查询', exact: true }).click()
  await expect(page.getByRole('combobox', { name: '查询模型', exact: true }).locator('option')).toHaveText(['质量管理 / 质量设备'])
  expect(state.queryCount()).toBe(1)
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
  await expect(page.getByRole('button', { name: '质量设备', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '生产设备', exact: true })).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('combobox', { name: '当前分类', exact: true })).toHaveValue('category-b')
  await expect(page.getByRole('button', { name: '质量设备', exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('link', { name: '分类与分组', exact: true }).click()
  await expect(page.getByRole('heading', { name: '分类与分组', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await expect(page.getByRole('combobox', { name: '当前分类', exact: true })).toBeVisible()
})

test('模型表单前往统一页补充分组后，返回刷新候选并保留输入', async ({ page }) => {
  await fixture(page)
  await page.goto('/#organizations')
  await page.getByLabel('新分类名称').fill('待建目录')
  await page.getByRole('button', { name: '创建分类', exact: true }).click()
  await expect(page.getByLabel('当前分类', { exact: true })).toHaveValue('category-2')
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  let dialog = page.getByRole('region', { name: '模型编辑', exact: true })
  await dialog.getByLabel('业务名称', { exact: true }).fill('保留的设备草稿')
  await dialog.getByRole('button', { name: '前往分类与分组', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await page.getByLabel('新分组名称').fill('补充的分组')
  await page.getByRole('button', { name: '创建分组', exact: true }).click()
  await expect(page.getByText('补充的分组', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
  dialog = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(dialog.getByLabel('业务名称', { exact: true })).toHaveValue('保留的设备草稿')
  await dialog.getByRole('combobox', { name: '分组', exact: true }).selectOption({ label: '补充的分组' })
  await expect(dialog.getByRole('combobox', { name: '分组', exact: true })).toHaveValue('group-2')
})

test('模型默认按分组紧凑行显示，折叠、筛选与视图切换保留正确归组', async ({ page }) => {
  const state = await fixture(page)
  state.current[0].definition.description = '这是一段包含换行和长业务说明的描述。\n'.repeat(30)
  state.groups.push({ id: 'group-extra', tenantId: 'browser-fixture', categoryId: 'category-a', name: '设备字典' })
  for (let index = 0; index < 9; index++) {
    state.current.push({
      ...state.current[0],
      definition: { ...state.current[0].definition, id: `extra-${index}`, groupId: 'group-extra', name: `扩展设备${index}` },
    })
  }
  state.current.push({
    etag: 'enum-etag', status: 'draft',
    definition: { id: 'enum-extra', tenantId: 'browser-fixture', categoryId: 'category-a', groupId: 'group-extra', kind: 'enum', name: '设备状态', enum: { values: [{ code: 'running', label: '运行中' }] } },
  })
  await page.goto('/#models')
  await expect(page.getByRole('button', { name: '列表视图', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('link', { name: '对象模型', exact: true })).toHaveAttribute('aria-current', 'page')
  const table = page.getByRole('table', { name: '按分组排列的业务模型' })
  await expect(table.locator('.model-list-row')).toHaveCount(10)
  await expect(table.getByRole('button', { name: '设备状态', exact: true })).toHaveCount(0)
  const production = table.getByRole('rowgroup', { name: '生产模型', exact: true })
  const dictionary = table.getByRole('rowgroup', { name: '设备字典', exact: true })
  await expect(production.getByRole('button', { name: '生产设备', exact: true })).toBeVisible()
  await expect(dictionary.getByRole('button', { name: '扩展设备0', exact: true })).toBeVisible()
  await expect(table.locator('.model-list-row')).toHaveCount(10)
  await expect(table.getByRole('button', { name: '质量设备', exact: true })).toHaveCount(0)
  const rows = await table.locator('.model-list-row').evaluateAll((elements) => elements.map((element) => {
    const rect = element.getBoundingClientRect()
    return { height: rect.height, bottom: rect.bottom }
  }))
  expect(rows.every((row) => row.height <= 40 && row.bottom <= page.viewportSize()!.height)).toBe(true)
  await page.getByRole('button', { name: '收起分组：设备字典', exact: true }).click()
  await expect(dictionary.getByRole('button', { name: '扩展设备0', exact: true })).toHaveCount(0)
  await expect(production.getByRole('button', { name: '生产设备', exact: true })).toBeVisible()
  await page.getByLabel('搜索业务模型', { exact: true }).fill('扩展设备0')
  await expect(dictionary.getByRole('button', { name: '扩展设备0', exact: true })).toBeVisible()
  await expect(production).toHaveCount(0)
  await page.getByLabel('搜索业务模型', { exact: true }).fill('')
  await page.getByRole('button', { name: '网格视图', exact: true }).click()
  await expect(page.getByRole('region', { name: '设备字典', exact: true }).getByRole('button', { name: '扩展设备0', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '列表视图', exact: true }).click()
  for (const width of [760, 390]) {
    await page.setViewportSize({ width, height: 844 })
    await expect(production.getByRole('button', { name: '生产设备', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await table.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    expect(await production.locator('.model-list-name-button > span').evaluate((element) => element.getBoundingClientRect().width)).toBeGreaterThanOrEqual(48)
  }
  expect(state.writes).toEqual([])
})

test('模型独立编辑地址支持刷新、直接打开与未保存离开保护', async ({ page }) => {
  const state = await fixture(page)
  await page.goto('/#models')
  await page.getByRole('button', { name: '生产设备', exact: true }).click()
  await expect(page).toHaveURL(/#models\/schema-0$/)
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  await expect(editor.getByRole('heading', { name: '生产设备', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('table', { name: '按分组排列的业务模型' })).toHaveCount(0)
  await page.reload()
  await expect(editor.getByRole('tab', { name: '属性配置', selected: true, exact: true })).toBeVisible()
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('生产设备')
  await editor.getByLabel('业务名称', { exact: true }).fill('尚未保存的生产设备')
  page.once('dialog', (dialog) => dialog.dismiss())
  await page.getByRole('link', { name: '数据探索', exact: true }).click()
  await expect(page).toHaveURL(/#models\/schema-0$/)
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('尚未保存的生产设备')
  page.once('dialog', (dialog) => dialog.accept())
  await page.getByRole('link', { name: '数据探索', exact: true }).click()
  await expect(page).toHaveURL(/#query$/)
  await expect(editor).toBeHidden()
  await page.getByRole('link', { name: '对象模型', exact: true }).click()
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('生产设备')
  await editor.getByRole('button', { name: '返回模型列表', exact: true }).click()
  await page.getByRole('button', { name: '生产设备', exact: true }).click()
  await expect(page).toHaveURL(/#models\/schema-0$/)
  await page.goBack()
  await expect(page).toHaveURL(/#models$/)
  await expect(page.getByRole('table', { name: '按分组排列的业务模型' })).toBeVisible()
  await page.goto('/#models/schema-1')
  await expect(page.getByLabel('当前分类', { exact: true })).toHaveValue('category-b')
  await expect(editor.getByRole('tab', { name: '属性配置', selected: true, exact: true })).toBeVisible()
  await editor.getByRole('tab', { name: '基础信息', exact: true }).click()
  await expect(editor.getByLabel('业务名称', { exact: true })).toHaveValue('质量设备')
  expect(state.writes).toEqual([])
})
