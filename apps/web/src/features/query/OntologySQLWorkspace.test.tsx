import { StrictMode, forwardRef, useImperativeHandle } from 'react';
import { createHash } from 'node:crypto';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiError, type ApiClient } from '../../lib/api';
import type { DefinitionRecord, OntologySQLAnalysis, OntologySQLRequest, QueryContextModel, QueryResult, QuerySpec } from '../../lib/types';
import { QueryPage } from './QueryPage';

vi.mock('./OntologySQLEditor', () => ({ default: forwardRef(function TestEditor(props: {
  text: string; onChange: (text: string) => void; onRun: () => void; onComposition: (value: boolean) => void;
  onComplete: (text: string, cursor: number, signal: AbortSignal) => Promise<unknown>;
}, ref) {
  useImperativeHandle(ref, () => ({ insert: (text: string) => props.onChange(props.text + text), locate: vi.fn(), complete: () => props.onComplete(props.text, props.text.length, new AbortController().signal), focus: vi.fn() }));
  return <textarea aria-label="OntologySQL 编辑器" value={props.text} onChange={(event) => props.onChange(event.target.value)} onCompositionStart={() => props.onComposition(true)} onCompositionEnd={() => props.onComposition(false)} onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') props.onRun(); }} />;
}) }));

const field = { id: 'amount', name: '金额', dataType: 'decimal' as const, precision: 24, scale: 6, nullable: true, filterable: true, sortable: true };
const model: QueryContextModel = { schemaId: 'orders', categoryId: 'category', category: '业务', name: '订单', fields: [field] };
const schema: DefinitionRecord = { etag: 'etag', status: 'active', definition: { id: 'orders', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '订单', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [field] } } };
const text = 'SELECT "金额" FROM "业务"."订单" LIMIT 25;';
const query: QuerySpec = { from: { schemaId: 'orders' }, select: ['amount'], page: { limit: 25 } };
const result: QueryResult = { data: [{ id: 'first', schemaId: 'orders', values: { amount: '9007199254740993.000001' }, etag: 'row-etag' }], fields: [field], hasMore: true, nextCursor: 'cursor' };
const hash = (text: string) => createHash('sha256').update(text).digest('hex');

