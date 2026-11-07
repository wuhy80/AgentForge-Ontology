import { test, expect, type Page } from '@playwright/test'
import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const repo = fileURLToPath(new URL('../../../', import.meta.url))
const credentialsFile = resolve(repo, '.cache/dev/admin.json')
test.skip(!existsSync(credentialsFile), '需要先启动本项目的本地 MySQL/OPA 和两个后端服务')

function account() {
  return JSON.parse(readFileSync(credentialsFile, 'utf8').replace(/^\uFEFF/, '')) as { Tenant: string; Username: string; Password: string }
}

async function connect(page: Page) {
  const login = page.getByRole('button', { name: '登录', exact: true })
  const navigation = page.getByRole('navigation', { name: '工作台导航' })
  await expect(login.or(navigation)).toBeVisible()
  if (await login.isVisible()) {
    const credentials = account()
    await page.getByLabel('租户', { exact: true }).fill(credentials.Tenant)
    await page.getByLabel('用户名', { exact: true }).fill(credentials.Username)
    await page.getByLabel('密码', { exact: true }).fill(credentials.Password)
    await login.click()
  }
  await expect(page.getByRole('navigation', { name: '工作台导航' })).toBeVisible()
}

test('真实服务：分类、可重新发现的草稿、发布、精确查询和实例维护', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', error => pageErrors.push(error.message))
  const category = `前端验收${Date.now()}`
  await page.goto('/')
  await connect(page)
  await page.getByRole('link', { name: '分类与分组', exact: true }).click()
  await page.getByLabel('新分类名称', { exact: true }).fill(category)
  await page.getByRole('button', { name: '创建分类', exact: true }).click()
  await expect(page.getByRole('combobox', { name: '当前分类', exact: true })).toHaveValue(/.+/)
  await expect(page.getByRole('combobox', { name: '当前分类', exact: true }).locator('option:checked')).toHaveText(category)
  await page.getByLabel('新分组名称').fill('基础模型')
  await page.getByRole('button', { name: '创建分组', exact: true }).click()
  await expect(page.getByText('基础模型', { exact: true })).toBeVisible()
  await page.getByRole('link', { name: '模型管理', exact: true }).click()
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  let dialog = page.getByRole('region', { name: '模型编辑', exact: true })
  await dialog.getByLabel('业务名称', { exact: true }).fill('精密设备')
  await dialog.getByRole('combobox', { name: '分组', exact: true }).selectOption({ label: '基础模型' })
  await dialog.getByLabel('描述', { exact: true }).fill('中文建模与精确数值查询的浏览器验收模型')
  await dialog.getByRole('tab', { name: '属性配置', exact: true }).click()
  await dialog.getByLabel('对象属性第1项名称').fill('设备名称')
  for (const [index, name, type] of [[2, '累计计数', 'int64'], [3, '测量值', 'decimal'], [4, '备注', 'string']] as const) {
    await dialog.getByRole('button', { name: '添加属性', exact: true }).click()
    await dialog.getByLabel(`对象属性第${index}项名称`).fill(name)
    await dialog.getByLabel(`对象属性第${index}项类型`).selectOption(type)
  }
  await dialog.getByLabel('对象属性第3项总位数').fill('24')
  await dialog.getByLabel('对象属性第3项小数位').fill('4')
  await dialog.locator('.model-properties-table tbody tr').nth(3).getByLabel('允许空值').check()
  await dialog.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(dialog.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')).toBeVisible()
  await dialog.getByRole('button', { name: '返回模型列表', exact: true }).click()
  // A reload restores the server session without exposing a credential to JS.
  await page.reload(); await connect(page)
  await expect(page.getByRole('combobox', { name: '当前分类', exact: true }).locator('option:checked')).toHaveText(category)
  await page.getByRole('button', { name: '精密设备', exact: true }).click()
  dialog = page.getByRole('region', { name: '模型编辑', exact: true })
  await dialog.getByRole('tab', { name: '索引信息', exact: true }).click()
  await dialog.getByRole('button', { name: '添加索引', exact: true }).click()
  await dialog.getByLabel('索引1名称').fill('按设备名称')
  await dialog.getByLabel('索引1第一个属性').selectOption({ label: '设备名称' })
  await dialog.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(dialog.getByRole('button', { name: '预览计划', exact: true })).toBeEnabled()
  await dialog.getByRole('button', { name: '预览计划', exact: true }).click()
  await expect(dialog.locator('pre')).toContainText('CREATE TABLE')
  await dialog.getByRole('button', { name: '发布定义', exact: true }).click()
  await expect(dialog.getByText('发布成功，当前定义已生效。')).toBeVisible()
  await expect(dialog.getByLabel('对象属性第1项类型')).toBeDisabled()
  await dialog.getByRole('button', { name: '返回模型列表', exact: true }).click()
  await page.getByRole('button', { name: '查询', exact: true }).click()
  await expect(page.getByRole('heading', { name: '探索业务数据' })).toBeVisible()

  for (const [name, amount] of [['输送泵一号', '9007199254740993.0000'], ['输送泵二号', '9007199254740993.0001']]) {
    await page.getByRole('button', { name: '新建实例', exact: true }).click()
    dialog = page.getByRole('dialog')
    for (const [field, value] of [['设备名称', name], ['累计计数', '9223372036854775807'], ['测量值', amount]]) {
      await dialog.getByLabel(`${field}的写入方式`).selectOption('value')
      await dialog.getByLabel(field, { exact: true }).fill(value)
    }
    await dialog.getByLabel('备注的写入方式').selectOption('null')
    await dialog.getByRole('button', { name: '创建实例', exact: true }).click()
    await expect(dialog.getByText('实例已创建', { exact: true })).toBeVisible()
    await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  }
  for (const field of ['设备名称', '累计计数', '测量值', '备注']) await page.getByRole('checkbox', { name: field, exact: true }).check()
  await page.getByLabel('排序字段', { exact: true }).selectOption({ label: '设备名称' })
  await page.getByLabel('每页条数').fill('1')
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  const results = page.getByRole('region', { name: '查询结果', exact: true })
  await expect(results.getByText('输送泵一号', { exact: true })).toBeVisible()
  await expect(results.getByText('9223372036854775807', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(results.getByText('输送泵二号', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '添加条件', exact: true }).click()
  await page.getByLabel('筛选字段', { exact: true }).selectOption({ label: '测量值' })
  await page.getByLabel('筛选运算符', { exact: true }).selectOption('gt')
  await page.getByLabel('比较值', { exact: true }).fill('9007199254740993.0000')
  await expect(results.getByText(/结果已过期/)).toBeVisible()
  await expect(page.getByRole('button', { name: '下一页', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(results.getByText('9007199254740993.0001', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '查看', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: '编辑实例', exact: true }).click()
  await dialog.getByLabel('设备名称的写入方式').selectOption('value')
  await dialog.getByLabel('设备名称', { exact: true }).fill('输送泵二号（已校验）')
  await dialog.getByRole('button', { name: '保存修改', exact: true }).click()
  await expect(dialog.getByText('实例已保存', { exact: true })).toBeVisible()
  await expect(dialog.getByText('9007199254740993.0001', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '删除实例', exact: true }).click()
  await dialog.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(dialog).toBeHidden()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(results.getByText('没有匹配的数据', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '移除此条件', exact: true }).click()
  await page.getByRole('button', { name: '运行查询', exact: true }).click()
  await expect(results.getByText('输送泵一号', { exact: true })).toBeVisible()

  const artifacts = resolve(repo, '.cache/frontend-verification')
  mkdirSync(artifacts, { recursive: true })
  await page.evaluate(() => { (document.activeElement as HTMLElement | null)?.blur(); window.scrollTo(0, 0) })
  await page.screenshot({ path: resolve(artifacts, 'data-explorer.png'), fullPage: true })
  await page.getByRole('link', { name: '模型管理', exact: true }).click()
  await page.screenshot({ path: resolve(artifacts, 'models.png'), fullPage: true })
  await page.getByRole('link', { name: '工作空间', exact: true }).click()
  await page.screenshot({ path: resolve(artifacts, 'overview.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole('link', { name: '数据探索', exact: true })).toBeVisible()
  await page.screenshot({ path: resolve(artifacts, 'mobile.png'), fullPage: true })
  const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
  const overflow = fits ? [] : await page.evaluate(() => Array.from(document.querySelectorAll('body *')).filter(element => {
    const rect = element.getBoundingClientRect()
    return rect.width > 0 && rect.right > window.innerWidth + 1
  }).slice(0, 12).map(element => ({ tag: element.tagName, class: element.className, right: element.getBoundingClientRect().right })))
  expect(overflow, '窄屏布局不应出现页面横向溢出').toEqual([])
  await page.getByRole('link', { name: '模型管理', exact: true }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  await expect(page.getByRole('region', { name: '模型编辑', exact: true })).toBeVisible()
  // A clean editor can leave and resume through the workspace navigation.
  await page.evaluate(() => { window.location.hash = 'overview' })
  await expect(page.getByRole('region', { name: '模型编辑', exact: true })).toBeHidden()
  await page.evaluate(() => { window.location.hash = 'models' })
  await expect(page.getByRole('region', { name: '模型编辑', exact: true })).toBeVisible()
  await page.getByRole('region', { name: '模型编辑', exact: true }).getByRole('button', { name: '返回模型列表', exact: true }).click()
  await page.getByRole('link', { name: '数据探索', exact: true }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.evaluate(() => { (document.activeElement as HTMLElement | null)?.blur(); window.scrollTo(0, 0) })
  await page.screenshot({ path: resolve(artifacts, 'mobile-query.png'), fullPage: true })
  expect(pageErrors).toEqual([])
  await page.getByRole('button', { name: '退出工作空间', exact: true }).click()
  await expect(page.getByRole('heading', { name: '登录你的工作空间', exact: true })).toBeVisible()
  const persisted = await page.evaluate(() => ({ local: localStorage.length, sessionKeys: Object.keys(sessionStorage) }))
  expect(persisted.local).toBe(0)
  expect(persisted.sessionKeys.every((key) => key.startsWith('ontology.category.'))).toBe(true)
})

test('真实服务：枚举、函数、动作和关系通过中文表单维护', async ({ page }) => {
  const categoryName = `定义验收${Date.now()}`
  const credentials = account()
  const loggedIn = await page.request.post('/api/metadata/auth/login', { headers: { Origin: 'http://127.0.0.1:5173' }, data: { tenant: credentials.Tenant, username: credentials.Username, password: credentials.Password } })
  expect(loggedIn.ok()).toBe(true)
  const session = await loggedIn.json() as { csrfToken: string }
  const headers = { 'X-CSRF-Token': session.csrfToken, Origin: 'http://127.0.0.1:5173' }
  const categoryResponse = await page.request.post('/api/metadata/categories', { headers, data: { name: categoryName } })
  expect(categoryResponse.ok()).toBe(true)
  const category = await categoryResponse.json() as { id: string }
  const groupResponse = await page.request.post('/api/metadata/groups', { headers, data: { name: '定义集合', categoryId: category.id } })
  expect(groupResponse.ok()).toBe(true)
  const group = await groupResponse.json() as { id: string }
  const schemaResponse = await page.request.post('/api/metadata/definitions', { headers, data: {
    categoryId: category.id, groupId: group.id, kind: 'schema', name: '设备端点',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ name: '名称', dataType: 'string' }] },
  } })
  expect(schemaResponse.ok()).toBe(true)
  const schema = await schemaResponse.json() as { definition: { id: string }; etag: string }
  const published = await page.request.post(`/api/metadata/definitions/${schema.definition.id}/publish`, { headers: { ...headers, 'If-Match': `"${schema.etag}"` } })
  expect(published.ok()).toBe(true)
  await page.goto('/#models'); await connect(page)
  await page.getByRole('combobox', { name: '当前分类', exact: true }).selectOption({ label: categoryName })
  for (const [kind, name] of [['enum', '设备状态'], ['function', '设备诊断'], ['action', '设备校准'], ['link', '关联设备']]) {
    await page.getByRole('button', { name: '新建定义', exact: true }).click()
    const dialog = page.getByRole('region', { name: '模型编辑', exact: true })
    await dialog.getByRole('combobox', { name: '类型', exact: true }).selectOption(kind)
    await dialog.getByLabel('业务名称', { exact: true }).fill(name)
    await dialog.getByRole('combobox', { name: '分组', exact: true }).selectOption({ label: '定义集合' })
    if (kind === 'enum') {
      await dialog.getByLabel('枚举值1编码').fill('RUNNING')
      await dialog.getByLabel('枚举值1标签').fill('运行中')
    }
    if (kind === 'function') await dialog.getByLabel('产物引用', { exact: true }).fill('registry.example.com/equipment/diagnosis')
    if (kind === 'action') await dialog.getByRole('combobox', { name: '实现函数', exact: true }).selectOption({ label: '设备诊断' })
    if (kind === 'link') {
      await dialog.getByRole('combobox', { name: '来源模型', exact: true }).selectOption(schema.definition.id)
      await dialog.getByRole('combobox', { name: '目标模型', exact: true }).selectOption(schema.definition.id)
      await dialog.getByLabel('正向业务名称', { exact: true }).fill('关联设备')
      await dialog.getByLabel('反向业务名称', { exact: true }).fill('被关联设备')
    }
    await dialog.getByRole('button', { name: '保存草稿', exact: true }).click()
    await expect(dialog.getByRole('button', { name: '发布定义', exact: true })).toBeEnabled()
    await dialog.getByRole('button', { name: '预览计划', exact: true }).click()
    await expect(dialog.getByRole('heading', { name: '存储计划预览', exact: true })).toBeVisible()
    await dialog.getByRole('button', { name: '发布定义', exact: true }).click()
    await expect(dialog.getByText('发布成功，当前定义已生效。')).toBeVisible()
    await dialog.getByRole('button', { name: '返回模型列表', exact: true }).click()
  }
})

test('真实服务：用户、角色、策略生效、行列权限和会话撤销', async ({ page, browser }) => {
  const suffix = Date.now().toString()
  const roleName = `产线观察员${suffix}`
  const username = `reader${suffix}`
  const displayName = `验收读者${suffix}`
  const password = randomUUID()
  const newPassword = randomUUID()
  await page.goto('/'); await connect(page)
  const session = await (await page.request.get('/api/metadata/auth/session')).json() as { csrfToken: string }
  const headers = { Origin: 'http://127.0.0.1:5173', 'X-CSRF-Token': session.csrfToken }
  const post = async (path: string, data: unknown, extra = {}) => {
    const response = await page.request.post(path, { headers: { ...headers, ...extra }, data })
    expect(response.ok(), `初始化请求失败: ${path} (${response.status()})`).toBe(true)
    return response.json()
  }
  const categoryName = `权限验收${suffix}`
  const category = await post('/api/metadata/categories', { name: categoryName })
  const group = await post('/api/metadata/groups', { name: '设备', categoryId: category.id })
  const schema = await post('/api/metadata/definitions', {
    categoryId: category.id, groupId: group.id, kind: 'schema', name: '权限设备',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ name: '设备名称', dataType: 'string' }, { name: '所属部门', dataType: 'string' }, { name: '采购价', dataType: 'decimal', precision: 18, scale: 2 }] },
  })
  await post(`/api/metadata/definitions/${schema.definition.id}/publish`, undefined, { 'If-Match': `"${schema.etag}"` })
  const properties = schema.definition.schema.properties as { id: string; name: string }[]
  const field = (name: string) => properties.find(item => item.name === name)!.id
  for (const [name, department] of [['授权设备', 'A'], ['其他部门设备', 'B']]) {
    await post(`/api/data/schemas/${schema.definition.id}/instances`, { values: { [field('设备名称')]: name, [field('所属部门')]: department, [field('采购价')]: '1234.56' }, idempotencyKey: randomUUID() })
  }
  await page.reload()
  await expect(page.getByRole('link', { name: '用户与权限', exact: true })).toBeVisible()
  await page.getByRole('link', { name: '用户与权限', exact: true }).click()
  await page.getByRole('tab', { name: '角色', exact: true }).click()
  await page.getByRole('button', { name: '创建角色', exact: true }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('角色名称', { exact: true }).fill(roleName)
  await dialog.getByLabel('角色说明', { exact: true }).fill('仅查看A部门设备名称')
  await dialog.getByRole('button', { name: '创建角色', exact: true }).click()
  await expect(dialog).toBeHidden()
  await page.getByRole('tab', { name: '用户', exact: true }).click()
  await page.getByRole('button', { name: '创建用户', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('用户名', { exact: true }).fill(username)
  await dialog.getByLabel('显示名称', { exact: true }).fill(displayName)
  await dialog.getByLabel('初始密码', { exact: true }).fill(password)
  await dialog.getByRole('checkbox', { name: roleName, exact: true }).check()
  await dialog.getByRole('button', { name: '创建用户', exact: true }).click()
  await expect(dialog).toBeHidden()
  await page.getByRole('tab', { name: '权限策略', exact: true }).click()
  await page.getByRole('button', { name: '添加规则', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('授权角色', { exact: true }).selectOption({ label: roleName })
  await dialog.getByLabel('业务操作', { exact: true }).selectOption('instance.query')
  await dialog.getByLabel('资源范围', { exact: true }).selectOption(schema.definition.id)
  await dialog.getByRole('group', { name: '可读字段', exact: true }).getByRole('checkbox', { name: '设备名称', exact: true }).check()
  await dialog.getByRole('button', { name: '添加行条件', exact: true }).first().click()
  await dialog.getByLabel('行条件字段', { exact: true }).selectOption(field('所属部门'))
  await dialog.getByLabel('行条件比较值', { exact: true }).fill('A')
  await dialog.getByRole('button', { name: '保存规则到编辑区', exact: true }).click()
  for (const operation of ['organization.read', 'definition.read']) {
    await page.getByRole('button', { name: '添加规则', exact: true }).click()
    dialog = page.getByRole('dialog')
    await dialog.getByLabel('授权角色', { exact: true }).selectOption({ label: roleName })
    await dialog.getByLabel('业务操作', { exact: true }).selectOption(operation)
    await dialog.getByLabel('资源范围', { exact: true }).selectOption('*')
    await dialog.getByRole('button', { name: '保存规则到编辑区', exact: true }).click()
  }
  await page.getByRole('button', { name: '保存策略草稿', exact: true }).click()
  await expect(page.getByRole('button', { name: '使策略生效', exact: true })).toBeEnabled()
  await page.getByLabel('模拟用户', { exact: true }).selectOption({ label: `${displayName}（${username}）` })
  await page.getByLabel('模拟资源', { exact: true }).selectOption(schema.definition.id)
  await page.getByRole('button', { name: '模拟授权', exact: true }).click()
  await expect(page.getByText('拒绝此操作', { exact: true })).toBeVisible()
  await page.getByLabel('使用策略', { exact: true }).selectOption('draft')
  await page.getByRole('button', { name: '模拟授权', exact: true }).click()
  await expect(page.getByText('允许此操作', { exact: true })).toBeVisible()
  const readerContext = await browser.newContext({ baseURL: 'http://127.0.0.1:5173' })
  try {
    const readerLogin = await readerContext.request.post('/api/metadata/auth/login', { headers: { Origin: headers.Origin }, data: { tenant: account().Tenant, username, password } })
    expect(readerLogin.ok()).toBe(true)
    const readerSession = await readerLogin.json() as { csrfToken: string }
    const readerHeaders = { Origin: headers.Origin, 'X-CSRF-Token': readerSession.csrfToken }
    const query = { from: { schemaId: schema.definition.id }, select: [field('设备名称')], page: { limit: 10 } }
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(403)
    await page.getByRole('button', { name: '使策略生效', exact: true }).click()
    await expect(page.getByText('权限策略已生效。', { exact: true }).first()).toBeVisible()
    const allowed = await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })
    expect(allowed.status()).toBe(200)
    const result = await allowed.json()
    expect(result.data).toHaveLength(1)
    expect(result.data[0].values).toEqual({ [field('设备名称')]: '授权设备' })
    expect(result.fields.map((item: { id: string }) => item.id)).toEqual([field('设备名称')])
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: { ...query, select: [field('采购价')] } })).status()).toBe(403)
    expect((await readerContext.request.get('/api/metadata/admin/users')).status()).toBe(403)
    await page.getByRole('tab', { name: '用户', exact: true }).click()
    await page.getByRole('row').filter({ hasText: displayName }).getByRole('button', { name: '编辑用户', exact: true }).click()
    dialog = page.getByRole('dialog')
    await dialog.getByRole('checkbox', { name: '启用账户', exact: true }).uncheck()
    await dialog.getByRole('button', { name: '保存用户', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect((await readerContext.request.get('/api/metadata/auth/session')).status()).toBe(401)
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(401)
    await page.getByRole('row').filter({ hasText: displayName }).getByRole('button', { name: '编辑用户', exact: true }).click()
    dialog = page.getByRole('dialog')
    await dialog.getByRole('checkbox', { name: '启用账户', exact: true }).check()
    await dialog.getByLabel('重置密码（留空则保留原密码）', { exact: true }).fill(newPassword)
    await dialog.getByRole('button', { name: '保存用户', exact: true }).click()
    await expect(dialog).toBeHidden()
    const readerPage = await readerContext.newPage()
    await readerPage.goto('/')
    await readerPage.getByLabel('租户', { exact: true }).fill(account().Tenant)
    await readerPage.getByLabel('用户名', { exact: true }).fill(username)
    await readerPage.getByLabel('密码', { exact: true }).fill(newPassword)
    await readerPage.getByRole('button', { name: '登录', exact: true }).click()
    await expect(readerPage.getByRole('navigation', { name: '工作台导航' })).toBeVisible()
    await expect(readerPage.getByRole('link', { name: '用户与权限', exact: true })).toHaveCount(0)
    await readerPage.getByRole('combobox', { name: '当前分类', exact: true }).selectOption({ label: categoryName })
    await readerPage.getByRole('link', { name: '数据探索', exact: true }).click()
    await readerPage.getByLabel('查询模型', { exact: true }).selectOption(schema.definition.id)
    await readerPage.getByRole('button', { name: '清空', exact: true }).click()
    await readerPage.getByRole('checkbox', { name: '设备名称', exact: true }).check()
    await readerPage.getByRole('button', { name: '运行查询', exact: true }).click()
    await expect(readerPage.getByRole('region', { name: '查询结果', exact: true }).getByText('授权设备', { exact: true })).toBeVisible()
    const replayCookie = (await readerContext.cookies()).find(cookie => cookie.name === 'ontology_session')!
    await readerPage.getByRole('button', { name: '退出工作空间', exact: true }).click()
    await expect(readerPage.getByRole('heading', { name: '登录你的工作空间' })).toBeVisible()
    expect((await readerContext.request.get('/api/metadata/auth/session', { headers: { Cookie: `ontology_session=${replayCookie.value}` } })).status()).toBe(401)
  } finally { await readerContext.close() }
  await page.getByRole('tab', { name: '审计记录', exact: true }).click()
  await page.getByRole('button', { name: '重新读取', exact: true }).click()
  await expect(page.getByRole('cell', { name: '策略生效', exact: true }).first()).toBeVisible()
  mkdirSync(resolve(repo, '.cache/frontend-verification'), { recursive: true })
  await page.screenshot({ path: resolve(repo, '.cache/frontend-verification/user-permissions.png'), fullPage: true })
})

test('真实服务：用户组继承角色、启停与移除成员撤销授权', async ({ page, browser }) => {
  const suffix = Date.now().toString()
  const roleName = `组设备观察员${suffix}`
  const groupName = `设备巡检组${suffix}`
  const renamedGroup = `设备巡检组改名${suffix}`
  const username = `groupreader${suffix}`
  const displayName = `组授权读者${suffix}`
  const password = randomUUID()
  await page.goto('/'); await connect(page)
  const session = await (await page.request.get('/api/metadata/auth/session')).json() as { csrfToken: string }
  const headers = { Origin: 'http://127.0.0.1:5173', 'X-CSRF-Token': session.csrfToken }
  const post = async (path: string, data: unknown, extra = {}) => {
    const response = await page.request.post(path, { headers: { ...headers, ...extra }, data })
    expect(response.ok(), `初始化请求失败: ${path} (${response.status()})`).toBe(true)
    return response.json()
  }
  const category = await post('/api/metadata/categories', { name: `组授权验收${suffix}` })
  const definitionGroup = await post('/api/metadata/groups', { name: '巡检模型', categoryId: category.id })
  const schema = await post('/api/metadata/definitions', {
    categoryId: category.id, groupId: definitionGroup.id, kind: 'schema', name: '巡检设备',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ name: '设备名称', dataType: 'string' }] },
  })
  await post(`/api/metadata/definitions/${schema.definition.id}/publish`, undefined, { 'If-Match': `"${schema.etag}"` })
  const fieldId = schema.definition.schema.properties[0].id as string
  await post(`/api/data/schemas/${schema.definition.id}/instances`, { values: { [fieldId]: '组内可见设备' }, idempotencyKey: randomUUID() })
  const role = await post('/api/metadata/admin/roles', { name: roleName, description: '经用户组分配业务角色' })
  const user = await post('/api/metadata/admin/users', { username, displayName, password, enabled: true, administrator: false, roleIds: [] })
  const policy = await (await page.request.get('/api/metadata/admin/policy')).json()
  expect(policy.draft, '验收不会覆盖已有未生效草稿').toBeUndefined()
  const saved = await page.request.put('/api/metadata/admin/policy', { headers: { ...headers, 'If-Match': `"${policy.etag}"` }, data: {
    rules: [...policy.current.rules, { id: randomUUID(), roleId: role.id, effect: 'allow', operation: 'instance.query', resourceId: schema.definition.id, readFields: [fieldId], writeFields: [] }],
  } })
  expect(saved.ok()).toBe(true)
  const draft = await saved.json()
  await post('/api/metadata/admin/policy/publish', undefined, { 'If-Match': `"${draft.etag}"` })
  const readerContext = await browser.newContext({ baseURL: 'http://127.0.0.1:5173' })
  try {
    const loginReader = async () => {
      const response = await readerContext.request.post('/api/metadata/auth/login', { headers: { Origin: headers.Origin }, data: { tenant: account().Tenant, username, password } })
      expect(response.ok()).toBe(true)
      const result = await response.json() as { csrfToken: string; user: { roleIds: string[]; administrator: boolean } }
      expect(result.user.roleIds).toEqual([])
      expect(result.user.administrator).toBe(false)
      return { Origin: headers.Origin, 'X-CSRF-Token': result.csrfToken }
    }
    const query = { from: { schemaId: schema.definition.id }, select: [fieldId], page: { limit: 10 } }
    let readerHeaders = await loginReader()
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(403)
    expect((await readerContext.request.get('/api/metadata/admin/user-groups')).status()).toBe(403)
    await page.reload()
    await page.getByRole('link', { name: '用户与权限', exact: true }).click()
    await page.getByRole('tab', { name: '用户组', exact: true }).click()
    await page.getByRole('button', { name: '创建用户组', exact: true }).click()
    let dialog = page.getByRole('dialog')
    await dialog.getByLabel('用户组名称', { exact: true }).fill(groupName)
    await dialog.getByLabel('用户组说明', { exact: true }).fill('巡检业务成员与角色')
    await dialog.getByRole('checkbox', { name: roleName, exact: true }).check()
    await dialog.getByRole('checkbox', { name: `${displayName}（${username}）`, exact: true }).check()
    await dialog.getByRole('button', { name: '创建用户组', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect((await readerContext.request.get('/api/metadata/auth/session')).status()).toBe(401)
    readerHeaders = await loginReader()
    const allowed = await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })
    expect(allowed.status()).toBe(200)
    expect((await allowed.json()).data[0].values).toEqual({ [fieldId]: '组内可见设备' })
    const editor = async (name: string) => {
      await page.getByRole('row').filter({ hasText: name }).getByRole('button', { name: '编辑用户组', exact: true }).click()
      return page.getByRole('dialog')
    }
    dialog = await editor(groupName)
    await dialog.getByLabel('用户组名称', { exact: true }).fill(renamedGroup)
    await dialog.getByRole('button', { name: '保存用户组', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect((await readerContext.request.get('/api/metadata/auth/session')).status()).toBe(200)
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(200)
    await page.getByRole('tab', { name: '用户', exact: true }).click()
    const readerRow = page.getByRole('row').filter({ hasText: displayName })
    await expect(readerRow.getByText(renamedGroup, { exact: true })).toBeVisible()
    await page.getByRole('tab', { name: '用户组', exact: true }).click()
    dialog = await editor(renamedGroup)
    await dialog.getByRole('checkbox', { name: '启用用户组', exact: true }).uncheck()
    await dialog.getByRole('button', { name: '保存用户组', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect((await readerContext.request.get('/api/metadata/auth/session')).status()).toBe(401)
    readerHeaders = await loginReader()
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(403)
    dialog = await editor(renamedGroup)
    await dialog.getByRole('checkbox', { name: '启用用户组', exact: true }).check()
    await dialog.getByRole('button', { name: '保存用户组', exact: true }).click()
    await expect(dialog).toBeHidden()
    readerHeaders = await loginReader()
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(200)
    dialog = await editor(renamedGroup)
    await dialog.getByRole('checkbox', { name: `${displayName}（${username}）`, exact: true }).uncheck()
    await dialog.getByRole('button', { name: '保存用户组', exact: true }).click()
    await expect(dialog).toBeHidden()
    expect((await readerContext.request.get('/api/metadata/auth/session')).status()).toBe(401)
    readerHeaders = await loginReader()
    expect((await readerContext.request.post('/api/data/query', { headers: readerHeaders, data: query })).status()).toBe(403)
    const currentUser = await (await page.request.get(`/api/metadata/admin/users/${user.id}`)).json()
    expect(currentUser.roleIds).toEqual([])
    const directoryGroup = await page.request.get(`/api/metadata/groups?categoryId=${category.id}`)
    expect((await directoryGroup.json()).data.map((entry: { name: string }) => entry.name)).toEqual(['巡检模型'])
    await page.getByRole('tab', { name: '审计记录', exact: true }).click()
    await page.getByRole('button', { name: '重新读取', exact: true }).click()
    await expect(page.getByRole('cell', { name: '更新用户组', exact: true }).first()).toBeVisible()
    await page.getByRole('tab', { name: '用户组', exact: true }).click()
    mkdirSync(resolve(repo, '.cache/frontend-verification'), { recursive: true })
    await page.screenshot({ path: resolve(repo, '.cache/frontend-verification/user-groups.png'), fullPage: true })
  } finally { await readerContext.close() }
})
