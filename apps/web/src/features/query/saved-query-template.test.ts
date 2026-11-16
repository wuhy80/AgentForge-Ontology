import { describe, expect, it } from 'vitest';
import { newGroup } from './query';
import { visualRowSavedTemplate, visualSavedTemplate } from './saved-query-template';
import type { QueryContextModel } from '../../lib/types';

describe('可视化查询模板', () => {
  it('关联模板保留多对象稳定绑定，按节点推导参数类型且不保存运行值', () => {
    const rows = { nodes: [{ id: 'n0', schemaId: 'orders', alias: '父' }, { id: 'n1', schemaId: 'orders', alias: '子' }], joins: [{ from: 'n0', to: 'n1', linkId: 'relation', direction: 'out' as const }] };
    const model: QueryContextModel = { schemaId: 'orders', name: '订单', category: '业务', categoryId: 'category', fields: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true }] };
    const context = { models: [model], links: [{ id: 'relation', category: '业务', name: '父子', sourceSchemaId: 'orders', targetSchemaId: 'orders' }] };
    const filter = newGroup();
    filter.children = [{ id: 'a', kind: 'condition', field: 'n1.amount', op: 'gte', raw: 'PRIVATE_RUNTIME_VALUE' }];
    const template = visualRowSavedTemplate(rows, context, { filter, select: ['n0.amount', 'n1.amount'], sort: 'n1.amount', direction: 'desc', limit: '1' });
    expect(template.query.rows).toEqual(rows);
    expect(template.query.where).toEqual({ op: 'and', args: [{ op: 'gte', field: 'n1.amount', parameter: '条件1' }] });
    expect(template.parameters).toEqual([{ name: '条件1', dataType: 'decimal', nullable: false, precision: 24, scale: 6 }]);
    expect(JSON.stringify(template)).not.toContain('PRIVATE');
    expect(template.query.orderBy).toEqual([{ field: 'n1.amount', direction: 'desc' }]);
  });
  it('只保存稳定字段与参数声明，丢弃输入实值且可在运行值未填写时保存', () => {
    const filter = newGroup();
    filter.children = [{ id: 'a', kind: 'condition', field: 'amount', op: 'gte', raw: 'PRIVATE_RUNTIME_VALUE' }, { id: 'b', kind: 'condition', field: 'state', op: 'in', raw: 'PRIVATE_CODE' }];
    const template = visualSavedTemplate('orders', [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6 }, { id: 'state', name: '状态', dataType: 'string', nullable: true, enumId: 'state-enum' }], { filter, select: ['amount'], sort: '', direction: 'asc', limit: '25' });
    expect(template.query.where).toEqual({ op: 'and', args: [{ op: 'gte', field: 'amount', parameter: '条件1' }, { op: 'in', field: 'state', parameter: '条件2' }] });
    expect(template.parameters).toEqual([{ name: '条件1', dataType: 'decimal', nullable: false, precision: 24, scale: 6 }, { name: '条件2', dataType: 'string', nullable: true, array: true, enumId: 'state-enum' }]);
    expect(JSON.stringify(template)).not.toContain('PRIVATE');
    expect(filter.children[0].kind === 'condition' && filter.children[0].raw).toBe('PRIVATE_RUNTIME_VALUE');
  });
});
