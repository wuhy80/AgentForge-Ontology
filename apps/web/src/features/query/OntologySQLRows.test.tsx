import { forwardRef, useImperativeHandle, useRef } from 'react';
import { createHash } from 'node:crypto';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiError, type ApiClient } from '../../lib/api';
import type { DefinitionRecord, OntologySQLRequest, QueryContextModel, QueryPathsResult, QuerySpec } from '../../lib/types';
import { QueryPage } from './QueryPage';

vi.mock('./OntologySQLEditor', () => ({ default: forwardRef(function TestEditor(props: { text: string; onChange: (text: string) => void; onComposition: (value: boolean) => void }, ref) {
  const input = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => ({ focus: vi.fn(), locate: vi.fn(), complete: vi.fn(), getSelection: () => ({ from: input.current?.selectionStart ?? 0, to: input.current?.selectionEnd ?? 0 }), applyEdit: (edit: { text: string }) => props.onChange(edit.text) }));
  return <textarea ref={input} aria-label="OntologySQL 编辑器" value={props.text} onChange={(event) => props.onChange(event.target.value)} onCompositionStart={() => props.onComposition(true)} onCompositionEnd={() => props.onComposition(false)} />;
}) }));

const amount = { id: 'amount', name: '金额', dataType: 'decimal' as const, precision: 24, scale: 6, filterable: true, sortable: true };
const name = { id: 'name', name: '名称', dataType: 'string' as const, filterable: true, sortable: true };
const orders: QueryContextModel = { schemaId: 'orders', categoryId: 'business', category: '业务', name: '订单', fields: [amount] };
const customers: QueryContextModel = { schemaId: 'customers', categoryId: 'business', category: '业务', name: '客户', fields: [name] };
const source = 'SELECT "o"."金额"\nFROM "业务"."订单" AS "o"\nLIMIT 25;';
const joined = source.replace('LIMIT', 'JOIN LINK "o"."客户" AS "c"\nLIMIT');
const rowPlan: QuerySpec = { from: { schemaId: 'orders' }, rows: { nodes: [{ id: 'n0', schemaId: 'orders', alias: 'o' }, { id: 'n1', schemaId: 'customers', alias: 'c' }], joins: [{ from: 'n0', to: 'n1', linkId: 'order-customer', direction: 'out' }] }, select: ['n0.amount', 'n1.name'], page: { limit: 25 } };
const fields = [{ ...amount, id: 'n0.amount', name: 'o.金额' }, { ...name, id: 'n1.name', name: 'c.名称' }];
const candidate = { id: 'path-1', fromAlias: 'o', steps: [{ linkId: 'order-customer', name: '订单客户', category: '业务', direction: 'out' as const, fromAlias: 'o', toAlias: 'c', target: customers }], text: joined };
const hash = (text: string) => createHash('sha256').update(text).digest('hex');
const response = (request: OntologySQLRequest) => ({ requestId: request.requestId!, textHash: hash(request.text) });
const schemas: DefinitionRecord[] = [orders, customers].map((model) => ({ etag: 'current', status: 'active', definition: { id: model.schemaId, tenantId: 'tenant', categoryId: 'business', groupId: 'group', kind: 'schema', name: model.name, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: model.fields } } }));
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((yes) => { resolve = yes; }); return { promise, resolve }; }
function fixture(overrides: Record<string, unknown> = {}) {
  const api = {
    queryContext: vi.fn().mockResolvedValue({ models: [orders, customers] }),
    getCurrent: vi.fn().mockResolvedValue({ status: 'active', etag: 'link', definition: { id: 'order-customer', kind: 'link', categoryId: 'business', name: '订单客户', link: { sourceSchemaId: 'orders', targetSchemaId: 'customers' } } }),
    analyzeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ ...response(request), fields: request.text.includes('JOIN LINK') ? fields : [amount], diagnostics: [], parameters: [], ready: true, query: request.text.includes('JOIN LINK') ? rowPlan : { from: { schemaId: 'orders' }, select: ['amount'], page: { limit: 25 } } })),
    planOntologySQLPaths: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ ...response(request), candidates: [candidate], diagnostics: [], truncated: false })),
    insertOntologySQLField: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ ...response(request), text: request.text.replace('"o"."金额"', '"o"."金额", "c"."名称"'), from: 0, to: 0, diagnostics: [] })),
    queryOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ ...response(request), query: rowPlan, rowMode: true, data: [], rows: [
      { id: 'path-a', values: { 'n0.amount': '9007199254740993.000001', 'n1.name': '客户一' }, entities: [{ nodeId: 'n0', schemaId: 'orders', instanceId: 'order-1' }, { nodeId: 'n1', schemaId: 'customers', instanceId: 'customer-1' }] },
      { id: 'path-b', values: { 'n0.amount': '9007199254740993.000001', 'n1.name': '客户二' }, entities: [{ nodeId: 'n0', schemaId: 'orders', instanceId: 'order-1' }, { nodeId: 'n1', schemaId: 'customers', instanceId: 'customer-2' }] },
    ], fields, hasMore: true, nextCursor: 'row-cursor' })),
    query: vi.fn().mockResolvedValue({ rowMode: true, data: [], rows: [], fields, hasMore: false }),
    ...overrides,
  } as unknown as ApiClient;
  render(<QueryPage api={api} categoryId="business" schemas={schemas} categories={[{ id: 'business', tenantId: 'tenant', name: '业务' }]} onModel={vi.fn()} />);
  return api;
}
async function inputSQL(value = source) { const input = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' }) as HTMLTextAreaElement; fireEvent.change(input, { target: { value } }); return input; }
async function customer() { return screen.findByRole('button', { name: /客户.*1.*属性/ }); }

describe('多 Link 查询工作台', () => {
  it('模型双击预览所有候选，明确采纳才修改 SQL，保留筛选并可撤销', async () => {
    const original = source.replace('LIMIT', 'WHERE "o"."金额" > 0\nLIMIT');
    const primary = { ...candidate, text: original.replace('WHERE', 'JOIN LINK "o"."客户" AS "c"\nWHERE') };
    const alternate = { ...primary, id: 'path-2', steps: [{ ...candidate.steps[0], name: '订单收货客户', toAlias: '收货客户' }], text: primary.text.replace('AS "c"', 'AS "收货客户"') };
    const api = fixture({ planOntologySQLPaths: vi.fn().mockImplementation(async (request) => ({ ...response(request), candidates: [primary, alternate], diagnostics: [], truncated: false })) });
    const input = await inputSQL(original);
    fireEvent.doubleClick(await customer());
    await screen.findByRole('region', { name: '关联路径选择' });
    expect(input.value).toBe(original);
    expect(screen.getByText('订单收货客户（正向）')).toBeTruthy();
    expect(api.queryOntologySQL).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '使用路径 2' }));
    expect(input.value).toBe(alternate.text);
    expect(input.value).toContain('WHERE "o"."金额" > 0');
    fireEvent.click(screen.getByRole('button', { name: '撤销生成或插入' }));
    expect(input.value).toBe(original);
    expect(api.query).not.toHaveBeenCalled();
  });
  it('文本编辑和中文输入取消路径规划，迟到响应不能恢复候选或改稿', async () => {
    const pending = deferred<QueryPathsResult>(); const api = fixture({ planOntologySQLPaths: vi.fn().mockReturnValue(pending.promise) });
    const input = await inputSQL(); fireEvent.doubleClick(await customer());
    await waitFor(() => expect(api.planOntologySQLPaths).toHaveBeenCalledTimes(1));
    const [request, signal] = vi.mocked(api.planOntologySQLPaths).mock.calls[0];
    fireEvent.change(input, { target: { value: `${source} ` } });
    expect(signal?.aborted).toBe(true);
    await act(async () => pending.resolve({ ...response(request), candidates: [candidate], diagnostics: [], truncated: false }));
    expect(screen.queryByRole('region', { name: '关联路径选择' })).toBeNull();
    expect(input.value).toBe(`${source} `);
    fireEvent.compositionStart(input); fireEvent.doubleClick(await customer());
    expect(api.planOntologySQLPaths).toHaveBeenCalledTimes(1);
    fireEvent.compositionEnd(input);
  });
  it('字段使用服务端绑定，同类型多个角色必须明确选择，缺参的 scope 仍可展示', async () => {
    const self = { ...rowPlan, rows: { ...rowPlan.rows!, nodes: [{ id: 'n0', schemaId: 'customers', alias: '当前客户' }, { id: 'n1', schemaId: 'customers', alias: '上级客户' }] } };
    const api = fixture({ analyzeOntologySQL: vi.fn().mockImplementation(async (request) => ({ ...response(request), fields: [], diagnostics: [], parameters: [], ready: false, scope: self.rows })) });
    const input = await inputSQL(joined);
    fireEvent.click(await customer());
    const aliases = await screen.findByRole('combobox', { name: '字段所属查询别名' });
    fireEvent.doubleClick(screen.getByRole('button', { name: '插入字段 名称' }));
    expect(api.insertOntologySQLField).not.toHaveBeenCalled();
    fireEvent.change(aliases, { target: { value: '上级客户' } });
    fireEvent.doubleClick(screen.getByRole('button', { name: '插入字段 名称' }));
    await waitFor(() => expect(api.insertOntologySQLField).toHaveBeenCalledWith(expect.objectContaining({ schemaId: 'customers', fieldId: 'name', alias: '上级客户' }), expect.any(AbortSignal)));
    await waitFor(() => expect(input.value).toContain('"c"."名称"'));
  });
  it('已选标记区分同模型的两个角色，重复插入不增列，手工删除后同步取消标记', async () => {
    const original = 'SELECT "当前客户"."名称" FROM "业务"."客户" AS "当前客户" JOIN LINK "当前客户"."上级" AS "上级客户" LIMIT 25;';
    const nodes = [{ id: 'n0', schemaId: 'customers', alias: '当前客户' }, { id: 'n1', schemaId: 'customers', alias: '上级客户' }];
    const api = fixture({
      analyzeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({
        ...response(request), diagnostics: [], parameters: [], ready: false, scope: { nodes, joins: [] },
        fields: nodes.filter((node) => request.text.slice(0, request.text.indexOf('FROM')).includes(`"${node.alias}"."名称"`)).map((node) => ({ ...name, id: `${node.id}.name` })),
      })),
      insertOntologySQLField: vi.fn().mockImplementation(async (request) => {
        const reference = `"${request.alias}"."名称"`;
        const text = request.text.includes(reference) ? request.text : request.text.replace(' FROM', `, ${reference} FROM`);
        return { ...response(request), text, from: text.indexOf(reference), to: text.indexOf(reference) + reference.length, diagnostics: [] };
      }),
    });
    const input = await inputSQL(original); fireEvent.click(await customer());
    const aliases = await screen.findByRole('combobox', { name: '字段所属查询别名' });
    const button = screen.getByRole('button', { name: '插入字段 名称' });
    fireEvent.change(aliases, { target: { value: '当前客户' } });
    await waitFor(() => expect(within(button).getByText('已选')).toBeTruthy());
    fireEvent.change(aliases, { target: { value: '上级客户' } });
    expect(within(button).queryByText('已选')).toBeNull();
    fireEvent.doubleClick(button);
    await waitFor(() => expect(input.value).toContain('"当前客户"."名称", "上级客户"."名称"'));
    await waitFor(() => expect(within(button).getByText('已选')).toBeTruthy());
    const inserted = input.value;
    fireEvent.doubleClick(button);
    await screen.findByText('此字段已在查询中，已定位到现有字段。');
    expect(input.value).toBe(inserted);
    expect(api.insertOntologySQLField).toHaveBeenCalledTimes(2);
    fireEvent.change(input, { target: { value: original } });
    const restoredAliases = await screen.findByRole('combobox', { name: '字段所属查询别名' });
    expect(within(button).queryByText('已选')).toBeNull();
    fireEvent.change(restoredAliases, { target: { value: '当前客户' } });
    await waitFor(() => expect(within(button).getByText('已选')).toBeTruthy());
    expect(api.queryOntologySQL).not.toHaveBeenCalled();
  });
  it('单模型的已有返回字段显示已选，重复双击保留 SQL', async () => {
    fixture(); const input = await inputSQL();
    const button = await screen.findByRole('button', { name: '插入字段 金额' });
    await waitFor(() => expect(within(button).getByText('已选')).toBeTruthy());
    fireEvent.doubleClick(button);
    await screen.findByText('此字段已在 SELECT 中，已定位到现有字段。');
    expect(input.value).toBe(source);
  });
  it('迟到字段编辑不覆盖新稿，403会清除路径、角色与结果', async () => {
    const pending = deferred<unknown>(); const api = fixture({ insertOntologySQLField: vi.fn().mockReturnValue(pending.promise) });
    const input = await inputSQL(joined); fireEvent.click(await customer());
    await screen.findByRole('group', { name: '查询中的模型角色' });
    fireEvent.doubleClick(screen.getByRole('button', { name: '插入字段 名称' }));
    await waitFor(() => expect(api.insertOntologySQLField).toHaveBeenCalledTimes(1));
    const [request, signal] = vi.mocked(api.insertOntologySQLField).mock.calls[0];
    fireEvent.change(input, { target: { value: `${joined} ` } });
    expect(signal?.aborted).toBe(true);
    await act(async () => pending.resolve({ ...response(request), text: 'OLD', from: 0, to: 3, diagnostics: [] }));
    expect(input.value).toBe(`${joined} `);
    vi.mocked(api.planOntologySQLPaths).mockRejectedValue(new ApiError(403, 'forbidden', '权限已撤销'));
    fireEvent.doubleClick(await customer());
    await screen.findByText('权限已撤销');
    expect(screen.queryByRole('button', { name: /客户.*1.*属性/ })).toBeNull();
    expect(screen.queryByRole('region', { name: '关联路径选择' })).toBeNull();
  });
  it('关联结果保留重复根及精确值，只读行续页携带完整共享计划，编辑停用旧游标', async () => {
    const api = fixture(); const input = await inputSQL(joined);
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    const table = await screen.findByRole('table', { name: '关联行查询结果' });
    expect(within(table).getAllByText('9007199254740993.000001')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: '关联数据' })).toBeNull();
    expect(screen.queryByRole('button', { name: '查看' })).toBeNull();
    expect(screen.getByText(/同一对象关联多个结果时保留多行/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(api.query).toHaveBeenCalledWith({ ...rowPlan, page: { limit: 25, cursor: 'row-cursor' } }, expect.any(AbortSignal)));
    fireEvent.change(input, { target: { value: `${joined} ` } });
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(true);
  });
  it('关联行完整转换为可视化并恢复SQL，角色与字段保持原绑定', async () => {
    fixture(); await inputSQL(joined);
    fireEvent.click(screen.getByRole('tab', { name: '可视化查询' }));
    const selected = await screen.findByRole('checkbox', { name: 'c.名称' });
    expect((selected as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText('模型角色 1') as HTMLInputElement).value).toBe('o');
    fireEvent.click(screen.getByRole('tab', { name: 'OntologySQL' }));
    const restored = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' });
    expect((restored as HTMLTextAreaElement).value).toContain('JOIN LINK "o" VIA "业务"."订单客户" DIRECTION OUT AS "c"');
  });
  it('首次分析的自动刷新不取消正在读取关联定义的模式转换', async () => {
    fixture({ getCurrent: vi.fn().mockImplementation((_id: string, signal?: AbortSignal) => new Promise((resolve, reject) => {
      const timer = setTimeout(() => resolve({ status: 'active', etag: 'link', definition: { id: 'order-customer', kind: 'link', categoryId: 'business', name: '订单客户', link: { sourceSchemaId: 'orders', targetSchemaId: 'customers' } } }), 25);
      signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); }, { once: true });
    })) });
    await inputSQL(joined); fireEvent.click(screen.getByRole('tab', { name: '可视化查询' }));
    const selected = await screen.findByRole('checkbox', { name: 'c.名称' });
    expect((selected as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole('alert')).toBeNull();
  });
  it('Link 授权诊断清除先前关联结果和字段目录', async () => {
    const api = fixture(); const input = await inputSQL(joined);
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByRole('table', { name: '关联行查询结果' });
    vi.mocked(api.analyzeOntologySQL).mockImplementation(async (request) => ({ ...response(request), ready: false, fields: [], parameters: [], diagnostics: [{ code: 'link_unavailable', severity: 'error', message: '关系不可用', range: { from: 0, to: 4, line: 1, column: 1 } }] }));
    fireEvent.change(input, { target: { value: `${joined} ` } });
    await screen.findByText('关系不可用');
    expect(screen.queryByRole('table', { name: '关联行查询结果' })).toBeNull();
    expect(screen.queryByRole('button', { name: /客户.*1.*属性/ })).toBeNull();
    expect(screen.queryByRole('button', { name: '插入字段 名称' })).toBeNull();
  });
});
