import { test, expect, type Page } from '@playwright/test'
import { createHmac } from 'node:crypto'
import { existsSync, readFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const repo = fileURLToPath(new URL('../../../', import.meta.url))
const credentialsFile = resolve(repo, '.cache/dev/secrets.json')
test.skip(!existsSync(credentialsFile), '需要先启动本项目的本地 MySQL/OPA 和两个后端服务')

function credential() {
  const secrets = JSON.parse(readFileSync(credentialsFile, 'utf8').replace(/^\uFEFF/, '')) as { AuthSecret: string }
  const payload = Buffer.from(JSON.stringify({ tenantId: 'demo-tenant', subject: 'developer', expiresAt: Math.floor(Date.now() / 1000) + 3600 })).toString('base64url')
  return `${payload}.${createHmac('sha256', secrets.AuthSecret).update(payload).digest('base64url')}`
}

async function connect(page: Page) {
  await page.getByLabel('访问凭证', { exact: true }).fill(credential())
  await page.getByRole('button', { name: '进入工作空间' }).click()
  await expect(page.getByRole('navigation', { name: '工作台导航' })).toBeVisible()
}

test('真实服务：分类、可重新发现的草稿、发布、精确查询和实例维护', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', error => pageErrors.push(error.message))
  const category = `前端验收${Date.now()}`
  await page.goto('/')
  await connect(page)
  await page.getByRole('link', { name: '模型管理', exact: true }).click()
  await page.getByRole('button', { name: '管理分类', exact: true }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('分类名称', { exact: true }).fill(category)
  await dialog.getByRole('button', { name: '创建分类', exact: true }).click()
  await expect(dialog.getByText('分类已创建，接下来为它添加分组。')).toBeVisible()
  await dialog.getByLabel('新分组名称').fill('基础模型')
  await dialog.getByRole('button', { name: '创建分组', exact: true }).click()
  await expect(dialog.getByText('分组已创建，可以在其中建立业务定义。')).toBeVisible()
  await dialog.getByRole('button', { name: '完成', exact: true }).click()
  await page.getByRole('button', { name: '新建定义', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('业务名称', { exact: true }).fill('精密设备')
  await dialog.getByRole('combobox', { name: '分类', exact: true }).selectOption({ label: category })
  await dialog.getByRole('combobox', { name: '分组', exact: true }).selectOption({ label: '基础模型' })
  await dialog.getByLabel('描述', { exact: true }).fill('中文建模与精确数值查询的浏览器验收模型')
  await dialog.getByLabel('对象属性第1项名称').fill('设备名称')
  for (const [index, name, type] of [[2, '累计计数', 'int64'], [3, '测量值', 'decimal'], [4, '备注', 'string']] as const) {
    await dialog.getByRole('button', { name: '添加属性', exact: true }).click()
    await dialog.getByLabel(`对象属性第${index}项名称`).fill(name)
    await dialog.getByLabel(`对象属性第${index}项类型`).selectOption(type)
  }
  await dialog.getByLabel('对象属性第3项总位数').fill('24')
  await dialog.getByLabel('对象属性第3项小数位').fill('4')
  await dialog.locator('tbody tr').nth(3).getByLabel('允许空值').check()
  await dialog.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(dialog.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')).toBeVisible()
  await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  // Reload intentionally destroys all frontend memory and requires a new session.
  await page.reload(); await connect(page)
  await page.getByLabel('按分类筛选').selectOption({ label: category })
  await page.getByRole('button', { name: '精密设备', exact: true }).click()
  dialog = page.getByRole('dialog')
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
  await dialog.getByRole('button', { name: '关闭', exact: true }).click()
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
  await expect(page.getByRole('dialog')).toBeVisible()
  // A browser back/hash change hides the page's portal, preserving its form.
  await page.evaluate(() => { window.location.hash = 'overview' })
  await expect(page.getByRole('dialog')).toBeHidden()
  await page.evaluate(() => { window.location.hash = 'models' })
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByRole('dialog').getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('link', { name: '数据探索', exact: true }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.evaluate(() => { (document.activeElement as HTMLElement | null)?.blur(); window.scrollTo(0, 0) })
  await page.screenshot({ path: resolve(artifacts, 'mobile-query.png'), fullPage: true })
  expect(pageErrors).toEqual([])
  await page.getByRole('button', { name: '退出工作空间', exact: true }).click()
  await expect(page.getByRole('heading', { name: '连接你的工作空间', exact: true })).toBeVisible()
  const persistedSecrets = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length }))
  expect(persistedSecrets).toEqual({ local: 0, session: 0 })
})

test('真实服务：枚举、函数、动作和关系通过中文表单维护', async ({ page }) => {
  const categoryName = `定义验收${Date.now()}`
  const headers = { Authorization: `Bearer ${credential()}` }
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
  for (const [kind, name] of [['enum', '设备状态'], ['function', '设备诊断'], ['action', '设备校准'], ['link', '关联设备']]) {
    await page.getByRole('button', { name: '新建定义', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('combobox', { name: '类型', exact: true }).selectOption(kind)
    await dialog.getByLabel('业务名称', { exact: true }).fill(name)
    await dialog.getByRole('combobox', { name: '分类', exact: true }).selectOption({ label: categoryName })
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
    await dialog.getByRole('button', { name: '关闭', exact: true }).click()
  }
})
