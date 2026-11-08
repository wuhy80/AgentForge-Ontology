import { Braces, Plus, Trash2, X } from 'lucide-react';
import type { Property } from '../../lib/types';
import { ValueInput } from './InstanceDialog';
import { availableOperators, newCondition, newGroup } from './query';
import type { FilterCondition, FilterGroup, FilterNode } from './query';
function ConditionEditor({ condition, properties, onChange, onRemove }: {
  condition: FilterCondition; properties: Property[]; onChange: (value: FilterCondition) => void; onRemove: () => void;
}) {
  const property = properties.find((item) => item.id === condition.field);
  const noValue = condition.op === 'isNull' || condition.op === 'isNotNull';
  const isSet = condition.op === 'in' || condition.op === 'notIn';
  return <div className={`query-condition ${isSet ? 'query-condition-set' : ''}`}>
    <select aria-label="筛选字段" value={condition.field} onChange={(event) => {
      const next = properties.find((item) => item.id === event.target.value);
      const validOp = availableOperators(next).some((item) => item.value === condition.op) ? condition.op : 'eq';
      onChange({ ...condition, field: event.target.value, op: validOp, raw: next?.dataType === 'boolean' ? 'true' : '' });
    }}>{properties.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}</select>
    <select aria-label="筛选运算符" value={condition.op} onChange={(event) => onChange({ ...condition, op: event.target.value })}>
      {availableOperators(property).map((item) => <option value={item.value} key={item.value}>{item.label}</option>)}
    </select>
    <div className="query-condition-input">{noValue ? <span className="muted query-no-value">无需填写比较值</span>
      : isSet && property?.enumId && property.enumValues ? <><label className="query-sr-only" htmlFor={`condition-${condition.id}`}>枚举集合值</label><select id={`condition-${condition.id}`} multiple value={condition.raw ? condition.raw.split('\n') : []} onChange={(event) => onChange({ ...condition, raw: Array.from(event.target.selectedOptions, (option) => option.value).join('\n') })}>
        {property.enumValues.map((value) => <option value={value.code} key={value.code}>{value.label}（{value.code}）{value.deprecated ? ' · 已停用' : ''}</option>)}
      </select><small className="muted">可多选；停用值仍可用于查询旧数据。</small></>
      : isSet ? <><textarea aria-label="集合值，每行一个" rows={3} value={condition.raw} onChange={(event) => onChange({ ...condition, raw: event.target.value })} placeholder="每行一个值，最多 100 个" />
        <small className="muted">每行一个值；字符串保留空格，空行表示空字符串。</small></>
        : property && <><label className="query-sr-only" htmlFor={`condition-${condition.id}`}>比较值</label><ValueInput property={property} allowDeprecated id={`condition-${condition.id}`} value={condition.raw} onChange={(raw) => onChange({ ...condition, raw })} /></>}
    </div>
    <button type="button" className="button subtle query-icon-button" aria-label="移除此条件" title="移除此条件" onClick={onRemove}><X size={16} /></button>
  </div>;
}

export function GroupEditor({ group, properties, onChange, onRemove, depth = 0 }: {
  group: FilterGroup; properties: Property[]; onChange: (value: FilterGroup) => void; onRemove?: () => void; depth?: number;
}) {
  function replace(value: FilterNode) { onChange({ ...group, children: group.children.map((child) => child.id === value.id ? value : child) }); }
  function remove(id: string) { onChange({ ...group, children: group.children.filter((child) => child.id !== id) }); }
  return <div className={`query-filter-group ${depth ? 'query-filter-group-nested' : ''}`}>
    <div className="query-group-heading"><span className="muted">满足</span><select aria-label={depth ? '子条件组逻辑' : '条件组逻辑'} value={group.join} onChange={(event) => onChange({ ...group, join: event.target.value as 'and' | 'or' })}>
      <option value="and">全部条件 AND</option><option value="or">任意条件 OR</option>
    </select><label className="query-check"><input type="checkbox" checked={group.negated} onChange={(event) => onChange({ ...group, negated: event.target.checked })} />取反 NOT</label>
      {onRemove && <button type="button" className="button subtle query-icon-button" aria-label="移除此条件组" onClick={onRemove}><Trash2 size={15} /></button>}
    </div>
    {!group.children.length && <div className="query-no-filters muted">{depth ? '请为此分组添加条件。' : '尚未添加筛选，将查询当前有权读取的数据。'}</div>}
    <div className="query-filter-children">{group.children.map((child) => child.kind === 'condition'
      ? <ConditionEditor key={child.id} condition={child} properties={properties} onChange={replace} onRemove={() => remove(child.id)} />
      : <GroupEditor key={child.id} group={child} properties={properties} onChange={replace} onRemove={() => remove(child.id)} depth={depth + 1} />)}</div>
    <div className="query-group-actions"><button className="button subtle" type="button" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newCondition(properties)] })}><Plus size={14} />添加条件</button>
      {depth < 3 && <button className="button subtle" type="button" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newGroup()] })}><Braces size={14} />添加分组</button>}</div>
  </div>;
}
