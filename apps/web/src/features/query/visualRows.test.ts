import { describe, expect, it } from 'vitest';
import type { QuerySpec, RowQuery } from '../../lib/types';
import { compileQuery, newGroup } from './query';
import { compileRowQuery, queryToEditor, queryToSQL, queryToSQLDraft, rowQueryModel } from './ontologySql';
import type { RowQueryContext } from './ontologySql';
import { appendVisualPath, assertVisualBindings } from './visualRows';

const amount = { id: 'amount', name: '金额', dataType: 'decimal' as const, precision: 24, scale: 6, filterable: true, sortable: true };
const name = { id: 'name', name: '名"称', dataType: 'string' as const, filterable: true, sortable: true };
const context: RowQueryContext = {
  models: [
    { schemaId: 'orders', categoryId: 'business', category: '业务', name: '订单', fields: [amount] },
    { schemaId: 'customers', categoryId: 'other', category: '客户目录', name: '客户', fields: [name, { ...name, id: 'external', name: '外部字段', filterable: false, sortable: false }] },
  ],
  links: [{ id: 'customer-orders', category: '业务', name: '客户订单', sourceSchemaId: 'customers', targetSchemaId: 'orders' }],
};
const rows: RowQuery = { nodes: [{ id: 'n0', schemaId: 'orders', alias: '订单角色' }, { id: 'n1', schemaId: 'customers', alias: '买方' }], joins: [{ from: 'n0', to: 'n1', linkId: 'customer-orders', direction: 'in' }] };
const base: QuerySpec = { from: { schemaId: 'orders' }, rows, select: ['n0.amount', 'n1.name'], page: { limit: 25 } };

describe('visual relationship query semantics', () => {
  it('keeps reverse joins, role identities, exact decimals and nested predicates across editor and SQL conversion', () => {
    const query: QuerySpec = { ...base, orderBy: [{ field: 'n0.amount', direction: 'desc' }], where: { op: 'not', args: [{ op: 'or', args: [
      { op: 'in', field: 'n0.amount', value: ['0.000001', '9007199254740993.000001'] }, { op: 'isNull', field: 'n1.name' },
    ] }] } };
    const editor = queryToEditor(query, context.models[0], context);
    expect(compileRowQuery(rows, context, editor)).toEqual(query);
    const draft = queryToSQLDraft(query, context.models[0], context);
    expect(draft.text).toContain('JOIN LINK "订单角色" VIA "业务"."客户订单" DIRECTION IN AS "买方"');
    expect(draft.text).toContain('"买方"."名""称" IS NULL');
    expect(draft.text).not.toContain('9007199254740993');
    expect(draft.parameters).toEqual({ 条件1: '0.000001', 条件2: '9007199254740993.000001' });
  });
  it('keeps same-schema self joins as distinct roles and stable field references', () => {
    const selfRows: RowQuery = { nodes: [{ id: 'n0', schemaId: 'customers', alias: '本人' }, { id: 'n1', schemaId: 'customers', alias: '上级' }], joins: [{ from: 'n0', to: 'n1', linkId: 'parent', direction: 'out' }] };
    const selfContext = { ...context, links: [{ id: 'parent', category: '业务', name: '上级客户', sourceSchemaId: 'customers', targetSchemaId: 'customers' }] };
    const query: QuerySpec = { ...base, from: { schemaId: 'customers' }, rows: selfRows, select: ['n0.name', 'n1.name'] };
    const editor = queryToEditor(query, context.models[1], selfContext);
    expect(compileRowQuery(selfRows, selfContext, editor).select).toEqual(['n0.name', 'n1.name']);
    expect(queryToSQL(query, context.models[1], undefined, selfContext)).toContain('"本人"."名""称", "上级"."名""称"');
  });
  it('promotes a single entity query to a row query without adding target fields or losing existing conditions', () => {
    const editor = { filter: { ...newGroup(), children: [{ id: 'condition', kind: 'condition' as const, field: 'amount', op: 'gte', raw: '9007199254740993.000001' }] }, select: ['amount'], sort: 'amount', direction: 'desc' as const, limit: '10' };
    const candidate = { id: 'path', fromAlias: '订单', steps: [{ linkId: 'customer-orders', name: '客户订单', category: '业务', direction: 'in' as const, fromAlias: '订单', toAlias: '客户', target: context.models[1] }], text: '' };
    const added = appendVisualPath({ editor }, candidate, { ...context, links: [] });
    const query = compileRowQuery(added.rows, added.context, added.editor);
    expect(query.select).toEqual(['n0.amount']);
    expect(query.where?.args?.[0]).toEqual({ op: 'gte', field: 'n0.amount', value: '9007199254740993.000001' });
    expect(query.orderBy).toEqual([{ field: 'n0.amount', direction: 'desc' }]);
    expect(compileQuery('orders', context.models[0].fields, editor).select).toEqual(['amount']);
  });
  it('rejects unavailable fields, invalid directions, duplicate roles and over-budget paths without partial conversion', () => {
    expect(() => queryToEditor({ ...base, select: ['n1.external'] }, context.models[0], context)).toThrow('失效');
    expect(() => queryToSQL({ ...base, select: ['n1.external'] }, context.models[0], undefined, context)).toThrow('主表');
    expect(() => rowQueryModel({ ...rows, joins: [{ ...rows.joins[0], direction: 'out' }] }, context)).toThrow('方向');
    expect(() => rowQueryModel({ ...rows, nodes: [rows.nodes[0], { ...rows.nodes[1], alias: '订单角色' }] }, context)).toThrow('唯一');
    expect(() => rowQueryModel({ ...rows, nodes: [...rows.nodes, ...rows.nodes, ...rows.nodes] }, context)).toThrow('4');
    expect(() => queryToEditor({ ...base, where: { op: 'in', field: 'n1.name', value: ['第一行\n第二行'] } }, context.models[0], context)).toThrow('换行');
  });
  it('refuses name reuse that changes a stable schema, Link or attribute binding', () => {
    expect(() => assertVisualBindings(base, structuredClone(base))).not.toThrow();
    expect(() => assertVisualBindings(base, { ...base, rows: { ...rows, nodes: [rows.nodes[0], { ...rows.nodes[1], schemaId: 'replacement' }] } })).toThrow('绑定');
    expect(() => assertVisualBindings(base, { ...base, rows: { ...rows, joins: [{ ...rows.joins[0], linkId: 'replacement' }] } })).toThrow('绑定');
    expect(() => assertVisualBindings(base, { ...base, select: ['n0.replacement', 'n1.name'] })).toThrow('绑定');
  });
});
