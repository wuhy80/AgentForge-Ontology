import type { Definition, DefinitionRecord, Kind, Property } from '../../lib/types'
import { newProperty } from './PropertyEditor'

export const MAX_SCHEMA_PROPERTIES = 100
export const MAX_SCHEMA_INDEXES = 32

export const kindLabels: Record<Kind, string> = {
  schema: '对象模型', enum: '枚举', link: '关系', action: '动作', function: '函数',
}

export const kindDescriptions: Record<Kind, string> = {
  schema: '用业务属性描述对象，管理数据结构与索引。',
  enum: '维护稳定的业务编码与用户可见标签。',
  link: '通过正反方向连接已经生效的对象模型。',
  action: '定义动作的输入、输出和副作用边界。',
  function: '登记函数签名、产物引用与副作用声明。',
}

export function newDefinition(kind: Kind, categoryId = '', groupId = ''): Definition {
  const definition: Definition = { id: '', tenantId: '', categoryId, groupId, kind, name: '', description: '' }
  if (kind === 'schema') definition.schema = { mode: 'schema_2_db', storageLayout: 'single', properties: [newProperty()], indexes: [] }
  if (kind === 'enum') definition.enum = { values: [{ code: '', label: '', deprecated: false }] }
  if (kind === 'link') definition.link = { sourceSchemaId: '', targetSchemaId: '', forwardName: '', reverseName: '', cardinality: '1:N', onDelete: 'restrict' }
  if (kind === 'action') definition.action = { input: [], output: [], sideEffects: 'dataWrite' }
  if (kind === 'function') definition.function = { input: [], output: [], sideEffects: 'none' }
  return definition
}

function validateProperties(properties: Property[], title: string): string | undefined {
  const names = new Set<string>()
  for (const property of properties) {
    const name = property.name.normalize('NFC').trim().toLocaleLowerCase()
    if (!name) return `请填写${title}的业务名称。`
    if (names.has(name)) return `${title}中存在重复的业务名称。`
    names.add(name)
    if (property.dataType === 'decimal' && (!Number.isInteger(property.precision) || !Number.isInteger(property.scale ?? 0) || property.precision! < 1 || property.precision! > 38 || (property.scale ?? 0) < 0 || (property.scale ?? 0) > property.precision!)) return '小数总位数须为 1–38，小数位须在 0 与总位数之间。'
  }
}

export function validateDefinition(definition: Definition): string | undefined {
  if (!definition.name.trim()) return '请填写业务名称。'
  if (!definition.categoryId || !definition.groupId) return '请选择分类和分组。'
  if (definition.schema) {
    if (definition.schema.properties.length === 0) return '对象模型至少需要一个属性。'
    if (definition.schema.properties.length > MAX_SCHEMA_PROPERTIES) return `对象模型最多支持 ${MAX_SCHEMA_PROPERTIES} 个属性。`
    if ((definition.schema.indexes?.length ?? 0) > MAX_SCHEMA_INDEXES) return `对象模型最多支持 ${MAX_SCHEMA_INDEXES} 个索引。`
    if (definition.schema.properties.some((property) => property.enumId)) return '当前对象属性暂不支持绑定枚举，请先解除枚举绑定。'
    const propertyError = validateProperties(definition.schema.properties, '属性')
    if (propertyError) return propertyError
    const names = new Set<string>()
    for (const index of definition.schema.indexes ?? []) {
      if (!index.name.trim()) return '请填写索引名称。'
      const name = index.name.trim().normalize('NFC').toLocaleLowerCase()
      if (names.has(name)) return '索引名称不能重复。'
      names.add(name)
      if (index.fieldIds.length < 1 || index.fieldIds.length > 2 || new Set(index.fieldIds).size !== index.fieldIds.length || index.fieldIds.some((id) => !definition.schema!.properties.some((property) => property.id && property.id === id))) return '每个索引请选择 1–2 个不同的已保存属性。'
    }
  }
  if (definition.enum) {
    if (definition.enum.values.length === 0) return '枚举至少需要一个业务值。'
    const codes = new Set<string>()
    for (const value of definition.enum.values) {
      if (!value.code || value.code.trim() !== value.code || value.code.length > 128) return '枚举编码须为 1–128 个字符，首尾不能有空格。'
      if (codes.has(value.code)) return '枚举业务编码不能重复。'
      codes.add(value.code)
      if (!value.label.trim()) return '请填写每个枚举值的显示标签。'
    }
  }
  if (definition.link && (!definition.link.sourceSchemaId || !definition.link.targetSchemaId)) return '请选择关系两端的已生效对象模型。'
  for (const callable of [definition.action, definition.function]) {
    if (!callable) continue
    const error = validateProperties(callable.input ?? [], '输入参数') ?? validateProperties(callable.output ?? [], '输出参数')
    if (error) return error
  }
}

export function activeEntries(entries: DefinitionRecord[], kind: Kind): DefinitionRecord[] {
  return entries.filter((entry) => entry.definition.kind === kind && (entry.status === 'active' || entry.hasCurrent))
}

export function cloneDefinition(definition: Definition): Definition {
  return structuredClone(definition)
}
