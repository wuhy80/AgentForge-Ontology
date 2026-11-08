import type { DefinitionRecord, Kind, Organization, Property } from '../../lib/types'
import { arrangeRelationNodes, buildRelationGraph, MAX_RELATION_LINKS, MAX_RELATION_NODES, type GraphPoint } from '../model/relation-graph'

export type OntologyGraphMode = 'relations' | 'dependencies'
export type DefinitionReferenceKind = 'enum' | 'target' | 'function' | 'source' | 'destination'
export interface DefinitionReference {
  targetId: string
  label: string
  kind: DefinitionReferenceKind
  propertyId?: string
  scope?: 'property' | 'input' | 'output'
}

// Only persisted stable references are dependencies. Source text and business
// names are never scanned to infer references or function calls.
export function collectDefinitionReferences(record: DefinitionRecord): DefinitionReference[] {
  const { definition } = record
  const references: DefinitionReference[] = []
  function enums(properties: Property[] | undefined, scope: 'property' | 'input' | 'output') {
    for (const property of properties ?? []) if (property.enumId) references.push({
      targetId: property.enumId, kind: 'enum', propertyId: property.id, scope,
      label: `${scope === 'property' ? '属性' : scope === 'input' ? '输入参数' : '输出参数'}「${property.name}」引用枚举`,
    })
  }
  if (definition.kind === 'schema') enums(definition.schema?.properties, 'property')
  if (definition.kind === 'link' && definition.link) {
    references.push({ targetId: definition.link.sourceSchemaId, label: '来源对象', kind: 'source' })
    references.push({ targetId: definition.link.targetSchemaId, label: '目标对象', kind: 'destination' })
  }
  if (definition.kind === 'action' || definition.kind === 'function') {
    const callable = definition.kind === 'action' ? definition.action : definition.function
    enums(callable?.input, 'input'); enums(callable?.output, 'output')
    if (definition.kind === 'action') {
      for (const id of callable?.targetSchemaIds ?? []) if (id) references.push({ targetId: id, label: '作用对象', kind: 'target' })
      if (callable?.functionId) references.push({ targetId: callable.functionId, label: '绑定函数', kind: 'function' })
    }
  }
  return references.filter(reference => !!reference.targetId)
}

export interface OntologyGraphNode { id: string; record: DefinitionRecord; position: GraphPoint }
export interface OntologyGraphEdge {
  id: string
  source: string
  target: string
  record: DefinitionRecord
  label: string
  reverseLabel?: string
  cardinality?: string
  offset: number
  kind: 'relation' | 'dependency'
}
export interface OntologyGraphModel {
  nodes: OntologyGraphNode[]
  edges: OntologyGraphEdge[]
  omittedNodes: number
  omittedEdges: number
  unavailableRelations: boolean
}
export interface OntologyGraphInput {
  records: DefinitionRecord[]
  categories: Organization[]
  categoryId: string
  mode: OntologyGraphMode
  selectedId?: string
  focusId?: string
  rootIds?: string[]
}
const expectedKinds: Record<DefinitionReferenceKind, Kind> = { enum: 'enum', target: 'schema', function: 'function', source: 'schema', destination: 'schema' }
const emptyGraph = (): OntologyGraphModel => ({ nodes: [], edges: [], omittedNodes: 0, omittedEdges: 0, unavailableRelations: false })

