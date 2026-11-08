import type { DefinitionRecord, Organization } from '../../lib/types'

export const MAX_RELATION_NODES = 120
export const MAX_RELATION_LINKS = 240

export interface GraphPoint { x: number; y: number }
export interface RelationGraphNode {
  id: string
  record?: DefinitionRecord
  restricted: boolean
  position: GraphPoint
}
export interface RelationGraphEdge {
  id: string
  source: string
  target: string
  record: DefinitionRecord
  offset: number
  restricted: boolean
}
export interface RelationGraphModel {
  nodes: RelationGraphNode[]
  edges: RelationGraphEdge[]
  omittedNodes: number
  omittedLinks: number
}
export interface RelationGraphInput {
  schemas: DefinitionRecord[]
  links: DefinitionRecord[]
  categories: Organization[]
  categoryId: string
  focusSchemaId?: string
  selectedLinkId?: string
}
export interface RelationLayout {
  positions: Record<string, GraphPoint>
  viewport?: GraphPoint & { zoom: number }
}

// The category is the authorization scope supplied by the workspace. A missing
// tenant is never interpreted as permission to mix records from every tenant.
export function buildRelationGraph(input: RelationGraphInput): RelationGraphModel {
  const { schemas, links, categories, categoryId, focusSchemaId, selectedLinkId } = input
  const tenantId = categories.find((category) => category.id === categoryId)?.tenantId
    ?? [...schemas, ...links].find((record) => record.definition.categoryId === categoryId && record.definition.tenantId)?.definition.tenantId
  if (!tenantId) return { nodes: [], edges: [], omittedNodes: 0, omittedLinks: 0 }
  const authorized = (record: DefinitionRecord) => record.definition.tenantId === tenantId
  const allSchemas = new Map<string, DefinitionRecord>()
  for (const record of schemas.filter(authorized)) {
    if (!record.definition.schema || !record.definition.id) continue
    const previous = allSchemas.get(record.definition.id)
    if (!previous || previous.status !== 'active') allSchemas.set(record.definition.id, record)
  }
  const distinctLinks = new Map<string, DefinitionRecord>()
  for (const record of links.filter(authorized)) {
    const link = record.definition.link
    if (!link || !record.definition.id || !link.sourceSchemaId || !link.targetSchemaId) continue
    if (focusSchemaId && link.sourceSchemaId !== focusSchemaId && link.targetSchemaId !== focusSchemaId && record.definition.id !== selectedLinkId) continue
    distinctLinks.set(`${record.definition.id}:${record.status}`, record)
  }
  const candidateLinks = [...distinctLinks.values()].sort((left, right) =>
    Number(right.definition.id === selectedLinkId) - Number(left.definition.id === selectedLinkId)
    || left.definition.name.localeCompare(right.definition.name, 'zh-CN')
    || left.status.localeCompare(right.status))
  const focusedIds = new Set<string>(focusSchemaId ? [focusSchemaId] : [])
  if (focusSchemaId) for (const record of candidateLinks) {
    focusedIds.add(record.definition.link!.sourceSchemaId)
    focusedIds.add(record.definition.link!.targetSchemaId)
  }
  const candidateSchemas = [...allSchemas.values()].filter((record) => !focusSchemaId || focusedIds.has(record.definition.id))
  const selected = candidateLinks.find((record) => record.definition.id === selectedLinkId)?.definition.link
  const priority = (record: DefinitionRecord) => Number(record.definition.id === focusSchemaId) * 2
    + Number(record.definition.id === selected?.sourceSchemaId || record.definition.id === selected?.targetSchemaId)
  candidateSchemas.sort((left, right) => priority(right) - priority(left)
    || left.definition.name.localeCompare(right.definition.name, 'zh-CN'))
  const nodes: RelationGraphNode[] = candidateSchemas.slice(0, MAX_RELATION_NODES).map((record) => ({
    id: record.definition.id, record, restricted: false, position: { x: 0, y: 0 },
  }))
  const visible = new Set(nodes.map((node) => node.id))
  const edges: RelationGraphEdge[] = []
  let omittedLinks = 0
  for (const record of candidateLinks) {
    const link = record.definition.link!
    // A record omitted by the rendering budget is different from a record the
    // caller cannot read. Do not mislabel the former as an authorization issue.
    if (edges.length >= MAX_RELATION_LINKS
      || allSchemas.has(link.sourceSchemaId) && !visible.has(link.sourceSchemaId)
      || allSchemas.has(link.targetSchemaId) && !visible.has(link.targetSchemaId)) {
      omittedLinks++; continue
    }
    const missing = Number(!visible.has(link.sourceSchemaId)) + Number(!visible.has(link.targetSchemaId))
    if (nodes.length + missing > MAX_RELATION_NODES) { omittedLinks++; continue }
    const endpoint = (id: string, side: 'source' | 'target') => {
      if (visible.has(id)) return id
      // Never put a hidden endpoint's stable ID into the DOM or saved layout.
      const placeholder = `restricted-endpoint-${edges.length}-${side}`
      nodes.push({ id: placeholder, restricted: true, position: { x: 0, y: 0 } })
      return placeholder
    }
    const source = endpoint(link.sourceSchemaId, 'source')
    const target = endpoint(link.targetSchemaId, 'target')
    edges.push({ id: `link:${record.definition.id}:${record.status}`, source, target, record, offset: 0,
      restricted: !visible.has(link.sourceSchemaId) || !visible.has(link.targetSchemaId) })
  }
  const pairs = new Map<string, RelationGraphEdge[]>()
  for (const edge of edges) {
    const key = [edge.source, edge.target].sort().join('\u0000')
    const group = pairs.get(key) ?? []
    group.push(edge); pairs.set(key, group)
  }
  for (const group of pairs.values()) group.forEach((edge, index) => {
    edge.offset = (index - (group.length - 1) / 2) * 76
  })
  const positions = arrangeRelationNodes(nodes, edges, focusSchemaId)
  return { nodes: nodes.map((node) => ({ ...node, position: positions[node.id] })), edges,
    omittedNodes: Math.max(0, candidateSchemas.length - visible.size), omittedLinks }
}

