import { describe, expect, it } from 'vitest'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { arrangeRelationNodes, buildRelationGraph, MAX_RELATION_LINKS, MAX_RELATION_NODES, parseRelationLayout } from './relation-graph'

const categories: Organization[] = [{ id: 'knowledge', tenantId: 'tenant', name: '个人知识库' }, { id: 'other', tenantId: 'tenant', name: '共享分类' }]
function schema(id: string, tenantId = 'tenant', categoryId = 'knowledge', status = 'active'): DefinitionRecord {
  return { status, etag: `${id}-${status}`, definition: { id, tenantId, categoryId, groupId: 'group', kind: 'schema', name: `对象${id}`, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } }
}
function link(id: string, source: string, target: string, status = 'active', tenantId = 'tenant'): DefinitionRecord {
  return { status, etag: `${id}-${status}`, definition: { id, tenantId, categoryId: 'knowledge', groupId: 'group', kind: 'link', name: `关系${id}`, link: { sourceSchemaId: source, targetSchemaId: target, forwardName: '关联到', reverseName: '关联自', cardinality: '1:N', onDelete: 'restrict' } } }
}

describe('关系图的定义边界和有界布局', () => {
  it('依据当前分类租户过滤混入的对象和关系，并保留合法跨分类端点', () => {
    const result = buildRelationGraph({ schemas: [schema('a'), schema('b', 'tenant', 'other'), schema('secret', 'foreign')], links: [link('ab', 'a', 'b'), link('secret', 'a', 'secret', 'active', 'foreign')], categories, categoryId: 'knowledge' })
    expect(result.nodes.map((node) => node.id)).toEqual(['a', 'b'])
    expect(result.edges.map((edge) => edge.record.definition.id)).toEqual(['ab'])
  })

  it('隐藏端点使用匿名占位节点，不把端点ID或名称作为可展示信息', () => {
    const secret = schema('hidden-sensitive-id', 'foreign')
    secret.definition.name = '不可见对象的名称'
    const result = buildRelationGraph({ schemas: [schema('a'), secret], links: [link('visible-link', 'a', secret.definition.id)], categories, categoryId: 'knowledge' })
    const hidden = result.nodes.find((node) => node.restricted)!
    expect(hidden.id).toMatch(/^restricted-endpoint-/)
    expect(hidden.record).toBeUndefined()
    expect(JSON.stringify(hidden)).not.toContain('sensitive')
    expect(JSON.stringify(hidden)).not.toContain(secret.definition.name)
    expect(result.edges[0].target).toBe(hidden.id)
    expect(result.edges[0].restricted).toBe(true)
  })

  it('平行关系、同Link当前态与草稿、自关联分别保持身份并分开路由', () => {
    const result = buildRelationGraph({ schemas: [schema('a'), schema('b')], links: [link('ab', 'a', 'b'), link('ab', 'a', 'b', 'draft'), link('other', 'b', 'a'), link('self', 'a', 'a')], categories, categoryId: 'knowledge' })
    expect(new Set(result.edges.map((edge) => edge.id)).size).toBe(4)
    expect(new Set(result.edges.filter((edge) => edge.source !== edge.target).map((edge) => edge.offset)).size).toBe(3)
    const self = result.edges.find((edge) => edge.record.definition.id === 'self')!
    expect(self.source).toBe(self.target)
  })

  it('对象局部图包含正向、反向、自关联和邻居，不混入无关关系', () => {
    const result = buildRelationGraph({ schemas: ['a', 'b', 'c', 'd'].map((id) => schema(id)), links: [link('out', 'a', 'b'), link('in', 'c', 'a'), link('self', 'a', 'a'), link('unrelated', 'b', 'd')], categories, categoryId: 'knowledge', focusSchemaId: 'a' })
    expect(result.nodes.map((node) => node.id).sort()).toEqual(['a', 'b', 'c'])
    expect(result.edges.map((edge) => edge.record.definition.id).sort()).toEqual(['in', 'out', 'self'])
  })

  it('优先显示选中的关系端点，超预算明确保留遗漏数量', () => {
    const schemas = Array.from({ length: MAX_RELATION_NODES + 10 }, (_, index) => schema(`node-${index.toString().padStart(3, '0')}`))
    const links = [link('selected', schemas.at(-2)!.definition.id, schemas.at(-1)!.definition.id), ...Array.from({ length: MAX_RELATION_LINKS + 10 }, (_, index) => link(`edge-${index}`, 'node-000', 'node-001'))]
    const result = buildRelationGraph({ schemas, links, categories, categoryId: 'knowledge', selectedLinkId: 'selected' })
    expect(result.nodes).toHaveLength(MAX_RELATION_NODES)
    expect(result.nodes.some((node) => node.id === schemas.at(-1)!.definition.id)).toBe(true)
    expect(result.edges).toHaveLength(MAX_RELATION_LINKS)
    expect(result.omittedNodes).toBe(10)
    expect(result.omittedLinks).toBe(11)
  })

  it('不能建立可信租户范围时返回空图', () => {
    expect(buildRelationGraph({ schemas: [schema('a', 'foreign', 'foreign-category')], links: [], categories: [], categoryId: 'knowledge' }).nodes).toEqual([])
  })

  it('含循环、孤立对象和多列时布局有界，不修改原定义或重复位置', () => {
    const schemas = Array.from({ length: 24 }, (_, index) => schema(String(index)))
    const links = [link('01', '0', '1'), link('10', '1', '0'), link('self', '1', '1')]
    const before = JSON.stringify({ schemas, links })
    const model = buildRelationGraph({ schemas, links, categories, categoryId: 'knowledge' })
    const positions = arrangeRelationNodes(model.nodes, model.edges)
    expect(new Set(Object.values(positions).map(({ x, y }) => `${x},${y}`)).size).toBe(24)
    expect(Object.values(positions).every(({ x, y }) => Number.isFinite(x) && Number.isFinite(y))).toBe(true)
    expect(JSON.stringify({ schemas, links })).toBe(before)
  })
})

describe('关系画布个人布局', () => {
  it('仅接受当前可见节点坐标和有限范围视口', () => {
    const result = parseRelationLayout(JSON.stringify({ version: 1, positions: { allowed: { x: 10, y: 20 }, revoked: { x: 30, y: 40 }, broken: { x: 'bad', y: 0 } }, viewport: { x: 1, y: 2, zoom: 0.8 } }), new Set(['allowed', 'broken']))
    expect(result).toEqual({ positions: { allowed: { x: 10, y: 20 } }, viewport: { x: 1, y: 2, zoom: 0.8 } })
  })

  it('损坏JSON、未知格式或越界坐标不会传给画布', () => {
    expect(parseRelationLayout('{', new Set())).toBeUndefined()
    expect(parseRelationLayout('{"version":2,"positions":{}}', new Set())).toBeUndefined()
    expect(parseRelationLayout(JSON.stringify({ version: 1, positions: { a: { x: 1e10, y: 1 } }, viewport: { x: 0, y: 0, zoom: 100 } }), new Set(['a']))).toEqual({ positions: {} })
  })
})
