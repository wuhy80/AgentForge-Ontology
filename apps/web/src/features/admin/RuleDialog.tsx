import { useState } from 'react'
import { Plus, Save, Trash2, X } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import type { DefinitionRecord, Organization, PermissionRule, Property, Role } from '../../lib/types'
import { availableOperators, compileFilter, errorMessage, newCondition, newGroup } from '../query/query'
import type { FilterCondition, FilterGroup, FilterNode } from '../query/query'
import { filterFromExpression, operations, operationResources, resourceName } from './policy'

function ConditionInput({ condition, properties, onChange, onRemove }: { condition: FilterCondition; properties: Property[]; onChange: (value: FilterCondition) => void; onRemove: () => void }) {
  const property = properties.find((item) => item.id === condition.field)
  const noValue = ['isNull', 'isNotNull'].includes(condition.op)
  const set = ['in', 'notIn'].includes(condition.op)
  return <div className="admin-condition">
    <select aria-label="行条件字段" value={condition.field} onChange={(event) => {
      const field = properties.find((item) => item.id === event.target.value)
      onChange({ ...condition, field: event.target.value, op: availableOperators(field).some((item) => item.value === condition.op) ? condition.op : 'eq', raw: field?.dataType === 'boolean' ? 'true' : '' })
    }}>{properties.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
    <select aria-label="行条件运算符" value={condition.op} onChange={(event) => onChange({ ...condition, op: event.target.value })}>{availableOperators(property).map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select>
    {noValue ? <span className="muted">无需比较值</span> : set ? <textarea aria-label="行条件集合，每行一个值" value={condition.raw} rows={2} onChange={(event) => onChange({ ...condition, raw: event.target.value })} placeholder="每行一个值" />
      : property?.dataType === 'boolean' ? <select aria-label="行条件比较值" value={condition.raw} onChange={(event) => onChange({ ...condition, raw: event.target.value })}><option value="true">是</option><option value="false">否</option></select>
        : property?.dataType === 'text' || property?.dataType === 'longtext' ? <textarea aria-label="行条件比较值" value={condition.raw} rows={2} onChange={(event) => onChange({ ...condition, raw: event.target.value })} />
        : <input aria-label="行条件比较值" type="text" value={condition.raw} onChange={(event) => onChange({ ...condition, raw: event.target.value })} placeholder={property?.dataType === 'timestamp' ? '带时区的 RFC 3339 时间' : '比较值'} />}
    <button type="button" className="icon-button" aria-label="移除行条件" onClick={onRemove}><X size={15} /></button>
  </div>
}

function FilterEditor({ group, properties, onChange, depth = 0, onRemove }: { group: FilterGroup; properties: Property[]; onChange: (value: FilterGroup) => void; depth?: number; onRemove?: () => void }) {
  function replace(value: FilterNode) { onChange({ ...group, children: group.children.map((item) => item.id === value.id ? value : item) }) }
  return <div className="admin-filter-group"><div className="admin-inline">
    <select aria-label="行条件组逻辑" value={group.join} onChange={(event) => onChange({ ...group, join: event.target.value as 'and' | 'or' })}><option value="and">满足全部 AND</option><option value="or">满足任意 OR</option></select>
    <label className="admin-check"><input type="checkbox" checked={group.negated} onChange={(event) => onChange({ ...group, negated: event.target.checked })} />取反 NOT</label>
    {onRemove && <button type="button" className="icon-button" aria-label="移除行条件组" onClick={onRemove}><Trash2 size={14} /></button>}</div>
    {!group.children.length && <p className="muted admin-filter-empty">不附加此行条件</p>}
    {group.children.map((child) => child.kind === 'condition' ? <ConditionInput key={child.id} condition={child} properties={properties} onChange={replace} onRemove={() => onChange({ ...group, children: group.children.filter((item) => item.id !== child.id) })} />
      : <FilterEditor key={child.id} group={child} properties={properties} onChange={replace} depth={depth + 1} onRemove={() => onChange({ ...group, children: group.children.filter((item) => item.id !== child.id) })} />)}
    <div className="admin-inline"><button type="button" className="button subtle" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newCondition(properties)] })}><Plus size={14} />添加行条件</button>
      {depth < 3 && <button type="button" className="button subtle" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newGroup()] })}>添加条件组</button>}</div>
  </div>
}

