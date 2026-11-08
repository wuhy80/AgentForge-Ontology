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
  action: '编写动作的独立 Python 源码，或绑定已发布的函数。',
  function: '维护函数签名，编写和运行无副作用的 Python 计算。',
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
    const schema = definition.schema
    const binding = schema.storageBinding
    if (schema.mode !== 'schema_2_db' && schema.mode !== 'db_2_schema') return '请选择已支持的建模方向。'
    if (schema.mode === 'db_2_schema' && !binding) return '数据库为准的模型必须绑定数据源与主表。'
    if (binding) {
      if (!binding.dataSourceId || !binding.table.trim() || !binding.idColumn?.trim()) return '请完整配置主数据源、主表名称和对象主键列。'
      const sources = new Set([`${binding.dataSourceId}/${binding.namespace ?? ''}/${binding.table}`])
      for (const property of schema.properties) {
        const mapping = property.mapping
        if (!mapping?.dataSourceId || !mapping.column.trim()) return `请配置属性“${property.name || '未命名属性'}”的数据源与映射列。`
        const sameSource = mapping.dataSourceId === binding.dataSourceId
        const sameTable = sameSource && (!mapping.table || mapping.table === binding.table) && (!mapping.namespace || mapping.namespace === binding.namespace)
        if (!sameTable) {
          if (!mapping.table?.trim() || !mapping.keyColumn?.trim()) return `属性“${property.name || '未命名属性'}”的其他来源须填写来源表与唯一关联键列。`
          if (!property.nullable) return `跨来源属性“${property.name || '未命名属性'}”必须允许空值。`
        }
        sources.add(`${mapping.dataSourceId}/${mapping.namespace ?? (sameSource ? binding.namespace ?? '' : '')}/${mapping.table || binding.table}`)
      }
      if (sources.size > 8) return '一个对象最多绑定 8 张来源表。'
    } else if (schema.properties.some((property) => property.mapping)) return '属性映射需要先配置对象的主数据源与主表。'
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
      if (index.fieldIds.some((id) => definition.schema!.properties.some((property) => property.id === id && (property.dataType === 'text' || property.dataType === 'longtext')))) return '长文本和超长文本属性不支持索引。'
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
  if (definition.action?.targetSchemaIds && (definition.action.targetSchemaIds.length > 32 || new Set(definition.action.targetSchemaIds).size !== definition.action.targetSchemaIds.length || definition.action.targetSchemaIds.some((id) => !id.trim() || id.trim() !== id))) return '关联对象最多选择 32 个，且不能重复或为空。'
  for (const callable of [definition.action, definition.function]) {
    if (!callable) continue
    const error = validateProperties(callable.input ?? [], '输入参数') ?? validateProperties(callable.output ?? [], '输出参数')
    if (error) return error
    if (callable.functionId && callable.artifactRef) return '实现函数与产物引用不能同时配置。'
    if (callable.runtime) {
      if (callable.sideEffects !== 'none') return 'Python 实现仅支持无副作用计算。'
      if (callable.artifactRef) return 'Python 源码与产物引用不能同时配置。'
      if (callable.functionId) return '独立 Python 源码与绑定函数不能同时配置。'
      if (callable.runtime.language !== 'python') return '当前仅支持 Python 运行器。'
      if (!callable.runtime.source.trim() || new TextEncoder().encode(callable.runtime.source).length > 65536) return '请填写 Python 源码，最多 64 KiB。'
      if (!/^(?!__)[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(callable.runtime.entrypoint) || ['False', 'None', 'True', 'and', 'as', 'assert', 'async', 'await', 'break', 'class', 'continue', 'def', 'del', 'elif', 'else', 'except', 'finally', 'for', 'from', 'global', 'if', 'import', 'in', 'is', 'lambda', 'nonlocal', 'not', 'or', 'pass', 'raise', 'return', 'try', 'while', 'with', 'yield'].includes(callable.runtime.entrypoint)) return 'Python 入口须为普通函数名，最多 64 个字母、数字或下划线，不使用关键字或双下划线前缀。'
      if (!Number.isInteger(callable.runtime.timeoutSeconds) || callable.runtime.timeoutSeconds < 1 || callable.runtime.timeoutSeconds > 30) return '执行超时须为 1–30 秒的整数。'
    }
  }
}

export function activeEntries(entries: DefinitionRecord[], kind: Kind): DefinitionRecord[] {
  return entries.filter((entry) => entry.definition.kind === kind && (entry.status === 'active' || entry.hasCurrent))
}

export function cloneDefinition(definition: Definition): Definition {
  return structuredClone(definition)
}
