import { expect, test, type Page } from '@playwright/test'
import type { DefinitionRecord, PermissionPolicy, PolicyRecord } from '../src/lib/types'

// All API calls use an isolated fixture. No existing account, policy or object
// is read or changed by these browser checks.
async function fixture(page: Page) {
  const tenantId = 'permission-context-fixture'
  const identity = { id: '00000000-0000-4000-8000-000000000001', tenantId, username: 'administrator', displayName: '权限管理员', enabled: true, administrator: true, roleIds: ['role'], etag: 'user-etag' }
  const definitions: DefinitionRecord[] = [
    { status: 'active', etag: 'records-current', definition: { id: 'records', tenantId, categoryId: 'category', groupId: 'group', kind: 'schema', name: '业务记录', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'owner', name: '负责人', dataType: 'uuid' }, { id: 'department', name: '部门', dataType: 'string' }] } } },
    { status: 'active', etag: 'staff-current', definition: { id: 'staff', tenantId, categoryId: 'category', groupId: 'group', kind: 'schema', name: '员工资料', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'staff-user', name: '对应用户', dataType: 'uuid' }, { id: 'staff-dept', name: '所属部门', dataType: 'string' }], indexes: [{ id: 'staff-user-key', name: '用户唯一', unique: true, fieldIds: ['staff-user'] }] } } },
  ]
  let policy: PolicyRecord = { current: { rules: [] }, etag: 'policy-current', state: 'state-current' }
  const saves: PermissionPolicy[] = [], unexpected: string[] = [], errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ json, status })
    if (path === '/api/metadata/auth/session') return reply({ user: identity, csrfToken: 'fixture-csrf', expiresAt: Math.floor(Date.now() / 1000) + 3600 })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python', 'javascript', 'go'] })
    if (path === '/api/data/sources' || path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'category', tenantId, name: '业务管理' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId: 'category', name: '基础对象' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : definitions, hasMore: false })
    if (path === '/api/metadata/admin/users') return reply({ data: [identity] })
    if (path === '/api/metadata/admin/user-groups' || path === '/api/metadata/admin/audit') return reply({ data: [] })
    if (path === '/api/metadata/admin/roles') return reply({ data: [{ id: 'role', name: '业务用户', description: '', etag: 'role-etag' }] })
    if (path === '/api/metadata/admin/policy' && method === 'GET') return reply(policy)
    if (path === '/api/metadata/admin/policy' && method === 'PUT') {
      const draft = request.postDataJSON() as PermissionPolicy
      saves.push(structuredClone(draft))
      policy = { ...policy, draft, etag: `policy-draft-${saves.length}` }
      return reply(policy)
    }
    if (path === '/api/metadata/admin/simulate' && method === 'POST') return reply({ decision: { allow: true, readFields: ['owner', 'department'], writeFields: [], state: 'current', rowFilter: { op: 'eq', field: 'department', value: 'PRIVATE_RESOLVED_DEPARTMENT' } }, reason: '已匹配授权，条件仍需在实际访问时校验', contextProtected: true })
    if (path === '/api/metadata/admin/policy/publish' && method === 'POST') {
      policy = { current: policy.draft!, etag: 'policy-published', state: 'state-published' }
      return reply(policy)
    }
    unexpected.push(`${method} ${path}`)
    return reply({ error: { code: 'unexpected_request', message: '隔离用例未定义此接口' } }, 404)
  })
  return { saves, errors, unexpected }
}

for (const width of [1440, 390]) {
  test(`动态权限编辑、草稿模拟与生效：${width}px`, async ({ page }) => {
    const state = await fixture(page)
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/#admin')
    await expect(page.getByRole('heading', { name: '用户与权限', exact: true })).toBeVisible()
    await page.getByRole('tab', { name: '权限策略', exact: true }).click()
    await page.getByRole('button', { name: '添加规则', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: '添加权限规则', exact: true })
    await dialog.getByLabel('资源范围').selectOption('records')
    await dialog.getByRole('button', { name: '添加行条件', exact: true }).click()
    await dialog.getByLabel('行条件取值来源').selectOption('user')
    await expect(dialog.getByLabel('当前用户信息')).toHaveValue('id')
    await dialog.getByRole('button', { name: '添加行条件', exact: true }).click()
    await dialog.getByLabel('行条件字段').nth(1).selectOption('department')
    await dialog.getByLabel('行条件取值来源').nth(1).selectOption('object')
    await dialog.getByLabel('取值对象模型').selectOption('staff')
    await dialog.getByLabel('取值对象属性').selectOption('staff-dept')
    await dialog.getByLabel('对象取值方式').selectOption('id')
    await dialog.getByLabel('用户匹配属性').selectOption('staff-user')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    await dialog.getByRole('button', { name: '保存规则到编辑区', exact: true }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByText(/负责人 等于 当前用户 · 用户 ID/)).toBeVisible()
    await expect(page.getByText(/部门 等于 员工资料 · 所属部门/)).toBeVisible()
    await page.getByRole('button', { name: '保存策略草稿', exact: true }).click()
    await expect.poll(() => state.saves.length).toBe(1)
    expect(state.saves[0].rules[0].rowFilter).toEqual({ op: 'and', args: [
      { op: 'eq', field: 'owner', valueRef: { source: 'user', attribute: 'id' } },
      { op: 'eq', field: 'department', valueRef: { source: 'object', schemaId: 'staff', fieldId: 'staff-dept', matchFieldId: 'staff-user', matchUserAttribute: 'id' } },
    ] })
    await page.getByLabel('模拟用户').selectOption('00000000-0000-4000-8000-000000000001')
    await page.getByLabel('模拟资源').selectOption('records')
    await page.getByLabel('使用策略').selectOption('draft')
    await page.getByRole('button', { name: '模拟授权', exact: true }).click()
    await expect(page.getByText('允许此操作', { exact: true })).toBeVisible()
    await expect(page.getByText('含动态上下文，实际取值不回显', { exact: true })).toHaveCount(2)
    await expect(page.getByText(/PRIVATE_RESOLVED/)).toHaveCount(0)
    await page.getByRole('button', { name: '使策略生效', exact: true }).click()
    await expect(page.getByText('权限策略已生效。', { exact: true })).toBeVisible()
    await expect(page.getByText('允许此操作', { exact: true })).toHaveCount(0)
    expect(state.errors).toEqual([])
    expect(state.unexpected).toEqual([])
  })
}
