import type { DefinitionRecord, Expression, Organization, PermissionRule, PermissionValueRef, Property } from '../../lib/types'
import { availableOperators, compileFilter, newGroup } from '../query/query'
import type { FilterCondition } from '../query/query'

export type PermissionFilterCondition = FilterCondition & { valueRef?: PermissionValueRef }
export type PermissionFilterGroup = { id: string; kind: 'group'; join: 'and' | 'or'; negated: boolean; children: PermissionFilterNode[] }
export type PermissionFilterNode = PermissionFilterCondition | PermissionFilterGroup

export const userAttributes = [
  { value: 'id', label: '用户 ID', identifiers: true, set: false },
  { value: 'username', label: '账号', identifiers: false, set: false },
  { value: 'displayName', label: '显示名称', identifiers: false, set: false },
  { value: 'tenantId', label: '租户 ID', identifiers: false, set: false },
  { value: 'roleIds', label: '有效角色 ID 集合', identifiers: true, set: true },
  { value: 'userGroupIds', label: '启用用户组 ID 集合', identifiers: true, set: true },
] as const

export function userAttributeOptions(property: Property | undefined, set: boolean) {
  return userAttributes.filter((attribute) => attribute.set === set && (property?.dataType === 'string' || property?.dataType === 'text' || property?.dataType === 'longtext' || attribute.identifiers && property?.dataType === 'uuid'))
}

export function contextSchemas(definitions: DefinitionRecord[]) {
  return definitions.filter(({ status, definition }) => status === 'active' && definition.kind === 'schema' && definition.schema && !definition.schema.storageBinding)
}

export function compatibleContextField(target: Property | undefined, source: Property) {
  return target?.dataType === source.dataType && (target.dataType !== 'decimal' || target.precision === source.precision && target.scale === source.scale)
}

export function contextMatchFields(definition: DefinitionRecord | undefined, set: boolean, attribute: 'id' | 'username') {
  const schema = definition?.definition.schema
  return schema?.properties.filter((field) => (field.dataType === 'string' || field.dataType === 'text' || field.dataType === 'longtext' || attribute === 'id' && field.dataType === 'uuid') && (set || schema.indexes?.some((index) => index.unique && index.fieldIds.length === 1 && index.fieldIds[0] === field.id))) ?? []
}

function validateReferenceShape(ref: PermissionValueRef, op: string) {
  const keys = new Set(['source', 'attribute', 'schemaId', 'fieldId', 'instanceId', 'matchFieldId', 'matchUserAttribute', 'mode'])
  if (!ref || typeof ref !== 'object' || Object.keys(ref).some((key) => !keys.has(key))) throw new Error('此上下文引用结构不受支持，原规则已保留')
  if (op === 'isNull' || op === 'isNotNull') throw new Error('空值判断不能引用上下文')
  const set = op === 'in' || op === 'notIn'
  if (ref.source === 'user') {
    const attribute = userAttributes.find((item) => item.value === ref.attribute)
    if (!attribute || attribute.set !== set || ref.schemaId || ref.fieldId || ref.instanceId || ref.matchFieldId || ref.matchUserAttribute || ref.mode) throw new Error('此用户上下文引用不受支持，原规则已保留')
    return
  }
  if (ref.source !== 'object' || ref.attribute || !ref.schemaId || !ref.fieldId || !['', 'scalar', 'set'].includes(ref.mode ?? '') || (ref.mode === 'set') !== set) throw new Error('此对象上下文引用不受支持，原规则已保留')
  const fixed = Boolean(ref.instanceId), matched = Boolean(ref.matchFieldId && ref.matchUserAttribute)
  if (fixed === matched || fixed && (set || ref.matchFieldId || ref.matchUserAttribute) || matched && !['id', 'username'].includes(ref.matchUserAttribute!)) throw new Error('对象取值须选择指定实例或按当前用户匹配，不能混用')
}

