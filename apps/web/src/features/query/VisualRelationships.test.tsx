import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, OntologySQLRequest, QueryContextModel, QueryPathsResult } from '../../lib/types';
import { VisualQueryPage } from './VisualQueryPage';

const models: QueryContextModel[] = [
  { schemaId: 'orders', categoryId: 'business', category: '业务', name: '订单', fields: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true }] },
  { schemaId: 'customers', categoryId: 'business', category: '业务', name: '客户', fields: [{ id: 'name', name: '名称', dataType: 'string', filterable: true, sortable: true }] },
];
const schemas: DefinitionRecord[] = models.map((model) => ({ status: 'active', etag: 'etag', definition: { id: model.schemaId, tenantId: 'tenant', categoryId: 'business', groupId: 'group', kind: 'schema', name: model.name, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields } } }));
const rows = { nodes: [{ id: 'n0', schemaId: 'orders', alias: '订单' }, { id: 'n1', schemaId: 'customers', alias: '客户' }], joins: [{ from: 'n0', to: 'n1', linkId: 'order-customer', direction: 'out' as const }] };
const source = 'SELECT "订单"."金额"\nFROM "业务"."订单" AS "订单"\nLIMIT 25;';
const joined = source.replace('LIMIT', 'JOIN LINK "订单" VIA "业务"."订单客户" DIRECTION OUT AS "客户"\nLIMIT');
const candidate = { id: 'path', fromAlias: '订单', steps: [{ linkId: 'order-customer', category: '业务', name: '订单客户', direction: 'out' as const, fromAlias: '订单', toAlias: '客户', target: models[1] }], text: joined };
const hash = (text: string) => createHash('sha256').update(text).digest('hex');
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((yes) => { resolve = yes; }); return { promise, resolve }; }
function fixture(overrides: Record<string, unknown> = {}) {
  const result = { rowMode: true, data: [], fields: [{ ...models[0].fields[0], id: 'n0.amount', name: '订单.金额' }], rows: [1, 2].map((id) => ({ id: `row-${id}`, values: { 'n0.amount': '9007199254740993.000001' }, entities: [{ nodeId: 'n0', schemaId: 'orders', instanceId: 'same-order' }, { nodeId: 'n1', schemaId: 'customers', instanceId: `customer-${id}` }] })), hasMore: true, nextCursor: 'row-cursor' };
  const api = {
    queryContext: vi.fn().mockImplementation(async (filters: { schemaId?: string }) => ({ models: filters.schemaId ? models.filter((model) => model.schemaId === filters.schemaId) : models })),
    getCurrent: vi.fn().mockResolvedValue({ status: 'active', etag: 'link', definition: { id: 'order-customer', kind: 'link', categoryId: 'business', name: '订单客户', link: { sourceSchemaId: 'orders', targetSchemaId: 'customers' } } }),
    analyzeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ requestId: request.requestId, textHash: hash(request.text), diagnostics: [], parameters: [], fields: [], ready: true, query: { from: { schemaId: 'orders' }, select: request.text.includes('JOIN LINK') ? ['n0.amount'] : ['amount'], ...(request.text.includes('JOIN LINK') ? { rows } : {}), page: { limit: 25 } } })),
    planOntologySQLPaths: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ requestId: request.requestId, textHash: hash(request.text), candidates: [candidate], diagnostics: [], truncated: false })),
    query: vi.fn().mockResolvedValue(result), ...overrides,
  } as unknown as ApiClient;
  render(<VisualQueryPage api={api} categoryId="business" schemas={schemas} categories={[{ id: 'business', tenantId: 'tenant', name: '业务' }]} onModel={vi.fn()} />);
  return api;
}
async function propose() {
  fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
  fireEvent.click(screen.getByRole('button', { name: '添加关联' }));
  await within(screen.getByRole('combobox', { name: '关联目标模型' })).findByRole('option', { name: '业务 / 客户' });
  fireEvent.change(screen.getByRole('combobox', { name: '关联目标模型' }), { target: { value: 'customers' } });
  fireEvent.click(screen.getByRole('button', { name: '查找关联路径' }));
}
async function apply() { await propose(); fireEvent.click(await screen.findByRole('button', { name: '采用关联路径 1' })); await screen.findByRole('checkbox', { name: '客户.名称' }); }