function makeApi(overrides: Record<string, unknown> = {}) {
  return {
    queryContext: vi.fn().mockResolvedValue({ models: [model] }),
    analyzeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest): Promise<OntologySQLAnalysis> => ({ requestId: request.requestId!, textHash: hash(request.text), diagnostics: [], parameters: [], fields: [field], ready: true, query })),
    queryOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ ...result, query, requestId: request.requestId!, textHash: hash(request.text) })),
    completeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ requestId: request.requestId, textHash: hash(request.text), items: [] })),
    formatOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ requestId: request.requestId, textHash: hash(request.text), text: request.text, diagnostics: [] })),
    query: vi.fn().mockResolvedValue(result), getInstance: vi.fn().mockResolvedValue(result.data[0]), ...overrides,
  } as unknown as ApiClient;
}
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((yes) => { resolve = yes; }); return { promise, resolve }; }
function page(api: ApiClient, extra: Partial<Parameters<typeof QueryPage>[0]> = {}) {
  return <QueryPage api={api} categoryId="category" categories={[{ id: 'category', tenantId: 'tenant', name: '业务' }]} schemas={[schema]} onModel={vi.fn()} {...extra} />;
}
async function inputSQL(value = text) { const input = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' }); fireEvent.change(input, { target: { value } }); return input; }

describe('OntologySQL workspace', () => {
  it('defaults to SQL, uses only the authorized catalog, and works through StrictMode effect replay', async () => {
    const api = makeApi(); render(<StrictMode>{page(api)}</StrictMode>);
    expect(screen.getByRole('tab', { name: 'OntologySQL' }).getAttribute('aria-selected')).toBe('true');
    await screen.findByRole('button', { name: /订单.*1.*属性/ });
    await inputSQL();
    await waitFor(() => expect(screen.getByText('检查通过')).toBeTruthy(), { timeout: 2500 });
    expect(api.queryOntologySQL).not.toHaveBeenCalled(); expect(api.query).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText('9007199254740993.000001');
    expect(screen.queryByRole('button', { name: '新建实例' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(api.query).toHaveBeenCalledWith({ ...query, page: { limit: 25, cursor: 'cursor' } }, expect.any(AbortSignal)));
  });
  it('invalidates bound pagination after edits and ignores even a noncooperative late query response', async () => {
    const pending = deferred<unknown>(); const api = makeApi({ queryOntologySQL: vi.fn().mockReturnValueOnce(pending.promise).mockImplementation(async (request: OntologySQLRequest) => ({ ...result, query, requestId: request.requestId, textHash: hash(request.text) })) });
    render(page(api)); const input = await inputSQL();
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await waitFor(() => expect(api.queryOntologySQL).toHaveBeenCalledTimes(1));
    const [request, signal] = vi.mocked(api.queryOntologySQL).mock.calls[0];
    fireEvent.change(input, { target: { value: text.replace('25', '10') } });
    expect(signal?.aborted).toBe(true);
    await act(async () => pending.resolve({ ...result, query, requestId: request.requestId, textHash: hash(request.text) }));
    expect(screen.queryByText('9007199254740993.000001')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '运行查询' })); await screen.findByText('9007199254740993.000001');
    fireEvent.change(input, { target: { value: text } });
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/结果对应上次查询/)).toBeTruthy();
  });
  it('discovers typed parameters, preserves decimals, and keeps missing distinct from an explicitly entered value', async () => {
    const api = makeApi({ analyzeOntologySQL: vi.fn().mockImplementation(async (request: OntologySQLRequest) => ({ requestId: request.requestId, textHash: hash(request.text), diagnostics: [], fields: [field], parameters: [{ name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: false, ranges: [], provided: Object.hasOwn(request.parameters ?? {}, '金额') }], ready: Object.hasOwn(request.parameters ?? {}, '金额'), query: Object.hasOwn(request.parameters ?? {}, '金额') ? query : undefined })) });
    render(page(api)); await inputSQL('SELECT "金额" FROM "订单" WHERE "金额" >= :金额 LIMIT 25;');
    const mode = await screen.findByRole('combobox', { name: '金额赋值方式' });
    expect((mode as HTMLSelectElement).value).toBe('missing');
    fireEvent.change(mode, { target: { value: 'value' } });
    fireEvent.change(screen.getByLabelText(/:金额/), { target: { value: '9007199254740993.000001' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await waitFor(() => expect(api.queryOntologySQL).toHaveBeenCalledWith(expect.objectContaining({ parameters: { 金额: '9007199254740993.000001' } }), expect.any(AbortSignal)));
  });
  it('suppresses remote analysis, completion, formatting and execution during IME composition', async () => {
    const pending = deferred<unknown>(); const api = makeApi({ formatOntologySQL: vi.fn().mockReturnValue(pending.promise) });
    render(page(api)); const input = await inputSQL();
    fireEvent.click(screen.getByRole('button', { name: '格式化' }));
    await waitFor(() => expect(api.formatOntologySQL).toHaveBeenCalledTimes(1));
    const formatRequest = vi.mocked(api.formatOntologySQL).mock.calls[0][0];
    fireEvent.compositionStart(input); const analysisCount = vi.mocked(api.analyzeOntologySQL).mock.calls.length;
    fireEvent.change(input, { target: { value: `${text}\n-- 中文输入` } });
    fireEvent.keyDown(input, { key: 'Enter', ctrlKey: true, isComposing: true });
    await act(async () => pending.resolve({ requestId: formatRequest.requestId, textHash: hash(formatRequest.text), text: 'OLD FORMAT', diagnostics: [] }));
    await new Promise((resolve) => setTimeout(resolve, 550));
    expect(api.analyzeOntologySQL).toHaveBeenCalledTimes(analysisCount); expect(api.queryOntologySQL).not.toHaveBeenCalled();
    expect((input as HTMLTextAreaElement).value).toContain('中文输入');
    expect((screen.getByRole('button', { name: /补全/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.compositionEnd(input);
    await waitFor(() => expect(api.analyzeOntologySQL).toHaveBeenCalledTimes(analysisCount + 1), { timeout: 1800 });
  });
  it('clears results and candidate directory on an authorization diagnostic, as well as HTTP denial', async () => {
    const api = makeApi(); render(page(api)); const input = await inputSQL();
    fireEvent.click(screen.getByRole('button', { name: '运行查询' })); await screen.findByText('9007199254740993.000001');
    vi.mocked(api.analyzeOntologySQL).mockImplementation(async (request) => ({ requestId: request.requestId!, textHash: hash(request.text), diagnostics: [{ code: 'source_unavailable', message: '查询对象不可用', severity: 'error', range: { from: 0, to: 1, line: 1, column: 1 } }], parameters: [], fields: [], ready: false }));
    fireEvent.change(input, { target: { value: `${text} ` } });
    await waitFor(() => expect(screen.queryByText('9007199254740993.000001')).toBeNull(), { timeout: 1800 });
    expect(screen.queryByRole('button', { name: /订单.*1.*属性/ })).toBeNull();
    vi.mocked(api.queryOntologySQL).mockRejectedValue(new ApiError(403, 'forbidden', '权限拒绝'));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' })); await screen.findByText('权限拒绝');
    expect(screen.queryByRole('button', { name: '下一页' })).toBeNull();
  });
  it('switches complete queries to visual and back, while rejecting cross-category queries without losing SQL', async () => {
    const api = makeApi(); render(page(api)); const input = await inputSQL();
    fireEvent.click(screen.getByRole('tab', { name: '可视化查询' }));
    await screen.findByRole('checkbox', { name: '金额' });
    expect((screen.getByRole('checkbox', { name: '金额' }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(screen.getByRole('tab', { name: 'OntologySQL' }));
    const restored = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' });
    expect((restored as HTMLTextAreaElement).value).toContain('FROM "业务"."订单"');
    vi.mocked(api.queryContext).mockResolvedValue({ models: [{ ...model, categoryId: 'other', category: '其他分类' }] });
    fireEvent.change(restored, { target: { value: text.replace('业务', '其他分类') } });
    fireEvent.click(screen.getByRole('tab', { name: '可视化查询' }));
    await screen.findByText(/无法切换：可视化查询只支持顶部当前分类/);
    expect((screen.getByRole('textbox', { name: 'OntologySQL 编辑器' }) as HTMLTextAreaElement).value).toContain('其他分类');
    expect(input).not.toBe(restored);
  });
});
