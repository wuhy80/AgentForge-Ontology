import { createHash } from 'node:crypto';
import { expect, test, type Page } from '@playwright/test';
import type { DefinitionRecord, QueryContextModel, QuerySpec, SavedQuery, SavedQueryInput, SavedQueryTemplate } from '../src/lib/types';

// Browser verification uses only in-memory HTTP fixtures, never business data.
async function fixture(page: Page) {
  const tenantId = 'saved-query-browser';
  const model: QueryContextModel = { schemaId: 'orders', categoryId: 'business', category: '业务', name: '订单', fields: [{ id: 'state', name: '状态', dataType: 'string', filterable: true, sortable: true }, { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true }] };
  const schema: DefinitionRecord = { status: 'active', etag: 'current', definition: { id: model.schemaId, tenantId, categoryId: 'business', groupId: 'group', kind: 'schema', name: model.name, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields } } };
  const template: SavedQueryTemplate = { query: { from: { schemaId: model.schemaId }, select: ['amount'], where: { op: 'gte', field: 'amount', parameter: '最低金额' }, page: { limit: 25 } }, parameters: [{ name: '最低金额', dataType: 'decimal', precision: 24, scale: 6, nullable: false }] };
  const records = new Map<string, SavedQuery>();
  const state = { creates: [] as SavedQueryInput[], compiles: [] as unknown[], executes: [] as { id: string; body: { parameters: Record<string, unknown> } }[], queries: [] as QuerySpec[], unexpected: [] as string[], errors: [] as string[], writes: [] as { method: string; ifMatch: string | undefined }[] };
  page.on('pageerror', (error) => state.errors.push(error.message));
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    const reply = (json: unknown, status = 200) => route.fulfill({ json, status });
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '查询用户', administrator: false, enabled: true, roleIds: [], etag: 'user' }, csrfToken: 'fixture', expiresAt: Math.floor(Date.now() / 1000) + 3600 });
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] });
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', operations: ['query'], maxPageSize: 100, maxSortFields: 1, savedQueries: true, aggregates: true, maxAggregateGroups: 1000, maxAggregateGroupFields: 3, maxAggregateMeasures: 20 });
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python'] });
    if (path === '/api/data/sources' || path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false });
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'business', tenantId, name: '业务' }], hasMore: false });
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'group', tenantId, categoryId: 'business', name: '基础模型' }], hasMore: false });
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? [] : [schema], hasMore: false });
    if (path === '/api/data/query/context') return reply({ models: [model] });
    if (path === '/api/data/schemas/orders/write-capabilities') return reply({ schemaId: 'orders', operations: [] });
    if (path === '/api/metadata/schemas/orders/links') return reply({ data: [] });
    const result = (query: QuerySpec) => query.aggregate ? { data: [], fields: [model.fields[0], { id: query.aggregate.measures[0].id, name: '金额合计', dataType: 'decimal', precision: 65, scale: 6 }], aggregateMode: true, aggregateRows: [{ values: { state: 'READY', [query.aggregate.measures[0].id]: '9007199254740993.000001' } }], hasMore: false } : { data: [{ id: 'row', schemaId: model.schemaId, etag: 'row', values: { amount: '9007199254740993.000001' } }], fields: [model.fields[1]], hasMore: false };
    if (path === '/api/data/query' && method === 'POST') { const query = request.postDataJSON() as QuerySpec; state.queries.push(query); return reply(result(query)); }
    if (path === '/api/data/query/ontology-sql/analyze') {
      const body = request.postDataJSON();
      return reply({ requestId: body.requestId, textHash: createHash('sha256').update(body.text).digest('hex'), diagnostics: [], parameters: [{ ...template.parameters[0], ranges: [], provided: !!body.parameters?.最低金额 }], fields: [model.fields[1]], ready: !!body.parameters?.最低金额, query: { ...template.query, where: { op: 'gte', field: 'amount', value: body.parameters?.最低金额 ?? '0' } } });
    }
    if (path === '/api/data/query/ontology-sql/complete') { const body = request.postDataJSON(); return reply({ requestId: body.requestId, textHash: createHash('sha256').update(body.text).digest('hex'), items: [] }); }
    if (path === '/api/data/saved-queries/compile') { state.compiles.push(request.postDataJSON()); return reply(template); }
    if (path === '/api/data/saved-queries') {
      if (method === 'GET') return reply({ data: [...records.values()].map(({ id, name, etag, createdAt, updatedAt }) => ({ id, name, etag, createdAt, updatedAt })), hasMore: false });
      const body = request.postDataJSON() as SavedQueryInput; state.creates.push(body);
      const saved = { ...body, id: `saved-${state.creates.length}`, etag: 'created', createdAt: '2026-11-08T00:00:00Z', updatedAt: '2026-11-08T00:00:00Z' };
      records.set(saved.id, saved); return reply(saved, 201);
    }
    const saved = path.match(/^\/api\/data\/saved-queries\/([^/]+)(\/execute)?$/);
    if (saved) {
      const record = records.get(saved[1]);
      if (!record) return reply({ code: 'not_found', message: '查询不存在' }, 404);
      if (saved[2]) { const body = request.postDataJSON(); state.executes.push({ id: saved[1], body }); return reply(result(record.query)); }
      if (method === 'GET') return reply(record);
      state.writes.push({ method, ifMatch: request.headers()['if-match'] });
      if (method === 'DELETE') { records.delete(record.id); return route.fulfill({ status: 204 }); }
      const updated = { ...record, ...request.postDataJSON(), etag: 'updated' }; records.set(record.id, updated); return reply(updated);
    }
    state.unexpected.push(`${method} ${path}`); return reply({ code: 'fixture_unhandled', message: '隔离测试未定义此接口' }, 404);
  });
  return state;
}

