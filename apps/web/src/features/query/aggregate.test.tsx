import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient } from '../../lib/api';
import type { DefinitionRecord, QueryContextModel } from '../../lib/types';
import { VisualQueryPage } from './VisualQueryPage';
import { aggregateFields, compileQuery, newGroup } from './query';
import { queryToEditor, queryToSQL } from './ontologySql';

const properties = [{ id: 'state', name: '状态', dataType: 'string' as const }, { id: 'amount', name: '金额', dataType: 'decimal' as const, precision: 24, scale: 6 }];
const schema: DefinitionRecord = { status: 'active', etag: 'current', definition: { id: 'orders', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '订单', schema: { mode: 'schema_2_db', storageLayout: 'single', properties } } };
const model: QueryContextModel = { schemaId: 'orders', categoryId: 'category', category: '业务', name: '订单', fields: properties.map((field) => ({ ...field, filterable: true, sortable: true })) };

describe('公共分组汇总', () => {
  it('使用稳定字段与指标标识编译，SQL往返保持分组与汇总含义', () => {
    const editor = { select: [], filter: newGroup(), sort: 'm0', direction: 'desc' as const, limit: '10', aggregate: { groupBy: ['state'], measures: [{ id: 'm0', op: 'sum' as const, field: 'amount', name: '合计金额' }] } };
    const query = compileQuery('orders', properties, editor);
    expect(query.select).toEqual([]);
    expect(query.orderBy).toEqual([{ field: 'm0', direction: 'desc' }]);
    expect(queryToSQL(query, model)).toContain('SUM("订单"."金额") AS "合计金额"');
    expect(queryToSQL(query, model)).toContain('GROUP BY "订单"."状态"\nORDER BY "合计金额" DESC');
    expect(queryToEditor(query, model).aggregate).toEqual(editor.aggregate);
    expect(aggregateFields(editor.aggregate, properties).map((field) => field.id)).toEqual(['state', 'm0']);
    expect(() => compileQuery('orders', properties, { ...editor, aggregate: { groupBy: [], measures: [{ id: 'm0', op: 'avg', field: 'state' }] } })).toThrow('只支持整数');
    const ungrouped = { from: { schemaId: 'orders' }, select: [], page: { limit: 25 }, aggregate: { measures: [{ id: 'm0', op: 'count' as const }] } };
    expect(queryToSQL(ungrouped, model)).toContain('COUNT(*)');
    expect(queryToEditor(ungrouped, model).aggregate?.measures[0].op).toBe('count');
  });

  it('能力开放后按组求和，精确结果无实例操作且分页沿用原请求', async () => {
    const response = { aggregateMode: true, aggregateRows: [{ values: { state: 'READY', m0: '9007199254740993.000001' } }], fields: [properties[0], { ...properties[1], id: 'm0', name: '金额合计' }], data: [], hasMore: true, nextCursor: 'signed-cursor' };
    const query = vi.fn().mockResolvedValue(response);
    const api = { query, dataCapabilities: vi.fn().mockResolvedValue({ aggregates: true }), writeCapabilities: vi.fn().mockResolvedValue({ operations: [] }) } as unknown as ApiClient;
    render(<VisualQueryPage api={api} categoryId="category" schemas={[schema]} onModel={vi.fn()} />);
    const mode = screen.getByRole('combobox', { name: '查询结果类型' });
    await waitFor(() => expect(within(mode).getByRole('option', { name: '分组汇总' }).matches(':disabled')).toBe(false));
    fireEvent.change(mode, { target: { value: 'aggregate' } });
    fireEvent.click(within(screen.getByRole('region', { name: '分组汇总配置' })).getByRole('checkbox', { name: '状态' }));
    fireEvent.change(screen.getByLabelText('指标1汇总方式'), { target: { value: 'sum' } });
    fireEvent.change(screen.getByLabelText('指标1计算属性'), { target: { value: 'amount' } });
    fireEvent.change(screen.getByLabelText('指标1显示名称'), { target: { value: '金额合计' } });
    fireEvent.click(screen.getByRole('button', { name: '运行查询' }));
    await screen.findByText('9007199254740993.000001');
    expect(query.mock.calls[0][0]).toMatchObject({ select: [], aggregate: { groupBy: ['state'], measures: [{ id: 'm0', op: 'sum', field: 'amount', name: '金额合计' }] } });
    expect(screen.queryByRole('button', { name: '新建实例' })).toBeNull();
    expect(screen.queryByRole('button', { name: '查看' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(query).toHaveBeenCalledTimes(2));
    expect(query.mock.calls[1][0]).toEqual({ ...query.mock.calls[0][0], page: { limit: 25, cursor: 'signed-cursor' } });
  });

  it('未声明能力或映射模型禁止启用分组汇总', async () => {
    const api = { dataCapabilities: vi.fn().mockResolvedValue({ aggregates: true }) } as unknown as ApiClient;
    const mapped = { ...schema, definition: { ...schema.definition, schema: { ...schema.definition.schema!, storageBinding: { dataSourceId: 'source', table: 'orders', idColumn: 'id' } } } };
    render(<VisualQueryPage api={api} categoryId="category" schemas={[mapped]} onModel={vi.fn()} />);
    await waitFor(() => expect(api.dataCapabilities).toHaveBeenCalled());
    expect(screen.getByRole('option', { name: '分组汇总' }).matches(':disabled')).toBe(true);
  });

  it('旧后端未声明能力时禁止启用汇总', async () => {
    const api = { dataCapabilities: vi.fn().mockResolvedValue({}) } as unknown as ApiClient;
    render(<VisualQueryPage api={api} categoryId="category" schemas={[schema]} onModel={vi.fn()} />);
    await waitFor(() => expect(api.dataCapabilities).toHaveBeenCalled());
    expect(screen.getByRole('option', { name: '分组汇总' }).matches(':disabled')).toBe(true);
  });
});