export function validateContextReference(ref: PermissionValueRef, op: string, target: Property, definitions: DefinitionRecord[]) {
  validateReferenceShape(ref, op)
  const set = op === 'in' || op === 'notIn'
  if (ref.source === 'user') {
    if (!userAttributeOptions(target, set).some((item) => item.value === ref.attribute)) throw new Error(`${target.name} 与所选用户信息类型不匹配`)
    return
  }
  const source = contextSchemas(definitions).find((item) => item.definition.id === ref.schemaId)
  const field = source?.definition.schema?.properties.find((item) => item.id === ref.fieldId)
  if (!source || !field || !compatibleContextField(target, field)) throw new Error('取值模型或属性不可用，或与条件字段类型不匹配')
  if (ref.instanceId && !/^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(ref.instanceId)) throw new Error('指定实例须填写完整的实例 ID')
  if (!ref.instanceId && !contextMatchFields(source, set, ref.matchUserAttribute!).some((item) => item.id === ref.matchFieldId)) throw new Error(set ? '请选择与当前用户类型匹配的取值字段' : '单值取值须按具有单字段唯一索引的属性匹配用户')
}

export function compilePermissionFilter(group: PermissionFilterGroup, properties: Property[], definitions: DefinitionRecord[]): Expression | undefined {
  let count = 0
  function visit(node: PermissionFilterNode, depth: number, root = false): Expression | undefined {
    count += node.kind === 'group' && node.negated ? 2 : 1
    if (count > 90 || depth > 8) throw new Error('行条件过多或嵌套过深，请简化条件')
    if (node.kind === 'group') {
      if (!node.children.length) {
        if (root && !node.negated) return undefined
        throw new Error('条件组不能为空，请添加条件或移除空组')
      }
      const expression: Expression = { op: node.join, args: node.children.map((child) => visit(child, depth + (node.negated ? 2 : 1))!) }
      return node.negated ? { op: 'not', args: [expression] } : expression
    }
    const property = properties.find((item) => item.id === node.field)
    if (!property || !availableOperators(property).some((item) => item.value === node.op)) throw new Error('行条件字段或操作符已失效，请重新选择')
    if (node.valueRef) {
      validateContextReference(node.valueRef, node.op, property, definitions)
      return { op: node.op, field: property.id, valueRef: { ...node.valueRef } }
    }
    return compileFilter({ ...newGroup(), children: [node] }, properties)!.args![0]
  }
  return visit(group, 0, true)
}

export const operations = [
  { value: 'organization.read', label: '查看分类与分组' },
  { value: 'organization.edit', label: '管理分类与分组' },
  { value: 'definition.read', label: '查看定义' },
  { value: 'definition.edit', label: '编辑定义' },
  { value: 'definition.publish', label: '发布定义' },
  { value: 'instance.read', label: '读取实例' },
  { value: 'instance.query', label: '查询实例' },
  { value: 'instance.create', label: '创建实例' },
  { value: 'instance.patch', label: '修改实例' },
  { value: 'instance.delete', label: '删除实例' },
  { value: 'instance.upsert', label: '创建或更新实例' },
  { value: 'instance.upsert.overwrite', label: '同步覆盖实例' },
  { value: 'link.query', label: '查询关联数据' },
  { value: 'link.create', label: '添加对象关系' },
  { value: 'link.delete', label: '移除对象关系' },
  { value: 'function.invoke', label: '运行函数' },
  { value: 'action.invoke', label: '运行动作' },
]

export function operationResources(operation: string, definitions: DefinitionRecord[]): DefinitionRecord[] {
  const kind = operation.startsWith('instance.') ? 'schema' : operation.startsWith('link.') ? 'link'
    : operation === 'function.invoke' ? 'function' : operation === 'action.invoke' ? 'action' : undefined
  return definitions.filter((item) => !kind || item.definition.kind === kind)
}

export function operationName(value: string) { return operations.find((item) => item.value === value)?.label ?? value }
export function resourceName(id: string, definitions: DefinitionRecord[], categories: Organization[]) {
  if (id === '*') return '租户内全部资源'
  const definition = definitions.find((item) => item.definition.id === id)?.definition
  if (!definition) return '目录中不可见的资源'
  const category = categories.find((item) => item.id === definition.categoryId)
  return `${category ? `${category.name} / ` : ''}${definition.name}`
}