for (const width of [1440, 390]) {
  test(`SQL保存不含实参，加载不执行，显式运行、改名与删除 ${width}px`, async ({ page }) => {
    const state = await fixture(page); await page.setViewportSize({ width, height: 1000 }); await page.goto('/#query');
    const editor = page.getByLabel('OntologySQL 编辑器', { exact: true });
    await editor.click(); await page.keyboard.insertText('SELECT "金额" FROM "订单" WHERE "金额" >= :最低金额 LIMIT 25;');
    await page.getByRole('combobox', { name: '最低金额赋值方式' }).selectOption('value');
    await page.locator('input[id="parameter-最低金额"]').fill('123456789012345678.000001');
    await page.getByRole('button', { name: '保存当前查询', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: '保存查询', exact: true });
    await dialog.getByRole('textbox', { name: '查询名称', exact: true }).fill('金额筛选');
    await dialog.getByRole('button', { name: '确认保存', exact: true }).click();
    await expect(page.getByRole('region', { name: '已保存查询', exact: true })).toBeVisible();
    expect(JSON.stringify(state.creates)).not.toContain('123456789012345678');
    expect(state.compiles[0]).not.toHaveProperty('parameters');
    expect(state.executes).toHaveLength(0);
    const runner = page.getByRole('region', { name: '已保存查询', exact: true });
    await expect(runner.getByRole('combobox', { name: '最低金额参数方式' })).toHaveValue('missing');
    await runner.getByRole('combobox', { name: '最低金额参数方式' }).selectOption('value');
    await runner.locator('input[id="saved-最低金额"]').fill('1.000001');
    await runner.getByRole('button', { name: '运行保存查询', exact: true }).click();
    await expect(runner.getByText('9007199254740993.000001', { exact: true })).toBeVisible();
    expect(state.executes[0]).toEqual({ id: 'saved-1', body: { parameters: { 最低金额: '1.000001' } } });
    await page.getByRole('button', { name: /我的查询/ }).click();
    await page.getByRole('button', { name: '重命名查询 金额筛选', exact: true }).click();
    await page.getByRole('dialog', { name: '重命名查询', exact: true }).getByRole('textbox', { name: '查询名称', exact: true }).fill('我的金额查询');
    await page.getByRole('dialog').getByRole('button', { name: '确认保存', exact: true }).click();
    await expect(page.getByRole('heading', { name: '我的金额查询', exact: true })).toBeVisible();
    await page.getByRole('button', { name: '删除查询 我的金额查询', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: '确认删除', exact: true }).click();
    await expect(page.getByRole('region', { name: '已保存查询', exact: true })).toHaveCount(0);
    expect(state.writes).toEqual([{ method: 'PUT', ifMatch: '"created"' }, { method: 'DELETE', ifMatch: '"updated"' }]);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(state.errors).toEqual([]); expect(state.unexpected).toEqual([]);
  });

  test(`可视化分组汇总、精确结果与共用模板 ${width}px`, async ({ page }) => {
    const state = await fixture(page); await page.setViewportSize({ width, height: 1000 }); await page.goto('/#query');
    await page.getByRole('tab', { name: '可视化查询', exact: true }).click();
    await page.getByRole('combobox', { name: '查询结果类型', exact: true }).selectOption('aggregate');
    await page.getByRole('button', { name: '添加条件', exact: true }).click();
    await page.getByRole('combobox', { name: '筛选运算符', exact: true }).selectOption('in');
    await page.getByRole('textbox', { name: '集合值，每行一个', exact: true }).fill('READY\nOLD');
    const config = page.getByRole('region', { name: '分组汇总配置', exact: true });
    await config.getByRole('checkbox', { name: '状态', exact: true }).check();
    await config.getByRole('combobox', { name: '指标1汇总方式', exact: true }).selectOption('sum');
    await config.getByRole('combobox', { name: '指标1计算属性', exact: true }).selectOption('amount');
    await config.getByRole('textbox', { name: '指标1显示名称', exact: true }).fill('金额合计');
    await page.getByRole('button', { name: '运行查询', exact: true }).click();
    await expect(page.getByRole('table', { name: '分组汇总结果', exact: true }).getByText('9007199254740993.000001', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: '新建实例', exact: true })).toHaveCount(0);
    expect(state.queries[0]).toMatchObject({ select: [], aggregate: { groupBy: ['state'], measures: [{ id: 'm0', op: 'sum', field: 'amount', name: '金额合计' }] } });
    await page.getByRole('button', { name: '保存当前查询', exact: true }).click();
    await page.getByRole('dialog').getByRole('textbox', { name: '查询名称', exact: true }).fill('按状态汇总');
    await page.getByRole('dialog').getByRole('button', { name: '确认保存', exact: true }).click();
    await expect(page.getByRole('region', { name: '已保存查询', exact: true })).toBeVisible();
    expect(state.creates[0]).toMatchObject({ source: { kind: 'visual' }, parameters: [{ name: '条件1', dataType: 'string', array: true }], query: { aggregate: state.queries[0].aggregate } });
    expect(JSON.stringify(state.creates[0])).not.toContain('READY');
    expect(state.executes).toHaveLength(0);
    await page.getByRole('region', { name: '已保存查询', exact: true }).getByRole('combobox', { name: '条件1参数方式', exact: true }).selectOption('value');
    await page.getByRole('textbox', { name: '条件1参数值，每行一个', exact: true }).fill('READY\nOLD');
    await page.getByRole('button', { name: '运行保存查询', exact: true }).click();
    await expect(page.getByRole('region', { name: '已保存查询', exact: true }).getByText('9007199254740993.000001', { exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    expect(state.errors).toEqual([]); expect(state.unexpected).toEqual([]);
  });
}
