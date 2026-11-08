import { test, expect, type Page } from '@playwright/test'
import type { DataSource, SourceOverview, SourceQueryResult, SourceTable, SourceTableInfo } from '../src/lib/types'

// All catalog, metric and row responses are isolated HTTP fixtures. This suite
// verifies browser interactions without connecting to a developer database.
async function fixture(page: Page, shared = false) {
  const tenantId = 'source-layout-fixture'
  const source: DataSource = { id: 'layout-source', tenantId, name: '订单业务库', driver: 'mysql', host: 'fixture.invalid', port: 3306, database: 'business', username: 'fixture_reader', tlsMode: 'require', tenantIsolation: shared ? 'column' : 'dedicated', enabled: true, passwordSet: true, etag: 'source-etag' }
  const tables: SourceTableInfo[] = Array.from({ length: 45 }, (_, index) => ({ namespace: 'business', name: `orders_${String(index + 1).padStart(2, '0')}`, type: 'table', columnCount: 3, primaryKeyCount: 1 }))
  const overview: SourceOverview = { sourceId: source.id, etag: source.etag, database: 'business', serverVersion: '26.7.0', charset: 'utf8mb4', checkedAt: '2026-11-08T03:00:00Z', metrics: {
    connections: { available: true, value: '12', unit: 'connections', scope: 'instance', basis: '当前数据库实例连接数' },
    storageBytes: { available: true, value: '1048576', unit: 'bytes', scope: 'database', basis: '当前数据库存储占用' },
    qps: { available: false, unit: 'queries/s', scope: 'instance', basis: '两个采样点间查询增量', reason: '数据库账号缺少统计权限' },
  } }
  const result: SourceQueryResult = { columns: ['amount', 'memo'], rows: [['9007199254740993.0001', '中文订单'], [null, '']], limit: 100, hasMore: true, elapsedMs: 3 }
  const state = { source, tables, queryMode: 'success' as 'success' | 'stale' | 'forbidden', refreshed: false, overviewReads: 0,
    inspections: [] as string[], queries: [] as { sql: string; etag: string; csrf: string }[], unhandled: [] as string[] }
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'admin', tenantId, username: 'administrator', displayName: '数据库管理员', administrator: true, enabled: true, roleIds: [], etag: 'user-etag' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, operations: ['query'] })
    if (['/api/metadata/categories', '/api/metadata/groups', '/api/metadata/definitions', '/api/data/source-versions'].includes(path)) return reply({ data: [], hasMore: false })
    if (path === '/api/data/sources') return reply({ data: [source], hasMore: false })
    if (path === `/api/data/sources/${source.id}`) return reply(source)
    if (path === `/api/data/sources/${source.id}/overview`) { state.overviewReads++; return reply({ ...overview, etag: source.etag }) }
    if (path === `/api/data/sources/${source.id}/tables`) return reply({ data: tables, hasMore: false })
    const inspected = path.match(/^\/api\/data\/sources\/layout-source\/tables\/([^/]+)$/)
    if (inspected) {
      const name = decodeURIComponent(inspected[1]); state.inspections.push(name)
      const table: SourceTable = { namespace: 'business', name, signature: state.refreshed ? 'refreshed-signature' : 'signature', columns: [
        { name: 'id', databaseType: 'bigint', dataType: 'int64', nullable: false, primaryKey: true, generated: false, position: 1, comment: '订单主键' },
        { name: 'amount', databaseType: 'decimal(24,4)', dataType: 'decimal', nullable: true, primaryKey: false, generated: false, position: 2, defaultValue: '0.0000', comment: state.refreshed ? '刷新后金额注释' : '订单金额' },
        { name: 'memo', databaseType: 'varchar(255)', dataType: 'string', nullable: true, primaryKey: false, generated: false, position: 3, defaultValue: null, comment: '业务备注' },
      ], indexes: [{ name: 'PRIMARY', unique: true, primary: true, columns: ['id'] }, { name: 'idx_amount', unique: false, primary: false, columns: ['amount'] }] }
      return reply(table)
    }
    if (path === `/api/data/sources/${source.id}/query` && method === 'POST') {
      state.queries.push({ sql: request.postDataJSON().sql, etag: request.headers()['if-match'], csrf: request.headers()['x-csrf-token'] })
      if (state.queryMode === 'stale') return reply({ error: { code: 'precondition_failed', message: '数据源配置已更新' } }, 412)
      if (state.queryMode === 'forbidden') return reply({ error: { code: 'administrator_required', message: '数据库管理权限已撤销' } }, 403)
      return reply(result)
    }
    state.unhandled.push(`${method} ${path}`)
    return reply({ error: { code: 'unhandled_fixture_request', message: '未定义的数据库管理隔离接口' } }, 404)
  })
  await page.goto('/#sources')
  await page.getByRole('row', { name: /订单业务库/ }).getByRole('button', { name: '管理', exact: true }).click()
  await expect(page.getByRole('tabpanel', { name: '字段详情', exact: true }).getByRole('cell', { name: '订单金额', exact: true })).toBeVisible()
  return state
}

