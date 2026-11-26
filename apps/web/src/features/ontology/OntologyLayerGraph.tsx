import { useId, useMemo, useRef, useState } from 'react'
import { ZoomIn, ZoomOut } from 'lucide-react'
import type { Organization } from '../../lib/types'
import type { OntologyGraphProps } from './OntologyGraph'
import { buildOntologyGraph } from './ontology-graph'
import './ontology-layer-graph.css'

const graphWidth = 1000
const schemaLayerSymbols: Record<string, string> = { L1: 'P', L2: 'C', L3: 'T' }
const zoneColors = ['--schema', '--link', '--enum', '--action', '--function', '--success']
const kindNames = { schema: '对象模型', link: '关系', enum: '枚举', action: '动作', function: '函数' }
const unassigned = { id: '', name: '未分层' }

type Props = Pick<OntologyGraphProps, 'records' | 'categories' | 'categoryId' | 'selectedId' | 'rootIds' | 'onSelect'> & {
  layers: { layers: { id: string; name: string }[]; assignments: Record<string, string> }
  groups: Organization[]
  hiddenLayers?: ReadonlySet<string>
  focusedGroup?: string
  showZones?: boolean
  showLabels?: boolean
  showAllEdges?: boolean
}

export function OntologyLayerGraph({
  records, categories, categoryId, selectedId, rootIds, onSelect, layers, groups,
  hiddenLayers, focusedGroup, showZones = true, showLabels = false, showAllEdges = false,
}: Props) {
  const model = useMemo(() => buildOntologyGraph({ records, categories, categoryId, mode: 'dependencies', selectedId, rootIds, panorama: true, focusedGroup }),
    [records, categories, categoryId, selectedId, rootIds, focusedGroup])
  const [zoom, setZoom] = useState(1)
  const [hoveredId, setHoveredId] = useState<string>()
  const viewport = useRef<HTMLDivElement>(null)
  const stage = useRef<HTMLDivElement>(null)
  const markerId = useId()
  const layerIds = new Set(layers.layers.map(layer => layer.id))
  const layerOf = (id: string) => {
    const assignment = Object.hasOwn(layers.assignments, id) ? layers.assignments[id] : unassigned.id
    return layerIds.has(assignment) ? assignment : unassigned.id
  }
  const buckets = new Map<string, typeof model.nodes>()
  for (const node of model.nodes) {
    const id = layerOf(node.id)
    const members = buckets.get(id) ?? []
    members.push(node)
    buckets.set(id, members)
  }

  const occupied = new Set(model.nodes.map(node => node.record.definition.groupId))
  const namedGroups = groups.filter(group => occupied.has(group.id)).sort((a, b) => a.name.localeCompare(b.name, 'zh-CN'))
  const namedIds = new Set(namedGroups.map(group => group.id))
  const groupSlots = [...namedIds, ...[...occupied].filter(id => !namedIds.has(id)).sort()]
  const minWidth = Math.max(320, groupSlots.length * 116)
  const groupWidth = graphWidth / Math.max(1, groupSlots.length)
  const allLayers = [...layers.layers, unassigned]
  let nextTop = 0
  const lanes = allLayers.flatMap(layer => {
    const members = buckets.get(layer.id) ?? []
    if (layer.id === unassigned.id && !members.length) return []
    const zones = groupSlots.flatMap((id, index) => {
      const entries = members.filter(node => node.record.definition.groupId === id)
        .sort((a, b) => a.record.definition.name.localeCompare(b.record.definition.name, 'zh-CN') || a.id.localeCompare(b.id))
      if (!entries.length) return []
      const columns = Math.min(entries.length, Math.max(1, Math.floor(minWidth / groupSlots.length / 108)))
      return [{ id, name: namedGroups.find(group => group.id === id)?.name, entries, columns,
        left: index * groupWidth, color: zoneColors[index % zoneColors.length] }]
    })
    const top = nextTop
    const rows = Math.max(0, ...zones.map(zone => Math.ceil(zone.entries.length / zone.columns)))
    const height = rows ? 52 + rows * 62 : 54
    nextTop += height
    // Keep the remaining panorama lanes in place when a layer is hidden.
    if (hiddenLayers?.has(layer.id)) return []

    const nodes = zones.flatMap(zone => zone.entries.map((node, index) => ({
      ...node, x: zone.left + (index % zone.columns + .5) * groupWidth / zone.columns,
      y: top + 62 + Math.floor(index / zone.columns) * 62,
    })))
    return [{ ...layer, top, height, zones, nodes }]
  })
  const height = nextTop
  const nodes = lanes.flatMap(lane => lane.nodes)
  const placed = new Map(nodes.map(node => [node.id, node]))
  const inGroup = (id: string) => !!focusedGroup && placed.get(id)?.record.definition.groupId === focusedGroup
  const edges = model.edges.flatMap(edge => {
    const from = placed.get(edge.source)
    const to = placed.get(edge.target)
    if (!from || !to) return []
    const loop = edge.source === edge.target
    const offset = loop ? -32 - Math.min(14, Math.abs(edge.offset) * .25) : edge.offset * .6
    const x = (from.x + to.x) / 2
    const y = (from.y + to.y) / 2 + offset
    const related = (id?: string) => !!id && (edge.record.definition.id === id || edge.source === id || edge.target === id)
    const selected = related(selectedId)
    const active = selected || related(hoveredId)
    const inFocus = inGroup(edge.source) || inGroup(edge.target)
    const path = loop
      ? `M ${from.x - 18} ${from.y} Q ${x - 90} ${y}, ${x} ${y} Q ${x + 90} ${y}, ${to.x + 18} ${to.y}`
      : `M ${from.x} ${from.y} Q ${(from.x + x) / 2} ${y}, ${x} ${y} Q ${(to.x + x) / 2} ${y}, ${to.x} ${to.y}`
    return [{ edge, x: x + (!loop && Math.abs(from.x - to.x) < 50 ? 28 : 0), y, selected, active, inFocus, path,
      crossLayer: layerOf(edge.source) !== layerOf(edge.target) }]
  })
  const neighbors = new Set(edges.filter(item => item.active || item.inFocus).flatMap(({ edge }) => [edge.source, edge.target]))
  const position = (x: number, y: number) => ({ left: `${x / graphWidth * 100}%`, top: `${y / height * 100}%` })

  function focusNode(id: string) {
    const node = placed.get(id)
    if (!node) return
    setZoom(current => Math.max(1.4, current))
    requestAnimationFrame(() => {
      const area = viewport.current
      const content = stage.current
      if (!area || !content) return
      area.scrollTo?.(node.x / graphWidth * content.clientWidth - area.clientWidth / 2, node.y / height * content.clientHeight - area.clientHeight / 2)
    })
  }

  return <section className="ontology-graph" aria-label="分层全景二维画布">
    <div className="ontology-graph-toolbar">
      <div role="group" aria-label="分层画布操作">
        <button type="button" aria-label="放大分层画布" disabled={zoom >= 2} onClick={() => setZoom(current => Math.min(2, current + .25))}><ZoomIn size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="缩小分层画布" disabled={zoom <= 1} onClick={() => setZoom(current => Math.max(1, current - .25))}><ZoomOut size={15} aria-hidden="true" /></button>
        <button type="button" onClick={() => { setZoom(1); viewport.current?.scrollTo?.(0, 0) }}>复位</button>
        <button type="button" disabled={!selectedId || !placed.has(selectedId)} onClick={() => selectedId && focusNode(selectedId)}>聚焦节点</button>
      </div>
    </div>
    <div ref={viewport} className="ontology-layer-viewport" aria-label="二维泳道" tabIndex={0}>
      {lanes.length > 0 ? <div ref={stage} className="ontology-lanes" style={{ width: `${zoom * 100}%`, minWidth: minWidth * zoom, height: height * zoom, minHeight: `${zoom * 100}%` }}>
        <svg viewBox={`0 0 ${graphWidth} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          <defs><marker id={markerId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 Z" fill="var(--edge)" /></marker></defs>
          {lanes.map(lane => <g key={lane.id}>
            <rect className="ontology-layer-lane" data-panorama-layer={lane.id} width={graphWidth} y={lane.top} height={lane.height} />
            {showZones && lane.zones.map(zone => <rect key={zone.id} className="panorama-zone" data-muted={!!focusedGroup && zone.id !== focusedGroup}
              style={{ color: `var(${zone.color})` }} x={zone.left + groupWidth * .025} y={lane.top + 26} width={groupWidth * .95} height={lane.height - 30} />)}
          </g>)}
          {edges.map(({ edge, path, active, inFocus, crossLayer }) => <path key={edge.id} d={path} className="ontology-layer-edge" data-active={active} data-cross-layer={crossLayer}
            data-connection={edge.kind}
            data-emphasis={!active ? (inFocus ? 'group' : showAllEdges ? 'all' : focusedGroup || selectedId || hoveredId ? 'muted' : undefined) : undefined}
            strokeDasharray={edge.kind === 'dependency' ? '5 4' : undefined} markerMid={active ? `url(#${markerId})` : undefined} />)}
          {edges.filter(item => item.selected).slice(0, 16).map(({ edge, path }) => <circle key={edge.id} r={3.4} className="panorama-flow">
            <animateMotion path={path} dur="2.1s" repeatCount="indefinite" />
          </circle>)}
        </svg>
        {lanes.map(lane => <span key={lane.id} className="ontology-layer-caption" data-panorama-layer={lane.id} style={position(0, lane.top)}>
          {lane.name} · {lane.id === 'M0' ? '暂无实例' : lane.nodes.length}
        </span>)}
        {showZones && lanes.flatMap(lane => lane.zones.map(zone => zone.name && <span key={`${lane.id}:${zone.id}`}
          className="panorama-zone-name" data-muted={!!focusedGroup && zone.id !== focusedGroup}
          style={{ ...position(zone.left + groupWidth / 2, lane.top + 26), color: `var(${zone.color})` }}>{zone.name}</span>))}
        {edges.map(({ edge, x, y, active }) => <button type="button" key={edge.id} className="ontology-layer-label" data-active={active}
          style={position(x, y)} aria-label={`查看${edge.record.definition.name}，${edge.label}`} onClick={() => onSelect(edge.record.definition.id)}>
          <span>{edge.label}{edge.cardinality && ` · ${edge.cardinality}`}</span>
        </button>)}
        {nodes.map(({ id, record: { definition }, x, y }) => <button type="button" key={id} className="ontology-layer-node"
          style={position(x, y)} data-panorama-kind={definition.kind} data-layer={layerOf(id)}
          data-muted={!!focusedGroup && !inGroup(id) && !neighbors.has(id) && id !== selectedId && id !== hoveredId}
          data-emphasis={id === selectedId || id === hoveredId || neighbors.has(id) || showLabels || nodes.length <= 12 ? 'label' : undefined}
          onPointerEnter={() => setHoveredId(id)} onPointerLeave={() => setHoveredId(undefined)}
          onDoubleClick={() => focusNode(id)} aria-pressed={selectedId === id}
          aria-label={`${kindNames[definition.kind]}：${definition.name}`} title={definition.name} onClick={() => onSelect(id)}>
          <span aria-hidden="true"><b>{definition.kind === 'function' ? 'ƒ' : definition.kind === 'action' ? 'A' : schemaLayerSymbols[layerOf(id)]}</b></span>
          <strong>{definition.name}</strong>
        </button>)}
      </div> : <div className="ontology-graph-empty">当前范围没有可显示的定义，请调整筛选。</div>}
    </div>
    <div className="ontology-graph-footer"><span>实线 · Link　虚线 · 动作与函数关联</span><span>{nodes.length} 个节点 · {edges.length} 条连线</span></div>
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120 个节点、240 条连线），可通过搜索、筛选或聚焦缩小范围。</p>}
  </section>
}
