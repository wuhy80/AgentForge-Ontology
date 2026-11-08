import { describe, expect, it } from 'vitest'
import type { DefinitionRecord, Kind, Organization, Property } from '../../lib/types'
import { buildOntologyGraph, collectDefinitionReferences } from './ontology-graph'

const categories: Organization[] = [{ id: 'knowledge', tenantId: 'tenant', name: '知识库' }, { id: 'shared', tenantId: 'tenant', name: '公共定义' }]
function record(id: string, kind: Kind = 'schema', categoryId = 'knowledge', tenantId = 'tenant'): DefinitionRecord {
  return { etag: `${id}-current`, status: 'active', definition: { id, tenantId, categoryId, groupId: 'group', kind, name: id,
    ...(kind === 'schema' ? { schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } }
      : kind === 'enum' ? { enum: { values: [{ code: 'active', label: '有效' }] } }
        : kind === 'link' ? { link: { sourceSchemaId: 'note', targetSchemaId: 'tag', forwardName: '属于标签', reverseName: '包含笔记', cardinality: 'N:M', onDelete: 'restrict' } }
          : { [kind]: { sideEffects: 'none', input: [], output: [] } }),
  } }
}
const enumProperty = (id: string, enumId: string): Property => ({ id, name: id, dataType: 'string', enumId })
const build = (records: DefinitionRecord[], extra: Partial<Parameters<typeof buildOntologyGraph>[0]> = {}) => buildOntologyGraph({ records, categories, categoryId: 'knowledge', mode: 'dependencies', ...extra })

describe('本体全景稳定引用', () => {
  it('只提取Schema属性、动作对象/函数和输入输出的真实ID，保留参数作用域', () => {
    const schema = record('note'), action = record('summarize', 'action'), fn = record('calculator', 'function'), link = record('note-tags', 'link')
    schema.definition.schema!.properties = [enumProperty('state', 'state-enum')]
    action.definition.action = { sideEffects: 'none', targetSchemaIds: ['note'], functionId: 'calculator', input: [enumProperty('state', 'state-enum')], output: [enumProperty('result', 'result-enum')] }
    fn.definition.function = { sideEffects: 'none', input: [], output: [], runtime: { language: 'python', source: '# calls ghost-function', entrypoint: 'run', timeoutSeconds: 1 } }
    expect(collectDefinitionReferences(schema)).toEqual([{ targetId: 'state-enum', label: '属性「state」引用枚举', kind: 'enum', propertyId: 'state', scope: 'property' }])
    expect(collectDefinitionReferences(action).map(item => [item.targetId, item.kind, item.scope])).toEqual([
      ['state-enum', 'enum', 'input'], ['result-enum', 'enum', 'output'], ['note', 'target', undefined], ['calculator', 'function', undefined],
    ])
    expect(collectDefinitionReferences(link).map(item => [item.targetId, item.kind])).toEqual([['note', 'source'], ['tag', 'destination']])
    expect(collectDefinitionReferences(fn)).toEqual([])
  })

  it('依赖线校验目标类型，不绘制不存在、跨租户、草稿或错类型目标', () => {
    const schema = record('note'), enumType = record('enum', 'enum'), foreign = record('foreign', 'enum', 'shared', 'foreign-tenant'), draft = { ...record('draft', 'enum'), status: 'draft' }
    schema.definition.schema!.properties = [enumProperty('valid', 'enum'), enumProperty('wrong-kind', 'function'), enumProperty('hidden', 'missing'), enumProperty('foreign', 'foreign'), enumProperty('draft', 'draft')]
    const model = build([schema, enumType, record('function', 'function'), foreign, draft])
    expect(model.edges.map(edge => edge.target)).toEqual(['enum'])
    expect(model.nodes.some(node => ['foreign', 'draft', 'missing'].includes(node.id))).toBe(false)
  })

  it('保留同租户合法跨分类依赖，排除没有参与引用的外部定义', () => {
    const schema = record('note'), external = record('external-enum', 'enum', 'shared'), unrelated = record('unrelated', 'function', 'shared')
    schema.definition.schema!.properties = [enumProperty('state', external.definition.id)]
    const model = build([schema, external, unrelated])
    expect(model.nodes.map(node => node.id).sort()).toEqual(['external-enum', 'note'])
    expect(model.edges[0].source).toBe('note'); expect(model.edges[0].target).toBe('external-enum')
  })

  it('筛选枚举或函数时仍展示当前可见的引用来源，聚焦只保留相邻依赖', () => {
    const schema = record('note'), enumType = record('state', 'enum'), action = record('action', 'action'), fn = record('calculator', 'function'), unrelated = record('unrelated')
    schema.definition.schema!.properties = [enumProperty('status', 'state')]
    action.definition.action!.functionId = 'calculator'; action.definition.action!.targetSchemaIds = ['note']
    const enumModel = build([schema, enumType, action, fn, unrelated], { rootIds: ['state'] })
    expect(enumModel.nodes.some(node => node.id === 'note')).toBe(true)
    expect(enumModel.nodes.some(node => node.id === 'unrelated')).toBe(false)
    const functionModel = build([schema, enumType, action, fn, unrelated], { rootIds: ['calculator'], focusId: 'calculator' })
    expect(functionModel.nodes.map(node => node.id).sort()).toEqual(['action', 'calculator'])
  })

  it('重命名保持节点和依赖边身份；同名重建不会接管稳定引用', () => {
    const schema = record('note'), enumType = record('enum', 'enum')
    schema.definition.schema!.properties = [enumProperty('state', 'enum')]
    const before = build([schema, enumType])
    const renamed = structuredClone(enumType); renamed.definition.name = '新的中文业务名'
    const after = build([schema, renamed])
    expect(after.nodes.map(node => node.id).sort()).toEqual(before.nodes.map(node => node.id).sort())
    expect(after.edges.map(edge => edge.id)).toEqual(before.edges.map(edge => edge.id))
    const replacement = record('replacement', 'enum'); replacement.definition.name = enumType.definition.name
    expect(build([schema, replacement]).edges).toEqual([])
  })

  it('来源/目标和输入/输出即使使用同一身份也不合并为一条错误依赖', () => {
    const node = record('note'), self = record('self', 'link'), action = record('action', 'action'), enumType = record('enum', 'enum')
    self.definition.link!.targetSchemaId = 'note'
    action.definition.action!.input = [enumProperty('state', 'enum')]; action.definition.action!.output = [enumProperty('state', 'enum')]
    const model = build([node, self, action, enumType])
    expect(new Set(model.edges.map(edge => edge.id)).size).toBe(4)
    expect(new Set(model.edges.filter(edge => edge.source === 'self').map(edge => edge.offset)).size).toBe(2)
    expect(new Set(model.edges.filter(edge => edge.source === 'action').map(edge => edge.offset)).size).toBe(2)
  })
})

