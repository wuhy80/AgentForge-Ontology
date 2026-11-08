import type { DefinitionRecord, Organization, PermissionValueRef, Property } from '../../lib/types'
import { compatibleContextField, contextMatchFields, contextSchemas, resourceName, userAttributeOptions } from './policy'

export function adjustContextReference(ref: PermissionValueRef | undefined, property: Property | undefined, op: string, definitions: DefinitionRecord[]): PermissionValueRef | undefined {
  if (!ref || op === 'isNull' || op === 'isNotNull') return undefined
  const set = op === 'in' || op === 'notIn'
  if (ref.source === 'user') {
    const options = userAttributeOptions(property, set)
    return { source: 'user', attribute: options.find((item) => item.value === ref.attribute)?.value ?? options[0]?.value }
  }
  const source = contextSchemas(definitions).find((item) => item.definition.id === ref.schemaId)
  const field = source?.definition.schema?.properties.find((item) => item.id === ref.fieldId)
  const match = contextMatchFields(source, set, ref.matchUserAttribute ?? 'id').find((item) => item.id === ref.matchFieldId)
  return { source: 'object', schemaId: ref.schemaId, fieldId: field && compatibleContextField(property, field) ? field.id : '',
    ...(set || ref.instanceId === undefined ? { matchFieldId: match?.id ?? '', matchUserAttribute: ref.matchUserAttribute ?? 'id' } : { instanceId: ref.instanceId }), ...(set ? { mode: 'set' } : {}) }
}

export function ContextValueInput({ value, property, op, definitions, categories, onChange }: {
  value: PermissionValueRef; property: Property | undefined; op: string; definitions: DefinitionRecord[]; categories: Organization[]; onChange: (value: PermissionValueRef) => void
}) {
  const set = op === 'in' || op === 'notIn'
  if (value.source === 'user') {
    const options = userAttributeOptions(property, set)
    return <select aria-label="当前用户信息" value={value.attribute ?? ''} onChange={(event) => onChange({ source: 'user', attribute: event.target.value as PermissionValueRef['attribute'] })}>
      <option value="">选择当前用户信息</option>
      {value.attribute && !options.some((item) => item.value === value.attribute) && <option value={value.attribute}>类型不匹配的用户信息，请重新选择</option>}
      {options.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
    </select>
  }
  const sources = contextSchemas(definitions)
  const source = sources.find((item) => item.definition.id === value.schemaId)
  const fields = source?.definition.schema?.properties.filter((item) => compatibleContextField(property, item)) ?? []
  const matchFields = contextMatchFields(source, set, value.matchUserAttribute ?? 'id')
  const fixed = Boolean(value.instanceId !== undefined)
  function binding(mode: 'instance' | 'user', attribute: 'id' | 'username' = 'id') {
    return { source: 'object' as const, schemaId: value.schemaId, fieldId: value.fieldId,
      ...(mode === 'instance' ? { instanceId: '' } : { matchFieldId: '', matchUserAttribute: attribute }), ...(set ? { mode: 'set' as const } : {}) }
  }
  return <div className="admin-context-input">
    <select aria-label="取值对象模型" value={value.schemaId ?? ''} onChange={(event) => onChange({ source: 'object', schemaId: event.target.value, fieldId: '', ...(set ? { mode: 'set', matchFieldId: '', matchUserAttribute: 'id' } : { instanceId: '' }) })}>
      <option value="">选择取值对象模型</option>
      {value.schemaId && !source && <option value={value.schemaId}>取值模型不可用，请重新选择</option>}
      {sources.map((item) => <option key={item.definition.id} value={item.definition.id}>{resourceName(item.definition.id, sources, categories)}</option>)}
    </select>
    <select aria-label="取值对象属性" value={value.fieldId ?? ''} onChange={(event) => onChange({ ...value, fieldId: event.target.value })}>
      <option value="">选择同类型属性</option>
      {value.fieldId && !fields.some((item) => item.id === value.fieldId) && <option value={value.fieldId}>属性不可用或类型不匹配</option>}
      {fields.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select>
    <select aria-label="对象取值方式" value={fixed ? 'instance' : value.matchUserAttribute === 'username' ? 'username' : 'id'} onChange={(event) => onChange(event.target.value === 'instance' ? binding('instance') : binding('user', event.target.value as 'id' | 'username'))}>
      {!set && <option value="instance">指定实例 ID</option>}
      <option value="id">按当前用户 ID 匹配</option><option value="username">按当前用户账号匹配</option>
    </select>
    {fixed ? <input aria-label="取值实例 ID" value={value.instanceId ?? ''} onChange={(event) => onChange({ ...value, instanceId: event.target.value })} placeholder="完整实例 ID" />
      : <select aria-label="用户匹配属性" value={value.matchFieldId ?? ''} onChange={(event) => onChange({ ...value, matchFieldId: event.target.value })}>
        <option value="">{set ? '选择用户匹配属性' : '选择单字段唯一属性'}</option>
        {value.matchFieldId && !matchFields.some((item) => item.id === value.matchFieldId) && <option value={value.matchFieldId}>匹配属性不可用，请重新选择</option>}
        {matchFields.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select>}
    <small className="muted">{set ? '读取匹配对象的属性集合，最多 100 条；空集拒绝本次请求。' : '取值时须唯一匹配一个对象。'}该属性仅用于判权；请单独控制其查看与修改权限。</small>
  </div>
}