describe('visual relationship authoring', () => {
  it('previews a path before adoption and runs the stable row plan only on explicit execution', async () => {
    const api = fixture(); await propose();
    await screen.findByRole('region', { name: '可视化关联路径选择' });
    expect(screen.getByRole('checkbox', { name: '金额' })).toBeTruthy();
    expect(api.query).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '采用关联路径 1' }));
    const target = await screen.findByRole('checkbox', { name: '客户.名称' });
    expect((target as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole('checkbox', { name: '订单.金额' }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole('button', { name: '新建实例' })).toBeNull();
    fireEvent.click(target);
    fireEvent.click(screen.getByRole('button', { name: '添加条件' }));
    fireEvent.change(screen.getByRole('combobox', { name: '筛选字段' }), { target: { value: 'n0.amount' } });
    fireEvent.change(screen.getByLabelText('比较值'), { target: { value: '9007199254740993.000001' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    const table = await screen.findByRole('table', { name: '关联行查询结果' });
    expect(within(table).getAllByText('9007199254740993.000001')).toHaveLength(2);
    const first = vi.mocked(api.query).mock.calls[0][0];
    expect(first.rows).toEqual(rows); expect(first.select).toEqual(['n0.amount', 'n1.name']);
    expect(first.where?.args?.[0].value).toBe('9007199254740993.000001');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.query).mock.calls[1][0]).toEqual({ ...first, page: { limit: 25, cursor: 'row-cursor' } });
    fireEvent.change(screen.getByLabelText('每页条数'), { target: { value: '10' } });
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(true);
  });
  it('keeps dependent roles until their references are removed and confirms the change to entity results', async () => {
    fixture(); await apply();
    const target = screen.getByRole('checkbox', { name: '客户.名称' }); fireEvent.click(target);
    fireEvent.click(screen.getByRole('button', { name: '移除最后一个关联' }));
    await screen.findByText(/请先移除“客户”角色的返回字段/);
    expect(screen.queryByRole('button', { name: '确认移除关联' })).toBeNull();
    fireEvent.click(target); fireEvent.click(screen.getByRole('button', { name: '移除最后一个关联' }));
    await screen.findByText(/移除后返回单模型对象列表/);
    fireEvent.click(screen.getByRole('button', { name: '确认移除关联' }));
    expect((screen.getByRole('checkbox', { name: '金额' }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole('checkbox', { name: '客户.名称' })).toBeNull();
    expect(screen.getByRole('button', { name: '新建实例' })).toBeTruthy();
  });
  it('allows incomplete role edits but refuses to execute duplicate or empty role names', async () => {
    const api = fixture(); await apply();
    fireEvent.change(screen.getByLabelText('模型角色 2'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText(/模型角色需要唯一名称/); expect(api.query).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('模型角色 2'), { target: { value: '订单' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    expect(api.query).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('模型角色 2'), { target: { value: '买方' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.query).mock.calls[0][0].rows?.nodes[1].alias).toBe('买方');
  });
  it('does not adopt a late path response after editing the current query', async () => {
    const pending = deferred<QueryPathsResult>(); const api = fixture({ planOntologySQLPaths: vi.fn().mockReturnValue(pending.promise) });
    await propose(); await waitFor(() => expect(api.planOntologySQLPaths).toHaveBeenCalledTimes(1));
    const [request, signal] = vi.mocked(api.planOntologySQLPaths).mock.calls[0];
    fireEvent.change(screen.getByLabelText('每页条数'), { target: { value: '10' } });
    expect(signal?.aborted).toBe(true);
    await act(async () => pending.resolve({ requestId: request.requestId!, textHash: hash(request.text), candidates: [candidate], diagnostics: [], truncated: false }));
    expect(screen.queryByRole('button', { name: '采用关联路径 1' })).toBeNull();
    expect(screen.getByRole('checkbox', { name: '金额' })).toBeTruthy();
  });
  it('clears rows and field candidates when row execution is denied and reloads current permissions explicitly', async () => {
    const api = fixture(); await apply(); fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByRole('table', { name: '关联行查询结果' });
    vi.mocked(api.query).mockRejectedValue(new ApiError(403, 'forbidden', '关联查询权限已撤销'));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText('关联查询权限已撤销');
    expect(screen.queryByRole('table')).toBeNull(); expect(screen.queryByRole('checkbox', { name: '客户.名称' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '重新读取查询权限' }));
    await screen.findByRole('checkbox', { name: '客户.名称' }); expect(screen.queryByRole('table')).toBeNull();
  });
  it('confirms a new root before discarding an associated query', async () => {
    fixture(); await apply(); fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: 'customers' } });
    await screen.findByText(/更换查询起点将清空/);
    expect(screen.getByLabelText('模型角色 2')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '确认更换起点' }));
    expect(screen.getByRole('checkbox', { name: '名称' })).toBeTruthy();
    expect(screen.queryByLabelText('模型角色 2')).toBeNull();
  });
  it('clears proposed paths and target candidates if permission is denied while adopting the first relationship', async () => {
    const api = fixture(); await propose(); await screen.findByRole('button', { name: '采用关联路径 1' });
    vi.mocked(api.getCurrent).mockRejectedValue(new ApiError(403, 'forbidden', '关系不可查询'));
    fireEvent.click(screen.getByRole('button', { name: '采用关联路径 1' }));
    await screen.findByText('关系不可查询');
    expect(screen.queryByRole('region', { name: '可视化关联路径选择' })).toBeNull();
    expect(within(screen.getByRole('combobox', { name: '关联目标模型' })).queryByRole('option', { name: '业务 / 客户' })).toBeNull();
    expect(api.query).not.toHaveBeenCalled();
  });
});
