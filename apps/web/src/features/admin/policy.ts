import type { DefinitionRecord, Expression, Organization, PermissionRule, Property } from '../../lib/types'
import { newGroup } from '../query/query'
import type { FilterGroup, FilterNode } from '../query/query'

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

export function expressionSummary(expression: Expression | null | undefined, properties: Property[]): string {
  if (!expression) return '不附加行条件'
  if (expression.op === 'not') return `不满足（${expressionSummary(expression.args?.[0], properties)}）`
  if (expression.op === 'and' || expression.op === 'or') return (expression.args ?? []).map((item) => `（${expressionSummary(item, properties)}）`).join(expression.op === 'and' ? ' 且 ' : ' 或 ')
  const field = properties.find((property) => property.id === expression.field)?.name ?? '不可见字段'
  const labels: Record<string, string> = { eq: '等于', ne: '不等于', gt: '大于', gte: '大于等于', lt: '小于', lte: '小于等于', startsWith: '开头是', in: '属于', notIn: '不属于', isNull: '为空', isNotNull: '不为空' }
  const value = expression.value === undefined ? '' : Array.isArray(expression.value) ? expression.value.map(String).join('、') : typeof expression.value === 'boolean' ? expression.value ? '是' : '否' : String(expression.value)
  return `${field} ${labels[expression.op] ?? expression.op}${value ? ` ${value}` : ''}`
}

export function fieldsSummary(fields: string[] | null | undefined, properties: Property[]): string {
  if (fields?.includes('*')) return '全部字段'
  if (!fields?.length) return '无字段'
  return fields.map((id) => properties.find((property) => property.id === id)?.name ?? '不可见字段').join('、')
}

export function filterFromExpression(expression?: Expression | null): FilterGroup {
  if (!expression) return newGroup()
  function visit(item: Expression): FilterNode {
    if (item.op === 'and' || item.op === 'or') return { ...newGroup(), join: item.op, children: (item.args ?? []).map(visit) }
    if (item.op === 'not') {
      if (item.args?.length !== 1) throw new Error('此行条件结构不受支持')
      const nested = visit(item.args[0])
      return nested.kind === 'group' ? { ...nested, negated: !nested.negated } : { ...newGroup(), negated: true, children: [nested] }
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
