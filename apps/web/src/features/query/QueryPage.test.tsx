import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, Instance, QueryResult } from '../../lib/types';
import { InstanceDialog } from './InstanceDialog';
import { QueryPage } from './QueryPage';

const amount = { id: 'amount', name: '金额', dataType: 'decimal' as const, precision: 24, scale: 6, required: true };
const schema: DefinitionRecord = {
  etag: 'schema-etag', status: 'active',
  definition: { id: 'schema-a', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '订单', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [amount] } },
};
const instance: Instance = { id: 'instance-123', schemaId: 'schema-a', etag: 'strong-etag', values: { amount: '9007199254740993.000001' } };
const result: QueryResult = { fields: [{ ...amount, name: '服务器金额' }], data: [instance], nextCursor: 'server-cursor', hasMore: true };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

describe('query request lifecycle', () => {
  it('映射对象可投影其他来源字段，仅主表属性可筛选排序，实例详情不提供写入入口', async () => {
    const primary = { ...amount, mapping: { dataSourceId: 'mysql-source', column: 'amount' } };
    const external = { id: 'status', name: '外部状态', dataType: 'string' as const, nullable: true, mapping: { dataSourceId: 'pg-source', namespace: 'public', table: 'states', column: 'status', keyColumn: 'order_id' } };
    const mapped: DefinitionRecord = { ...schema, definition: { ...schema.definition, schema: { ...schema.definition.schema!, mode: 'db_2_schema', storageBinding: { dataSourceId: 'mysql-source', table: 'orders', idColumn: 'id' }, properties: [primary, external] } } };
    const query = vi.fn().mockResolvedValue({ ...result, fields: [primary, external], data: [{ ...instance, values: { ...instance.values, status: 'ready' } }] });
    const getInstance = vi.fn().mockResolvedValue({ ...instance, values: { ...instance.values, status: 'ready' } });
    const api = { query, getInstance } as unknown as ApiClient;
    render(<QueryPage categoryId="category" api={api} schemas={[mapped]} onModel={vi.fn()} />);
    expect(screen.queryByRole('button', { name: '新建实例' })).toBeNull();
    expect(within(screen.getByLabelText('排序字段')).queryByRole('option', { name: '外部状态' })).toBeNull();
    fireEvent.click(screen.getByRole('checkbox', { name: '外部状态' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText('ready');
    expect(query.mock.calls[0][0].select).toEqual(['status']);
    fireEvent.click(screen.getByRole('button', { name: '查看' }));
    const detail = await screen.findByRole('dialog');
    await within(detail).findByText('ready');
    expect(within(detail).queryByRole('button', { name: '编辑实例' })).toBeNull();
    expect(within(detail).queryByRole('button', { name: '删除实例' })).toBeNull();
  });

  it('applies a repeated navigation request even when its target schema ID is unchanged', () => {
    const api = { query: vi.fn() } as unknown as ApiClient;
    const other: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'schema-b', name: '客户' } };
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} initialSchemaId="schema-a" selectionRequest={1} onModel={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: 'schema-b' } });
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe('schema-b');
    rerender(<QueryPage categoryId="category" api={api} schemas={[schema, other]} initialSchemaId="schema-a" selectionRequest={2} onModel={vi.fn()} />);
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe('schema-a');
  });

  it('cancels and ignores late responses; flags changed configuration and reuses only executed spec for paging', async () => {
    const first = deferred<QueryResult>(), second = deferred<QueryResult>(), third = deferred<QueryResult>(), fourth = deferred<QueryResult>();
    const query = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockReturnValueOnce(third.promise).mockReturnValueOnce(fourth.promise);
    const api = { query } as unknown as ApiClient;
    render(<QueryPage categoryId="category" api={api} schemas={[schema]} onModel={vi.fn()} />);
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    const firstSignal = query.mock.calls[0][1] as AbortSignal;
    fireEvent.click(screen.getByRole('button', { name: '取消查询' }));
    expect(firstSignal.aborted).toBe(true);
    await act(async () => first.resolve(result));
    expect(screen.queryByText(instance.values.amount as string)).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await act(async () => second.resolve(result));
    expect(screen.getByText(instance.values.amount as string)).toBeTruthy();
    expect(screen.getByRole('columnheader', { name: /服务器金额/ })).toBeTruthy();
    fireEvent.change(screen.getByLabelText('每页条数'), { target: { value: '10' } });
    expect(screen.getByText(/结果已过期/)).toBeTruthy();
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    expect(query.mock.calls[2][0].page).toEqual({ limit: 10 });
    await act(async () => third.resolve(result));
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(query.mock.calls[3][0]).toEqual({ ...query.mock.calls[2][0], page: { limit: 10, cursor: 'server-cursor' } });
    fireEvent.click(screen.getByRole('button', { name: '取消查询' }));
    await act(async () => fourth.resolve({ ...result, data: [{ ...instance, values: { amount: 'late' } }] }));
    expect(screen.queryByText('late')).toBeNull();
  });

  it('aborts on a model or client switch and never renders the previous request data', async () => {
    const pending = deferred<QueryResult>();
    const query = vi.fn().mockReturnValue(pending.promise);
    const api = { query } as unknown as ApiClient;
    const other: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'schema-b', name: '客户' } };
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    const signal = query.mock.calls[0][1] as AbortSignal;
    fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: 'schema-b' } });
    expect(signal.aborted).toBe(true);
    await act(async () => pending.resolve(result));
    expect(screen.queryByText(instance.id)).toBeNull();
    rerender(<QueryPage categoryId="category" api={{ query: vi.fn() } as unknown as ApiClient} schemas={[schema, other]} onModel={vi.fn()} />);
    expect(screen.queryByText(instance.id)).toBeNull();
    expect((screen.getByRole('checkbox', { name: '金额' }) as HTMLInputElement).checked).toBe(false);
  });

  it('allows retrying a failed continuation but requires a fresh run after cursor expiry', async () => {
    const query = vi.fn().mockResolvedValueOnce(result)
      .mockRejectedValueOnce(new ApiError(0, 'network_error', '网络中断'))
      .mockRejectedValueOnce(new ApiError(422, 'invalid_cursor', '游标已失效'))
      .mockResolvedValueOnce({ ...result, hasMore: false, nextCursor: undefined });
    render(<QueryPage categoryId="category" api={{ query } as unknown as ApiClient} schemas={[schema]} onModel={vi.fn()} />);
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText(instance.values.amount as string);
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('网络中断');
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('游标已失效');
    expect((screen.getByRole('button', { name: '下一页' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await waitFor(() => expect(query).toHaveBeenCalledTimes(4));
    expect(query.mock.calls[3][0].page).toEqual({ limit: 25 });
  });

  it('clears previously read results when a later request is denied', async () => {
    const query = vi.fn().mockResolvedValueOnce(result).mockRejectedValueOnce(new ApiError(403, 'forbidden', '无权读取此模型'));
    render(<QueryPage categoryId="category" api={{ query } as unknown as ApiClient} schemas={[schema]} onModel={vi.fn()} />);
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText(instance.values.amount as string);
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('无权读取此模型');
    expect(screen.queryByText(instance.values.amount as string)).toBeNull();
    expect(screen.queryByText(instance.id)).toBeNull();
  });

  it('only shows models in the current category and retains their category labels', () => {
    const other = { ...schema, definition: { ...schema.definition, id: 'schema-b', categoryId: 'other-category' } };
    const categories = [{ id: 'category', tenantId: 'tenant', name: '销售' }, { id: 'other-category', tenantId: 'tenant', name: '采购' }];
    const api = { query: vi.fn() } as unknown as ApiClient;
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} initialSchemaId={other.definition.id} categories={categories} onModel={vi.fn()} />);
    expect(screen.getByRole('option', { name: '销售 / 订单' })).toBeTruthy();
    expect(screen.queryByRole('option', { name: '采购 / 订单' })).toBeNull();
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(schema.definition.id);
    rerender(<QueryPage categoryId="other-category" api={api} schemas={[schema, other]} categories={categories} onModel={vi.fn()} />);
    expect(screen.getByRole('option', { name: '采购 / 订单' })).toBeTruthy();
    expect(screen.queryByRole('option', { name: '销售 / 订单' })).toBeNull();
  });

  it('does not expose an out-of-category model when the selected category is empty or missing', () => {
    const query = vi.fn(), onModel = vi.fn(), api = { query } as unknown as ApiClient;
    const { rerender } = render(<QueryPage categoryId="empty-category" api={api} schemas={[schema]} initialSchemaId={schema.definition.id} onModel={onModel} />);
    expect(screen.getByText('当前分类暂无生效模型')).toBeTruthy();
    expect(screen.queryByLabelText('查询模型')).toBeNull();
    expect(screen.queryByRole('button', { name: '新建实例' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '前往模型管理' }));
    expect(onModel).toHaveBeenCalledTimes(1);
    rerender(<QueryPage categoryId="" api={api} schemas={[schema]} onModel={onModel} />);
    expect(screen.getByText('请先选择当前分类')).toBeTruthy();
    expect(screen.queryByRole('button', { name: '运行查询' })).toBeNull();
    expect(query).not.toHaveBeenCalled();
  });

  it('resets the local model selection on category changes while accepting only in-category navigation targets', () => {
    const api = { query: vi.fn() } as unknown as ApiClient;
    const sameCategory = { ...schema, definition: { ...schema.definition, id: 'schema-second', name: '订单明细' } };
    const otherCategory = { ...schema, definition: { ...schema.definition, id: 'schema-other', categoryId: 'other-category', name: '客户' } };
    const schemas = [schema, sameCategory, otherCategory];
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={schemas} onModel={vi.fn()} />);
    fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: sameCategory.definition.id } });
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(sameCategory.definition.id);
    rerender(<QueryPage categoryId="other-category" api={api} schemas={schemas} initialSchemaId={sameCategory.definition.id} selectionRequest={1} onModel={vi.fn()} />);
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(otherCategory.definition.id);
    rerender(<QueryPage categoryId="category" api={api} schemas={schemas} onModel={vi.fn()} />);
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(schema.definition.id);
    rerender(<QueryPage categoryId="category" api={api} schemas={schemas} initialSchemaId={sameCategory.definition.id} selectionRequest={2} onModel={vi.fn()} />);
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(sameCategory.definition.id);
  });

  it('applies an in-category navigation target once its directory finishes loading', () => {
    const api = { query: vi.fn() } as unknown as ApiClient;
    const target = { ...schema, definition: { ...schema.definition, id: 'schema-target', name: '订单明细' } };
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[]} initialSchemaId={target.definition.id} selectionRequest={1} onModel={vi.fn()} />);
    expect(screen.queryByLabelText('查询模型')).toBeNull();
    rerender(<QueryPage categoryId="category" api={api} schemas={[schema, target]} initialSchemaId={target.definition.id} selectionRequest={1} onModel={vi.fn()} />);
    expect((screen.getByLabelText('查询模型') as HTMLSelectElement).value).toBe(target.definition.id);
  });

  it('aborts a prior category request and clears filters, results, cursors and instance dialogs on category changes', async () => {
    const pending = deferred<QueryResult>();
    const query = vi.fn().mockResolvedValueOnce(result).mockReturnValueOnce(pending.promise).mockResolvedValueOnce({ ...result, data: [], hasMore: false, nextCursor: undefined });
    const api = { query } as unknown as ApiClient;
    const other: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'schema-b', categoryId: 'other-category', name: '客户' } };
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '添加条件' }));
    fireEvent.change(screen.getByLabelText('比较值'), { target: { value: '1.000001' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText(instance.values.amount as string);
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    const signal = query.mock.calls[1][1] as AbortSignal;
    fireEvent.click(screen.getByRole('button', { name: '新建实例' }));
    expect(screen.getByRole('dialog')).toBeTruthy();
    rerender(<QueryPage categoryId="other-category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    expect(signal.aborted).toBe(true);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByText(instance.values.amount as string)).toBeNull();
    expect(screen.queryByRole('button', { name: '下一页' })).toBeNull();
    expect(screen.queryByLabelText('比较值')).toBeNull();
    expect((screen.getByRole('checkbox', { name: '金额' }) as HTMLInputElement).checked).toBe(false);
    await act(async () => pending.resolve(result));
    expect(screen.queryByText(instance.id)).toBeNull();
    fireEvent.click(screen.getByRole('checkbox', { name: '金额' }));
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText('没有匹配的数据');
    expect(query.mock.calls[2][0]).toEqual({ from: { schemaId: other.definition.id }, select: ['amount'], page: { limit: 25 } });
  });
});