// A bounded breadth-first layout is predictable for model diagrams with cycles
// and self-links. It computes screen positions only, never ontology semantics.
export function arrangeRelationNodes(nodes: Pick<RelationGraphNode, 'id'>[], edges: Pick<RelationGraphEdge, 'source' | 'target'>[], focusSchemaId?: string): Record<string, GraphPoint> {
  const ids = new Set(nodes.map((node) => node.id))
  const adjacency = new Map(nodes.map((node) => [node.id, new Set<string>()]))
  const incoming = new Map(nodes.map((node) => [node.id, 0]))
  for (const edge of edges) {
    if (edge.source === edge.target || !ids.has(edge.source) || !ids.has(edge.target)) continue
    adjacency.get(edge.source)!.add(edge.target)
    incoming.set(edge.target, (incoming.get(edge.target) ?? 0) + 1)
  }
  const layers = new Map<string, number>()
  const roots = [...ids].filter((id) => incoming.get(id) === 0)
  if (focusSchemaId && ids.has(focusSchemaId)) roots.unshift(focusSchemaId)
  const visit = (root: string) => {
    if (layers.has(root)) return
    layers.set(root, 0)
    const queue = [root]
    for (let cursor = 0; cursor < queue.length; cursor++) {
      const source = queue[cursor]
      for (const target of adjacency.get(source) ?? []) {
        if (layers.has(target)) continue
        layers.set(target, Math.min(5, layers.get(source)! + 1)); queue.push(target)
      }
    }
  }
  roots.forEach(visit)
  ;[...ids].forEach(visit)
  const columns = new Map<number, string[]>()
  for (const id of ids) {
    const layer = layers.get(id) ?? 0
    const column = columns.get(layer) ?? []
    column.push(id); columns.set(layer, column)
  }
  const positions: Record<string, GraphPoint> = Object.create(null)
  let columnStart = 0
  for (const [, members] of [...columns].sort(([left], [right]) => left - right)) {
    members.forEach((id, row) => {
      // Additional rows wrap into their own columns instead of overlapping the
      // next breadth-first layer's first column.
      positions[id] = { x: 60 + (columnStart + Math.floor(row / 8)) * 370, y: 70 + row % 8 * 175 }
    })
    columnStart += Math.ceil(members.length / 8)
  }
  return positions
}

function point(value: unknown): value is GraphPoint {
  if (!value || typeof value !== 'object') return false
  const input = value as Record<string, unknown>
  return typeof input.x === 'number' && Number.isFinite(input.x) && Math.abs(input.x) <= 100000
    && typeof input.y === 'number' && Number.isFinite(input.y) && Math.abs(input.y) <= 100000
}

export function parseRelationLayout(text: string | null, visibleIds: Set<string>): RelationLayout | undefined {
  if (!text || text.length > 60000) return
  try {
    const input = JSON.parse(text) as Record<string, unknown>
    if (!input || input.version !== 1 || !input.positions || typeof input.positions !== 'object') return
    const positions: Record<string, GraphPoint> = Object.create(null)
    for (const [id, value] of Object.entries(input.positions).slice(0, MAX_RELATION_NODES)) {
      if (visibleIds.has(id) && point(value)) positions[id] = { x: value.x, y: value.y }
    }
    const viewport = input.viewport as Record<string, unknown> | undefined
    return { positions, ...(point(viewport) && typeof viewport.zoom === 'number' && viewport.zoom >= 0.15 && viewport.zoom <= 2.5
      ? { viewport: { x: viewport.x, y: viewport.y, zoom: viewport.zoom } } : {}) }
  } catch { return }
}