export function expressionSummary(expression: Expression | null | undefined, properties: Property[], definitions: DefinitionRecord[] = []): string {
  if (!expression) return '不附加行条件'
  if (expression.op === 'not') return `不满足（${expressionSummary(expression.args?.[0], properties, definitions)}）`
  if (expression.op === 'and' || expression.op === 'or') return (expression.args ?? []).map((item) => `（${expressionSummary(item, properties, definitions)}）`).join(expression.op === 'and' ? ' 且 ' : ' 或 ')
  const field = properties.find((property) => property.id === expression.field)?.name ?? '不可见字段'
  const labels: Record<string, string> = { eq: '等于', ne: '不等于', gt: '大于', gte: '大于等于', lt: '小于', lte: '小于等于', startsWith: '开头是', in: '属于', notIn: '不属于', isNull: '为空', isNotNull: '不为空' }
  const ref = expression.valueRef
  let value = expression.value === undefined ? '' : Array.isArray(expression.value) ? expression.value.map(String).join('、') : typeof expression.value === 'boolean' ? expression.value ? '是' : '否' : String(expression.value)
  if (ref?.source === 'user') value = `当前用户 · ${userAttributes.find((item) => item.value === ref.attribute)?.label ?? '不支持的信息'}`
  else if (ref?.source === 'object') {
    const source = definitions.find((item) => item.definition.id === ref.schemaId)?.definition
    const attribute = source?.schema?.properties.find((item) => item.id === ref.fieldId)?.name ?? '不可见属性'
    const matchField = source?.schema?.properties.find((item) => item.id === ref.matchFieldId)?.name ?? '不可见匹配字段'
    value = `${source?.name ?? '不可见模型'} · ${attribute}（${ref.instanceId ? '指定实例' : `${matchField}匹配当前用户${ref.matchUserAttribute === 'username' ? '账号' : ' ID'}`}）${ref.mode === 'set' ? '集合' : ''}`
  } else if (ref) value = '不支持的上下文引用'
  return `${field} ${labels[expression.op] ?? expression.op}${value ? ` ${value}` : ''}`
}

export function fieldsSummary(fields: string[] | null | undefined, properties: Property[]): string {
  if (fields?.includes('*')) return '全部字段'
  if (!fields?.length) return '无字段'
  return fields.map((id) => properties.find((property) => property.id === id)?.name ?? '不可见字段').join('、')
}

export function filterFromExpression(expression?: Expression | null): PermissionFilterGroup {
  if (!expression) return newGroup()
  let count = 0
  function visit(item: Expression, depth = 0): PermissionFilterNode {
    if (++count > 100 || depth > 10) throw new Error('此行条件超过编辑预算，原规则已保留')
    if (item.op === 'and' || item.op === 'or') {
      if (!item.args?.length || item.field || item.value !== undefined || item.valueRef) throw new Error('此行条件结构不受支持，原规则已保留')
      return { ...newGroup(), join: item.op, children: item.args.map((child) => visit(child, depth + 1)) }
    }
    if (item.op === 'not') {
      if (item.args?.length !== 1 || item.field || item.value !== undefined || item.valueRef) throw new Error('此行条件结构不受支持')
      const nested = visit(item.args[0], depth + 1)
      return nested.kind === 'group' ? { ...nested, negated: !nested.negated } : { ...newGroup(), negated: true, children: [nested] }
    }
    if (item.args?.length || !item.field) throw new Error('此行条件结构不受支持，原规则已保留')
    if (item.valueRef) {
      if (item.value !== undefined) throw new Error('上下文引用与固定值不能同时存在，原规则已保留')
      validateReferenceShape(item.valueRef, item.op)
      return { id: crypto.randomUUID(), kind: 'condition', field: item.field, op: item.op, raw: '', valueRef: { ...item.valueRef } }
    }
    const value = item.value
    if (Array.isArray(value) && value.some((entry) => typeof entry === 'string' && /[\r\n]/.test(entry))) throw new Error('集合中包含换行文本，当前可视化编辑器无法完整表示。原规则已保留，请取消编辑，或清空行条件后重新配置。')
    return { id: crypto.randomUUID(), kind: 'condition', field: item.field ?? '', op: item.op, raw: Array.isArray(value) ? value.map(String).join('\n') : value === undefined || value === null ? '' : String(value) }
  }
  const node = visit(expression)
  return node.kind === 'group' ? node : { ...newGroup(), children: [node] }
}

export function canonicalPolicy(rules: PermissionRule[]): string {
  return JSON.stringify(rules)
}