describe('本体画布范围与预算', () => {
  it('业务关系复用Link语义，自关联和平行关系独立，缺失端点不渲染隐藏对象', () => {
    const note = record('note'), tag = record('tag'), first = record('first', 'link'), second = record('second', 'link'), self = record('self', 'link'), hidden = record('hidden-link', 'link')
    self.definition.link!.targetSchemaId = 'note'; hidden.definition.link!.targetSchemaId = 'secret-id'
    const model = build([note, tag, first, second, self, hidden], { mode: 'relations' })
    expect(model.nodes.map(node => node.id).sort()).toEqual(['note', 'tag'])
    expect(model.edges).toHaveLength(3)
    expect(model.edges.find(edge => edge.record.definition.id === 'self')!.source).toBe('note')
    expect(model.edges.find(edge => edge.record.definition.id === 'self')!.target).toBe('note')
    expect(new Set(model.edges.filter(edge => edge.source !== edge.target).map(edge => edge.offset)).size).toBe(2)
    expect(model.unavailableRelations).toBe(true)
    expect(model.edges.some(edge => edge.target === 'secret-id')).toBe(false)
  })

  it('关系图选择预算外对象时优先显示，但不自动改为对象局部图', () => {
    const records = Array.from({ length: 130 }, (_, i) => record(`node-${String(i).padStart(3, '0')}`))
    const selectedId = records.at(-1)!.definition.id
    const model = build(records, { mode: 'relations', selectedId })
    expect(model.nodes).toHaveLength(120)
    expect(model.nodes.some(node => node.id === selectedId)).toBe(true)
    expect(model.omittedNodes).toBe(10)
  })

  it('定义依赖预算同时限制节点和连线，选中定义在预算内优先可见', () => {
    const enums = Array.from({ length: 117 }, (_, i) => record(`enum-${i}`, 'enum'))
    const schemas = Array.from({ length: 3 }, (_, i) => record(`schema-${i}`))
    schemas.forEach(schema => { schema.definition.schema!.properties = enums.map((enumType, i) => enumProperty(`property-${i}`, enumType.definition.id)) })
    const extra = Array.from({ length: 12 }, (_, i) => record(`extra-${i}`, 'function'))
    const selectedId = extra.at(-1)!.definition.id
    const model = build([...schemas, ...enums, ...extra], { selectedId })
    expect(model.nodes).toHaveLength(120)
    expect(model.edges.length).toBeLessThanOrEqual(240)
    expect(model.nodes.some(node => node.id === selectedId)).toBe(true)
    expect(model.omittedNodes).toBe(12)
    expect(model.omittedEdges).toBeGreaterThan(0)
  })

  it('没有可信分类租户时不输出图，也不修改输入定义', () => {
    const records = [record('note'), record('tag'), record('link', 'link')]
    const before = JSON.stringify(records)
    expect(build(records, { categories: [] }).nodes).toEqual([])
    build(records, { mode: 'relations' }); build(records)
    expect(JSON.stringify(records)).toBe(before)
  })
})
