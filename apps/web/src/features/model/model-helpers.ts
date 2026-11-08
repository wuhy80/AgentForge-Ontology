import type { Definition, DefinitionRecord, Kind, Property } from '../../lib/types'
import { newProperty } from './PropertyEditor'
import { languageLabels, validEntrypoint } from '../../lib/runtime-language'
import { draftText, parseFieldValue } from '../query/query'

export const MAX_SCHEMA_PROPERTIES = 100
export const MAX_SCHEMA_INDEXES = 32

export const kindLabels: Record<Kind, string> = {
  schema: '对象模型', enum: '枚举', link: '关系', action: '动作', function: '函数',
}

export const kindDescriptions: Record<Kind, string> = {
  schema: '用业务属性描述对象，管理数据结构与索引。',
  enum: '维护稳定的业务编码与用户可见标签。',
  link: '通过正反方向连接已经生效的对象模型。',
  action: '编写动作的独立 Python、JavaScript 或 Go 源码，或绑定已发布的函数。',
  function: '维护函数签名，编写 Python、JavaScript 或 Go 计算或有明确范围的事务写入。',
}

export function newDefinition(kind: Kind, categoryId = '', groupId = ''): Definition {
  const definition: Definition = { id: '', tenantId: '', categoryId, groupId, kind, name: '', description: '' }
  if (kind === 'schema') definition.schema = { mode: 'schema_2_db', storageLayout: 'single', properties: [newProperty()], indexes: [] }
  if (kind === 'enum') definition.enum = { values: [{ code: '', label: '', deprecated: false }] }
  if (kind === 'link') definition.link = { sourceSchemaId: '', targetSchemaId: '', forwardName: '', reverseName: '', cardinality: '1:N', onDelete: 'restrict' }
  if (kind === 'action') definition.action = { input: [], output: [], sideEffects: 'none' }
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
    if (property.enumId && property.dataType !== 'string') return '枚举绑定只支持短文本属性。'
    if (property.default !== undefined) {
      if (property.default === null) {
        if (!property.nullable) return `${property.name}：非空属性不能使用 NULL 默认值。`
      } else {
        if (typeof property.default !== (property.dataType === 'boolean' ? 'boolean' : 'string')) return `${property.name}：默认值类型不合法。`
        try { parseFieldValue(property, draftText(property.default)) } catch (cause) { return cause instanceof Error ? cause.message : '默认值不合法。' }
        if (property.enumId && property.enumValues && !property.enumValues.some((value) => value.code === property.default && !value.deprecated)) return `${property.name}：请选择有效的枚举默认值。`
      }
    }
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
    if (callable.sideEffects !== 'dataWrite' && callable.dataWrite?.length) return '仅写入业务数据的定义允许声明写入范围。'
    if (callable.sideEffects === 'dataWrite') {
      if (!callable.dataWrite?.length || callable.dataWrite.length > 32) return '业务写入须声明 1–32 个对象及允许操作。'
      const schemas = new Set<string>()
      for (const item of callable.dataWrite) {
        if (!item.schemaId.trim() || item.schemaId.trim() !== item.schemaId || schemas.has(item.schemaId.toLowerCase())) return '写入对象不能为空或重复。'
        schemas.add(item.schemaId.toLowerCase())
        if (!item.operations.length || item.operations.length > 4 || new Set(item.operations).size !== item.operations.length || item.operations.some((name) => !['create', 'update', 'delete', 'upsert'].includes(name))) return '每个写入对象须选择不重复的创建、更新、删除或创建更新操作。'
      }
    }
    if (callable.runtime) {
      if (callable.sideEffects === 'externalWrite') return '源码尚不支持修改外部系统。'
      if (callable.artifactRef) return '源码与产物引用不能同时配置。'
      if (callable.functionId) return '独立源码与绑定函数不能同时配置。'
      if (!['python', 'javascript', 'go'].includes(callable.runtime.language)) return '当前仅支持 Python、JavaScript、Go 运行器。'
      if (!callable.runtime.source.trim() || callable.runtime.source.includes('\0') || new TextEncoder().encode(callable.runtime.source).length > 65536) return '请填写源码，最多 64 KiB，不能包含空字符。'
      if (!validEntrypoint(callable.runtime.language, callable.runtime.entrypoint)) return `${languageLabels[callable.runtime.language]} 入口须为普通函数名，最多 64 个字母、数字或下划线，不使用关键字、保留入口或双下划线前缀。`
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