export function buildOntologyGraph(input: OntologyGraphInput): OntologyGraphModel {
  const { records, categories, categoryId, mode, selectedId, focusId } = input
  const tenantId = categories.find(category => category.id === categoryId)?.tenantId
  if (!tenantId) return emptyGraph()
  const current = new Map<string, DefinitionRecord>()
  for (const record of records) if (record.status === 'active' && record.definition.tenantId === tenantId) current.set(record.definition.id, record)
  const requested = input.rootIds ? new Set(input.rootIds) : undefined
  const roots = [...current.values()].filter(record => record.definition.categoryId === categoryId && (!requested || requested.has(record.definition.id)))
  if (mode === 'relations') {
    const rootIds = new Set(roots.map(record => record.definition.id))
    const links = [...current.values()].filter(record => record.definition.kind === 'link' && record.definition.link
      && (rootIds.has(record.definition.id) || rootIds.has(record.definition.link.sourceSchemaId) || rootIds.has(record.definition.link.targetSchemaId)))
    const validLinks = links.filter(record => current.get(record.definition.link!.sourceSchemaId)?.definition.kind === 'schema'
      && current.get(record.definition.link!.targetSchemaId)?.definition.kind === 'schema')
    const scope = new Set(roots.filter(record => record.definition.kind === 'schema').map(record => record.definition.id))
    validLinks.forEach(record => { scope.add(record.definition.link!.sourceSchemaId); scope.add(record.definition.link!.targetSchemaId) })
    const focus = current.get(focusId ?? '')
    const focusSchemaId = focus?.definition.kind === 'schema' ? focus.definition.id
      : focus?.definition.kind === 'link' ? focus.definition.link?.sourceSchemaId : undefined
    const model = buildRelationGraph({
      schemas: [...current.values()].filter(record => scope.has(record.definition.id)), links: validLinks,
      categories, categoryId, focusSchemaId,
      selectedLinkId: current.get(selectedId ?? '')?.definition.kind === 'link' ? selectedId : undefined,
    })
    const selectedSchema = current.get(selectedId ?? '')
    if (!focusSchemaId && selectedSchema?.definition.kind === 'schema' && scope.has(selectedSchema.definition.id)
      && !model.nodes.some(node => node.id === selectedId) && model.nodes.length) {
      model.nodes[model.nodes.length - 1] = { id: selectedSchema.definition.id, record: selectedSchema, restricted: false, position: { x: 0, y: 0 } }
      const visibleIds = new Set(model.nodes.map(node => node.id))
      const previousLength = model.edges.length
      model.edges = model.edges.filter(edge => visibleIds.has(edge.source) && visibleIds.has(edge.target))
      model.omittedLinks += previousLength - model.edges.length
      const positions = arrangeRelationNodes(model.nodes, model.edges)
      model.nodes.forEach(node => { node.position = positions[node.id] })
    }
    return {
      nodes: model.nodes.filter(node => !!node.record).map(node => ({ id: node.id, record: node.record!, position: node.position })),
      edges: model.edges.map(edge => ({ id: edge.id, source: edge.source, target: edge.target, record: edge.record,
        label: edge.record.definition.link!.forwardName || edge.record.definition.name, reverseLabel: edge.record.definition.link!.reverseName,
        cardinality: edge.record.definition.link!.cardinality, offset: edge.offset, kind: 'relation' })),
      omittedNodes: model.omittedNodes, omittedEdges: model.omittedLinks, unavailableRelations: validLinks.length !== links.length,
    }
  }

  const included = new Set(roots.map(record => record.definition.id))
  const rootIds = new Set(included)
  for (const record of current.values()) {
    if (collectDefinitionReferences(record).some(reference => rootIds.has(reference.targetId)
      && current.get(reference.targetId)?.definition.kind === expectedKinds[reference.kind])) included.add(record.definition.id)
  }
  // The supplied catalog has already bounded external reads. Traverse only
  // references whose authorized records are present, never fetch or infer.
  const queue = [...included]
  for (let index = 0; index < queue.length; index++) {
    const record = current.get(queue[index])!
    for (const reference of collectDefinitionReferences(record)) {
      const target = current.get(reference.targetId)
      if (!target || target.definition.kind !== expectedKinds[reference.kind] || included.has(reference.targetId)) continue
      included.add(reference.targetId); queue.push(reference.targetId)
    }
  }
  const edges: OntologyGraphEdge[] = []
  const unique = new Set<string>()
  for (const id of included) {
    const record = current.get(id)!
    for (const reference of collectDefinitionReferences(record)) {
      const target = current.get(reference.targetId)
      if (!included.has(reference.targetId) || target?.definition.kind !== expectedKinds[reference.kind]) continue
      const edgeId = JSON.stringify([id, reference.kind, reference.scope ?? '', reference.propertyId ?? '', reference.targetId])
      if (unique.has(edgeId)) continue
      unique.add(edgeId)
      edges.push({ id: `dependency:${edgeId}`, source: id, target: reference.targetId, record, label: reference.label, offset: 0, kind: 'dependency' })
    }
  }
  const visibleScope = new Set(included)
  if (focusId && included.has(focusId)) {
    visibleScope.clear(); visibleScope.add(focusId)
    for (const edge of edges) if (edge.source === focusId || edge.target === focusId) { visibleScope.add(edge.source); visibleScope.add(edge.target) }
  }
  const candidateEdges = edges.filter(edge => visibleScope.has(edge.source) && visibleScope.has(edge.target))
  const important = new Set([selectedId, focusId].filter((id): id is string => !!id))
  const priority = new Set(important)
  candidateEdges.forEach(edge => { if (important.has(edge.source) || important.has(edge.target)) { priority.add(edge.source); priority.add(edge.target) } })
  const compare = (left: DefinitionRecord, right: DefinitionRecord) =>
    Number(important.has(right.definition.id)) - Number(important.has(left.definition.id))
    || Number(priority.has(right.definition.id)) - Number(priority.has(left.definition.id))
    || left.definition.name.localeCompare(right.definition.name, 'zh-CN') || left.definition.id.localeCompare(right.definition.id)
  const candidates = [...visibleScope].map(id => current.get(id)!).sort(compare)
  const shown = candidates.slice(0, MAX_RELATION_NODES)
  const visibleIds = new Set(shown.map(record => record.definition.id))
  const shownEdges = candidateEdges.filter(edge => visibleIds.has(edge.source) && visibleIds.has(edge.target))
    .sort((left, right) => Number(priority.has(right.source) || priority.has(right.target)) - Number(priority.has(left.source) || priority.has(left.target)) || left.id.localeCompare(right.id))
    .slice(0, MAX_RELATION_LINKS)
  const pairs = new Map<string, OntologyGraphEdge[]>()
  for (const edge of shownEdges) {
    const key = JSON.stringify([edge.source, edge.target].sort())
    const group = pairs.get(key) ?? []; group.push(edge); pairs.set(key, group)
  }
  pairs.forEach(group => group.forEach((edge, index) => { edge.offset = (index - (group.length - 1) / 2) * 58 }))
  const positions = arrangeRelationNodes(shown.map(record => ({ id: record.definition.id })), shownEdges, focusId)
  return { nodes: shown.map(record => ({ id: record.definition.id, record, position: positions[record.definition.id] })), edges: shownEdges,
    omittedNodes: candidates.length - shown.length, omittedEdges: candidateEdges.length - shownEdges.length, unavailableRelations: false }
}