test('数据库概览与双栏目录、分页搜索和字段详情，刷新与键盘 Tabs 在宽窄屏可用', async ({ page }) => {
  const errors: string[] = []; page.on('pageerror', (error) => errors.push(error.message))
  const state = await fixture(page)
  await expect(page.getByRole('heading', { name: '数据库管理', level: 1 })).toBeVisible()
  const overview = page.getByRole('region', { name: '数据库基础信息', exact: true })
  await expect(overview).toContainText('26.7.0')
  await expect(overview).toContainText('utf8mb4')
  await expect(overview).toContainText('12')
  await expect(overview).toContainText('数据库账号缺少统计权限')
  await expect(overview.getByText('暂不可用', { exact: true })).toHaveCount(1)
  expect(state.queries).toEqual([])
  expect(state.inspections).toEqual(['orders_01'])
  const catalog = page.getByRole('region', { name: '数据库表目录', exact: true })
  await expect(catalog.getByRole('columnheader', { name: '字段数', exact: true })).toBeVisible()
  await expect(catalog.getByRole('columnheader', { name: '主键数', exact: true })).toBeVisible()
  await expect(catalog.getByRole('button', { name: /^business\.orders_/ })).toHaveCount(20)
  await catalog.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(catalog.getByRole('button', { name: 'business.orders_21', exact: true })).toBeVisible()
  await catalog.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(catalog.getByRole('button', { name: /^business\.orders_/ })).toHaveCount(5)
  await page.getByLabel('搜索数据库表', { exact: true }).fill('orders_02')
  await expect(catalog.getByRole('button', { name: /^business\.orders_/ })).toHaveCount(1)
  await expect(catalog.getByRole('button', { name: '上一页', exact: true })).toBeDisabled()
  await catalog.getByRole('button', { name: 'business.orders_02', exact: true }).click()
  const fields = page.getByRole('tabpanel', { name: '字段详情', exact: true })
  const fieldTable = fields.getByRole('table').first()
  await expect(fieldTable.getByRole('row', { name: /amount/ })).toContainText('0.0000')
  await expect(fieldTable.getByRole('row', { name: /amount/ })).toContainText('idx_amount')
  await page.getByLabel('搜索字段', { exact: true }).fill('订单金额')
  await expect(fieldTable.getByRole('cell', { name: 'amount', exact: true })).toBeVisible()
  await expect(fieldTable.getByRole('cell', { name: 'memo', exact: true })).toHaveCount(0)
  await page.getByLabel('搜索字段', { exact: true }).fill('')
  state.refreshed = true
  await page.getByRole('button', { name: '刷新结构', exact: true }).click()
  await expect(fields.getByRole('cell', { name: '刷新后金额注释', exact: true })).toBeVisible()
  expect(state.inspections.at(-1)).toBe('orders_02')
  expect(state.overviewReads).toBe(2)
  const tabs = page.getByRole('tablist', { name: '数据库管理视图', exact: true })
  await tabs.getByRole('tab', { name: '字段详情', exact: true }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(tabs.getByRole('tab', { name: '表数据预览', exact: true })).toBeFocused()
  await expect(tabs.getByRole('tab', { name: '表数据预览', exact: true })).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('End')
  await expect(tabs.getByRole('tab', { name: 'SQL控制台', exact: true })).toBeFocused()
  await page.keyboard.press('Home')
  await expect(tabs.getByRole('tab', { name: '字段详情', exact: true })).toHaveAttribute('aria-selected', 'true')
  for (const width of [1920, 1440, 760, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    await expect(page.getByRole('button', { name: '返回数据源列表', exact: true })).toBeVisible()
    for (const name of ['字段详情', '表数据预览', 'SQL控制台']) {
      await tabs.getByRole('tab', { name, exact: true }).click()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    }
    if (width === 1440 || width === 390) {
      await tabs.getByRole('tab', { name: '字段详情', exact: true }).click()
      await page.screenshot({ path: `../../.cache/source-manager-${width === 1440 ? 'wide' : 'narrow'}.png`, fullPage: true })
    }
  }
  expect(state.queries).toEqual([])
  await page.getByRole('button', { name: '返回数据源列表', exact: true }).click()
  await expect(page.getByRole('region', { name: '已配置的数据源', exact: true })).toBeVisible()
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

test('预览与 SQL 独立执行并保留精度，配置过期需刷新，撤权清除管理内容', async ({ page }) => {
  const state = await fixture(page)
  await page.getByRole('tab', { name: '表数据预览', exact: true }).click()
  expect(state.queries).toEqual([])
  await page.getByRole('button', { name: '预览表数据', exact: true }).click()
  const preview = page.getByRole('tabpanel', { name: '表数据预览', exact: true })
  await expect(preview.getByRole('cell', { name: '9007199254740993.0001', exact: true })).toBeVisible()
  await expect(preview.getByRole('cell', { name: 'NULL', exact: true })).toBeVisible()
  await expect(preview.getByRole('cell', { name: '空字符串', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: 'SQL控制台', exact: true }).click()
  const sql = page.getByRole('tabpanel', { name: 'SQL控制台', exact: true })
  await expect(sql.getByRole('region', { name: 'SQL 查询结果', exact: true })).toHaveCount(0)
  await page.getByLabel('SQL 语句', { exact: true }).fill('SELECT amount, memo FROM orders_01 LIMIT 2;')
  await page.getByRole('button', { name: '执行 SQL', exact: true }).click()
  await expect(sql.getByRole('cell', { name: '9007199254740993.0001', exact: true })).toBeVisible()
  expect(state.queries).toEqual([
    { sql: 'SELECT * FROM `business`.`orders_01` LIMIT 100;', etag: '"source-etag"', csrf: 'fixture-csrf' },
    { sql: 'SELECT amount, memo FROM orders_01 LIMIT 2;', etag: '"source-etag"', csrf: 'fixture-csrf' },
  ])
  await page.getByLabel('SQL 语句', { exact: true }).fill('SELECT amount FROM orders_01 LIMIT 3;')
  await expect(sql.getByRole('region', { name: 'SQL 查询结果', exact: true })).toHaveCount(0)
  state.queryMode = 'stale'
  await page.getByRole('button', { name: '执行 SQL', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('刷新结构')
  await expect(page.getByRole('button', { name: '执行 SQL', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'business.orders_01', exact: true })).toBeVisible()
  state.source.etag = 'source-updated'; state.queryMode = 'success'
  await page.getByRole('button', { name: '刷新结构', exact: true }).click()
  await expect.poll(() => state.overviewReads).toBe(2)
  await page.getByRole('tab', { name: 'SQL控制台', exact: true }).click()
  await expect(page.getByRole('button', { name: '执行 SQL', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: '执行 SQL', exact: true }).click()
  await expect(sql.getByRole('cell', { name: '9007199254740993.0001', exact: true })).toBeVisible()
  expect(state.queries.at(-1)?.etag).toBe('"source-updated"')
  state.queryMode = 'forbidden'
  await page.getByRole('button', { name: '执行 SQL', exact: true }).click()
  await expect(page.getByRole('heading', { name: '数据源配置', exact: true })).toBeVisible()
  await expect(page.getByRole('alert')).toContainText('数据库管理权限已撤销')
  await expect(page.getByText('9007199254740993.0001', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('region', { name: '数据库表目录', exact: true })).toHaveCount(0)
  expect(state.unhandled).toEqual([])
})

test('共享数据库可浏览字段与索引，预览及 SQL 均不读取业务行', async ({ page }) => {
  const state = await fixture(page, true)
  await expect(page.getByRole('tabpanel', { name: '字段详情', exact: true }).getByRole('cell', { name: '订单金额', exact: true })).toBeVisible()
  await page.getByRole('tab', { name: '表数据预览', exact: true }).click()
  await expect(page.getByRole('button', { name: '预览表数据', exact: true })).toBeDisabled()
  await page.getByRole('tab', { name: 'SQL控制台', exact: true }).click()
  await expect(page.getByRole('button', { name: '执行 SQL', exact: true })).toBeDisabled()
  await expect(page.getByLabel('SQL 语句', { exact: true })).toBeDisabled()
  expect(state.queries).toEqual([])
  expect(state.unhandled).toEqual([])
})
