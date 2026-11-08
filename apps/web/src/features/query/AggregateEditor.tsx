import { Plus, Trash2 } from 'lucide-react';
import type { AggregateOperation, Property, QueryAggregate } from '../../lib/types';
import { aggregateLabels } from './query';

export function AggregateEditor({ aggregate, properties, onChange }: { aggregate: QueryAggregate; properties: Property[]; onChange: (value: QueryAggregate) => void }) {
  const available = properties.filter((field) => !['text', 'longtext'].includes(field.dataType));
  const groupBy = aggregate.groupBy ?? [];
  return <section className="query-aggregate-editor" aria-label="分组汇总配置">
    <div className="query-step-title"><h3>分组汇总</h3><small className="muted">先筛选，再按分组计算；最多 1000 组</small></div>
    <fieldset className="query-aggregate-groups"><legend>分组属性（最多 3 个，可不分组）</legend>{available.map((field) => <label key={field.id} className="query-check"><input type="checkbox" checked={groupBy.includes(field.id)} disabled={!groupBy.includes(field.id) && groupBy.length >= 3} onChange={(event) => onChange({ ...aggregate, groupBy: event.target.checked ? [...groupBy, field.id] : groupBy.filter((id) => id !== field.id) })} />{field.name}</label>)}</fieldset>
    <div className="query-section-heading"><h3>汇总指标</h3><button type="button" className="button subtle" disabled={aggregate.measures.length >= 20} onClick={() => onChange({ ...aggregate, measures: [...aggregate.measures, { id: `m${crypto.randomUUID().replaceAll('-', '').slice(0, 12)}`, op: 'count', name: '' }] })}><Plus size={14} />添加指标</button></div>
    {aggregate.measures.map((measure, index) => <div key={measure.id} className="query-aggregate-measure">
      <label>汇总方式<select aria-label={`指标${index + 1}汇总方式`} value={measure.op} onChange={(event) => {
        const op = event.target.value as AggregateOperation;
        const source = available.find((field) => field.id === measure.field);
        const field = source && (!['sum', 'avg'].includes(op) || ['int64', 'decimal'].includes(source.dataType)) && (!['min', 'max'].includes(op) || source.dataType !== 'boolean') ? source.id : undefined;
        onChange({ ...aggregate, measures: aggregate.measures.map((item) => item.id === measure.id ? { ...item, op, field } : item) });
      }}>{Object.entries(aggregateLabels).map(([op, label]) => <option value={op} key={op}>{label}</option>)}</select></label>
      <label>计算属性<select aria-label={`指标${index + 1}计算属性`} value={measure.field ?? ''} onChange={(event) => onChange({ ...aggregate, measures: aggregate.measures.map((item) => item.id === measure.id ? { ...item, field: event.target.value || undefined } : item) })}><option value="">{measure.op === 'count' ? '全部记录 COUNT(*)' : '选择属性'}</option>{(measure.op === 'count' ? properties : available).filter((field) => (!['sum', 'avg'].includes(measure.op) || ['int64', 'decimal'].includes(field.dataType)) && (!['min', 'max'].includes(measure.op) || field.dataType !== 'boolean')).map((field) => <option key={field.id} value={field.id}>{field.name}</option>)}</select></label>
      <label>显示名称<input aria-label={`指标${index + 1}显示名称`} value={measure.name ?? ''} maxLength={128} placeholder="例如：合计金额" onChange={(event) => onChange({ ...aggregate, measures: aggregate.measures.map((item) => item.id === measure.id ? { ...item, name: event.target.value } : item) })} /></label>
      <button type="button" className="button subtle query-icon-button" aria-label={`删除指标${index + 1}`} onClick={() => onChange({ ...aggregate, measures: aggregate.measures.filter((item) => item.id !== measure.id) })}><Trash2 size={15} /></button>
    </div>)}
    <p className="muted">计数忽略空值，全部记录计数包含所有匹配记录；数值按精确字符串返回。超过组数预算时请缩小筛选范围。</p>
  </section>;
}