function FieldSelection({ title, fields, properties, onChange }: { title: string; fields: string[]; properties: Property[]; onChange: (fields: string[]) => void }) {
  return <fieldset className="admin-fieldset"><legend>{title}</legend><label className="admin-check"><input type="checkbox" checked={fields.includes('*')} onChange={(event) => onChange(event.target.checked ? ['*'] : [])} />全部字段（含后续新增）</label>
    {!fields.includes('*') && <div className="admin-field-checks">{properties.map((property) => <label className="admin-check" key={property.id}><input type="checkbox" checked={fields.includes(property.id)} onChange={(event) => onChange(event.target.checked ? [...fields, property.id] : fields.filter((id) => id !== property.id))} />{property.name}</label>)}</div>}
    {!fields.length && <small className="muted">尚未授予任何字段。</small>}
  </fieldset>
}

export function RuleDialog({ rule, roles, definitions, categories, onClose, onSave }: {
  rule?: PermissionRule; roles: Role[]; definitions: DefinitionRecord[]; categories: Organization[]; onClose: () => void; onSave: (rule: PermissionRule) => void
}) {
  const [roleId, setRoleId] = useState(rule?.roleId ?? roles[0]?.id ?? '')
  const [effect, setEffect] = useState<'allow' | 'deny'>(rule?.effect ?? 'allow')
  const [operation, setOperation] = useState(rule?.operation ?? 'instance.query')
  const [resourceId, setResourceId] = useState(rule?.resourceId ?? '')
  const [readFields, setReadFields] = useState(rule?.readFields ?? [])
  const [writeFields, setWriteFields] = useState(rule?.writeFields ?? [])
  const [initialFilters] = useState(() => {
    try { return { row: filterFromExpression(rule?.rowFilter), next: filterFromExpression(rule?.newRowFilter), error: '' } }
    catch (reason) { return { row: newGroup(), next: newGroup(), error: errorMessage(reason) } }
  })
  const [rowFilter, setRowFilter] = useState(initialFilters.row)
  const [newRowFilter, setNewRowFilter] = useState(initialFilters.next)
  const [filterError, setFilterError] = useState(initialFilters.error)
  const [error, setError] = useState('')
  const instanceOperation = operation.startsWith('instance.')
  const candidates = operationResources(operation, definitions)
  const selected = candidates.find((item) => item.definition.id === resourceId)
  const properties = selected?.definition.schema?.properties ?? []
  const canCondition = effect === 'allow' && instanceOperation && resourceId !== '*' && properties.length > 0
  const canNewCondition = canCondition && ['instance.create', 'instance.patch', 'instance.upsert', 'instance.upsert.overwrite'].includes(operation)

  function changeResource(value: string) {
    setResourceId(value); setReadFields([]); setWriteFields([]); setRowFilter(newGroup()); setNewRowFilter(newGroup()); setFilterError(''); setError('')
  }
  function save() {
    try {
      if (filterError) throw new Error(filterError)
      if (!roles.some((role) => role.id === roleId)) throw new Error('请选择角色')
      if (!operations.some((item) => item.value === operation)) throw new Error('请选择受支持的操作')
      if (!resourceId || resourceId !== '*' && !selected) throw new Error('请选择当前可见的生效资源')
      if (operation.startsWith('organization.') && resourceId !== '*') throw new Error('分类与分组操作使用租户内全部资源范围')
      const dataAllow = effect === 'allow' && instanceOperation
      const where = canCondition ? compileFilter(rowFilter, properties) : undefined
      const next = canNewCondition ? compileFilter(newRowFilter, properties) : undefined
      const allowedFields = new Set(properties.map((property) => property.id))
      if (dataAllow && resourceId !== '*' && [...readFields, ...writeFields].some((field) => field !== '*' && !allowedFields.has(field))) throw new Error('字段已失效，请重新选择')
      onSave({ id: rule?.id ?? crypto.randomUUID(), roleId, effect, operation, resourceId,
        readFields: dataAllow ? resourceId === '*' ? readFields.includes('*') ? ['*'] : [] : readFields : [],
        writeFields: dataAllow ? resourceId === '*' ? writeFields.includes('*') ? ['*'] : [] : writeFields : [],
        ...(where ? { rowFilter: where } : {}), ...(next ? { newRowFilter: next } : {}) })
    } catch (reason) { setError(errorMessage(reason)) }
  }

  return <Dialog title={rule ? '编辑权限规则' : '添加权限规则'} description="选择角色、操作与资源；保存到本地规则后，再保存并生效策略。" onClose={onClose} wide>
    <form onSubmit={(event) => { event.preventDefault(); save() }} className="admin-form">
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {filterError && <div className="notice notice-error" role="alert">{filterError}<button type="button" className="button" onClick={() => { setRowFilter(newGroup()); setNewRowFilter(newGroup()); setFilterError('') }}>清空行条件并重新配置</button></div>}
      <div className="form-grid"><label className="field">授权角色<select aria-label="授权角色" value={roleId} onChange={(event) => setRoleId(event.target.value)}><option value="">请选择角色</option>{roles.map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}</select></label>
        <label className="field">规则效果<select aria-label="规则效果" value={effect} onChange={(event) => setEffect(event.target.value as 'allow' | 'deny')}><option value="allow">允许操作</option><option value="deny">拒绝整个操作</option></select></label>
        <label className="field">业务操作<select aria-label="业务操作" value={operation} onChange={(event) => { setOperation(event.target.value); changeResource(event.target.value.startsWith('organization.') ? '*' : '') }}>
          {operations.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></label>
        <label className="field">资源范围<select aria-label="资源范围" value={resourceId} onChange={(event) => changeResource(event.target.value)}><option value="">请选择资源</option><option value="*">租户内全部资源</option>
          {!operation.startsWith('organization.') && candidates.map((item) => <option key={item.definition.id} value={item.definition.id}>{resourceName(item.definition.id, definitions, categories)}</option>)}
        </select></label></div>
      {effect === 'deny' ? <div className="notice">拒绝规则作用于整个操作，不附加字段或行条件。</div>
        : !instanceOperation ? <div className="notice">{operation.endsWith('.invoke') ? '调用按整个操作授权，不支持字段或行条件。运行动作还需拥有其绑定函数的运行权限。' : operation.startsWith('link.') ? '关系按整个操作授权；查询和维护关系时仍独立检查两端对象的数据权限。' : '定义及组织管理按整个操作授权，无需选择字段或行条件。'}</div>
          : <><div className="form-grid"><FieldSelection title="可读字段" fields={readFields} properties={resourceId === '*' ? [] : properties} onChange={setReadFields} /><FieldSelection title="可写字段" fields={writeFields} properties={resourceId === '*' ? [] : properties.filter((property) => !property.readOnly)} onChange={setWriteFields} /></div>
            {resourceId === '*' && <p className="muted">全部资源范围仅支持“全部字段”或“无字段”，不支持行条件。</p>}
            {canCondition && <><section><h3>读取 / 原行条件</h3><p className="muted admin-help">先在服务端限制可读取、可修改或可删除的原行。</p><FilterEditor group={rowFilter} properties={properties} onChange={setRowFilter} /></section>
              {canNewCondition && <section><h3>创建 / 修改后的新行条件</h3><p className="muted admin-help">留空时沿用原行条件；限制创建或修改后的值。</p><FilterEditor group={newRowFilter} properties={properties} onChange={setNewRowFilter} /></section>}</>}
          </>}
      <div className="admin-footer"><button type="button" className="button" onClick={onClose}>取消</button><button type="submit" className="button primary"><Save size={16} />保存规则到编辑区</button></div>
    </form>
  </Dialog>
}
