import { describe, expect, it } from 'vitest';
import { newGroup } from './query';
import { visualSavedTemplate } from './saved-query-template';

describe('可视化查询模板', () => {
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