describe('instance mutations', () => {
  async function submitCreate() {
    fireEvent.click(screen.getByRole('button', { name: '新建实例' }));
    const dialog = within(screen.getByRole('dialog'));
    fireEvent.change(dialog.getByLabelText('金额'), { target: { value: '9007199254740993.000001' } });
    fireEvent.click(dialog.getByRole('button', { name: '创建实例' }));
  }

  function closeInstance() {
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '关闭' }));
  }

  it('retains uncertain creation intents across category switches without retaining instance dialogs', async () => {
    const createInstance = vi.fn().mockRejectedValueOnce(new ApiError(0, 'network_error', '连接中断')).mockResolvedValueOnce(instance);
    const api = { createInstance } as unknown as ApiClient;
    const other: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'schema-b', categoryId: 'other-category', name: '客户' } };
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    const firstKey = createInstance.mock.calls[0][2];
    rerender(<QueryPage categoryId="other-category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    rerender(<QueryPage categoryId="category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    await submitCreate();
    await screen.findByRole('button', { name: '编辑实例' });
    expect(createInstance.mock.calls[1][2]).toBe(firstKey);
  });

  it('retains uncertain creation intents across schema switches, isolates schemas and releases confirmed intents', async () => {
    const createInstance = vi.fn()
      .mockRejectedValueOnce(new ApiError(0, 'network_error', '连接中断'))
      .mockRejectedValueOnce(new ApiError(0, 'network_error', '连接中断'))
      .mockResolvedValueOnce(instance).mockResolvedValueOnce(instance);
    const api = { createInstance } as unknown as ApiClient;
    const other: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'schema-b', name: '客户' } };
    render(<QueryPage categoryId="category" api={api} schemas={[schema, other]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    const firstKey = createInstance.mock.calls[0][2];
    closeInstance();
    fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: 'schema-b' } });
    await submitCreate();
    await screen.findByText('连接中断');
    expect(createInstance.mock.calls[1][2]).not.toBe(firstKey);
    closeInstance();
    fireEvent.change(screen.getByLabelText('查询模型'), { target: { value: 'schema-a' } });
    await submitCreate();
    await screen.findByRole('button', { name: '编辑实例' });
    expect(createInstance.mock.calls[2][2]).toBe(firstKey);
    closeInstance();
    await submitCreate();
    await screen.findByRole('button', { name: '编辑实例' });
    expect(createInstance.mock.calls[3][2]).not.toBe(firstKey);
  });

  it('retains an uncertain intent after an ETag refresh and clears it when the API session changes', async () => {
    const createInstance = vi.fn().mockRejectedValue(new ApiError(0, 'network_error', '连接中断'));
    const api = { createInstance } as unknown as ApiClient;
    const { rerender } = render(<QueryPage categoryId="category" api={api} schemas={[schema]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    const firstKey = createInstance.mock.calls[0][2];
    closeInstance();
    const updated: DefinitionRecord = { ...schema, etag: 'renamed-etag', definition: { ...schema.definition, name: '订单明细' } };
    rerender(<QueryPage categoryId="category" api={api} schemas={[updated]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    expect(createInstance.mock.calls[1][2]).toBe(firstKey);
    const nextCreate = vi.fn().mockRejectedValue(new ApiError(0, 'network_error', '连接中断'));
    rerender(<QueryPage categoryId="category" api={{ createInstance: nextCreate } as unknown as ApiClient} schemas={[updated]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    expect(nextCreate.mock.calls[0][2]).not.toBe(firstKey);
    rerender(<QueryPage categoryId="category" api={api} schemas={[updated]} onModel={vi.fn()} />);
    await submitCreate();
    await screen.findByText('连接中断');
    expect(createInstance.mock.calls[2][2]).not.toBe(firstKey);
  });

  it('reuses the same idempotency key and exact decimal string after uncertain creation', async () => {
    const createInstance = vi.fn().mockRejectedValueOnce(new ApiError(0, 'network_error', '连接中断')).mockResolvedValueOnce(instance);
    const onChanged = vi.fn();
    render(<InstanceDialog api={{ createInstance } as unknown as ApiClient} schema={schema} onClose={vi.fn()} onChanged={onChanged} />);
    const dialog = within(screen.getByRole('dialog'));
    fireEvent.change(dialog.getByLabelText('金额'), { target: { value: '9007199254740993.000001' } });
    fireEvent.click(dialog.getByRole('button', { name: '创建实例' }));
    await screen.findByText('连接中断');
    fireEvent.click(dialog.getByRole('button', { name: '创建实例' }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith('实例已创建'));
    expect(createInstance.mock.calls).toHaveLength(2);
    expect(createInstance.mock.calls[0]).toEqual(['schema-a', { amount: '9007199254740993.000001' }, expect.any(String)]);
    expect(createInstance.mock.calls[1][2]).toBe(createInstance.mock.calls[0][2]);
  });

  it('reads the full instance before editing and sends only changed fields with the fresh ETag', async () => {
    const time = { id: 'time', name: '发生时间', dataType: 'timestamp' as const, nullable: true };
    const protectedField = { id: 'protected', name: '生成编号', dataType: 'string' as const, readOnly: true };
    const editSchema: DefinitionRecord = { ...schema, definition: { ...schema.definition, schema: { ...schema.definition.schema!, properties: [amount, time, protectedField] } } };
    const original: Instance = { ...instance, values: { ...instance.values, time: '2026-11-06T14:30:00.123456+08:00', protected: 'server-generated' } };
    const getInstance = vi.fn().mockResolvedValue(original);
    const patchInstance = vi.fn().mockResolvedValue({ ...original, etag: 'new-etag', values: { ...original.values, amount: '9007199254740992.000001' } });
    render(<InstanceDialog api={{ getInstance, patchInstance } as unknown as ApiClient} schema={editSchema} instanceId={instance.id} onClose={vi.fn()} onChanged={vi.fn()} />);
    await screen.findByText('server-generated');
    fireEvent.click(screen.getByRole('button', { name: '编辑实例' }));
    expect(screen.queryByLabelText('生成编号的写入方式')).toBeNull();
    fireEvent.change(screen.getByLabelText('金额的写入方式'), { target: { value: 'value' } });
    fireEvent.change(screen.getByLabelText('金额'), { target: { value: '9007199254740992.000001' } });
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }));
    await waitFor(() => expect(patchInstance).toHaveBeenCalledWith('schema-a', instance.id, { amount: '9007199254740992.000001' }, 'strong-etag'));
    expect(getInstance.mock.calls[0].slice(0, 2)).toEqual(['schema-a', instance.id]);
  });
});
