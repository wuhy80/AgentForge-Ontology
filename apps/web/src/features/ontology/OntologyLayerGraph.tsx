import { useEffect, useId, useMemo, useRef, useState, type PointerEvent } from 'react'
import { ZoomIn, ZoomOut } from 'lucide-react'
import type { Organization } from '../../lib/types'
import type { OntologyGraphProps } from './OntologyGraph'
import { buildOntologyGraph } from './ontology-graph'
import './ontology-layer-graph.css'

const graphWidth = 1000
const height = 760
const deckWidth = 680
const deckDepth = 270
const cameras = { iso: [-.32, .53], front: [0, .12], top: [0, Math.PI / 2] } as const
type Camera = readonly [number, number]
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
  const [camera, setCamera] = useState<Camera>(cameras.iso)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const drag = useRef<{ x: number; y: number; camera: Camera; pan: typeof pan; translate: boolean }>(undefined)
  const [hoveredId, setHoveredId] = useState<string>()
  const viewport = useRef<HTMLDivElement>(null)
  const stage = useRef<HTMLDivElement>(null)
  const markerId = useId()
  useEffect(() => {
    const area = viewport.current
    if (!area) return
    function zoomOnWheel(event: WheelEvent) {
      if (event.ctrlKey || !event.deltaY) return
      event.preventDefault()
      setZoom(current => Math.max(1, Math.min(2, current - Math.sign(event.deltaY) * .1)))
    }
    area.addEventListener('wheel', zoomOnWheel, { passive: false })
    return () => area.removeEventListener('wheel', zoomOnWheel)
  }, [])
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
  const allLayers = [...layers.layers, ...(buckets.has(unassigned.id) ? [unassigned] : [])]
  const groupWidth = deckWidth / Math.max(1, groupSlots.length)
  const [yaw, pitch] = camera
  const project = (x: number, elevation: number, depth: number) => {
    const across = x * Math.cos(yaw) - depth * Math.sin(yaw)
    const away = x * Math.sin(yaw) + depth * Math.cos(yaw)
    return { x: graphWidth / 2 + across + pan.x, y: height / 2 - elevation * Math.cos(pitch) + away * Math.sin(pitch) + pan.y }
  }
  const point = ({ x, y }: { x: number; y: number }) => `${x},${y}`
  const corners = (left: number, right: number, elevation: number, near = -deckDepth / 2, far = deckDepth / 2) =>
    [project(left, elevation, near), project(right, elevation, near), project(right, elevation, far), project(left, elevation, far)]
  const polygon = (...args: Parameters<typeof corners>) => corners(...args).map(point).join(' ')
  const lanes = allLayers.flatMap((layer, layerIndex) => {
    if (hiddenLayers?.has(layer.id)) return []
    const elevation = 245 - layerIndex * (490 / Math.max(1, allLayers.length - 1))
    const zones = groupSlots.map((id, slot) => {
      const entries = (buckets.get(layer.id) ?? []).filter(node => node.record.definition.groupId === id)
        .sort((a, b) => a.record.definition.name.localeCompare(b.record.definition.name, 'zh-CN') || a.id.localeCompare(b.id))
      const left = -deckWidth / 2 + slot * groupWidth
      return { id, name: namedGroups.find(group => group.id === id)?.name, entries, left, color: zoneColors[slot % zoneColors.length] }
    })
    const nodes = zones.flatMap(zone => {
      const columns = Math.max(1, Math.min(zone.entries.length, Math.ceil(Math.sqrt(zone.entries.length * groupWidth / deckDepth))))
      const rows = Math.ceil(zone.entries.length / columns)
      return zone.entries.map((node, index) => ({
        ...node,
        worldX: zone.left + (index % columns + .5) * groupWidth / columns,
        depth: ((Math.floor(index / columns) + .5) / rows - .5) * (deckDepth - 34),
      })).map(node => ({
        ...node,
        ...project(node.worldX, elevation + 6, node.depth + (camera === cameras.top ? layerIndex * 17 : 0)),
      }))
    })
    return [{ ...layer, elevation, zones, nodes }]
  })
  const topView = camera === cameras.top
  const decks = topView ? lanes.slice(-1) : [...lanes].reverse()
  const nodes = lanes.flatMap(lane => lane.nodes)
  const highlighted = nodes.find(node => node.id === (hoveredId ?? selectedId))
  const floor = highlighted && !topView && decks.length ? project(highlighted.worldX, decks[0].elevation, highlighted.depth) : undefined
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

  function startDrag(event: PointerEvent<HTMLDivElement>) {
    if (event.target instanceof Element && event.target.closest('button')) return
    if (event.button !== 0 && event.button !== 2) return
    drag.current = { x: event.clientX, y: event.clientY, camera, pan, translate: event.shiftKey || event.button === 2 }
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  function moveDrag(event: PointerEvent<HTMLDivElement>) {
    const current = drag.current
    if (!current) return
    const dx = event.clientX - current.x
    const dy = event.clientY - current.y
    if (current.translate) {
      const scale = graphWidth / (stage.current?.clientWidth || graphWidth)
      setPan({ x: current.pan.x + dx * scale, y: current.pan.y + dy * scale })
    } else {
      setCamera([current.camera[0] + dx / 240, Math.max(.12, Math.min(1.56, current.camera[1] + dy / 240))])
    }
  }

  return <section className="ontology-graph" aria-label="分层全景画布">
    <div ref={viewport} className="ontology-layer-viewport panorama-viewport" aria-label="立体分层图" tabIndex={0}
      onPointerDown={startDrag} onPointerMove={moveDrag} onLostPointerCapture={() => { drag.current = undefined }}
      onContextMenu={event => event.preventDefault()}>
      {lanes.length > 0 ? <div ref={stage} className="ontology-lanes panorama-stage" style={{ width: `${zoom * 100}%`, height: `${zoom * 100}%` }}>
        <svg viewBox={`0 0 ${graphWidth} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          <defs><marker id={markerId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 Z" fill="var(--edge)" /></marker></defs>
          {!topView && decks.length > 1 && corners(-deckWidth / 2, deckWidth / 2, decks[0].elevation).map((corner, index) => {
            const cap = corners(-deckWidth / 2, deckWidth / 2, decks[decks.length - 1].elevation)[index]
            return <line key={index} className="panorama-pillar" x1={corner.x} y1={corner.y} x2={cap.x} y2={cap.y} />
          })}
          {decks.map(lane => {
            const slab = corners(-deckWidth / 2, deckWidth / 2, lane.elevation)
            return <g key={lane.id} data-panorama-layer={lane.id}>
              <polygon className="panorama-slab-rim" points={[slab[3], slab[2], { ...slab[2], y: slab[2].y + 9 }, { ...slab[3], y: slab[3].y + 9 }].map(point).join(' ')} />
              <polygon className="ontology-layer-lane" data-panorama-layer={lane.id} points={slab.map(point).join(' ')} />
              {showZones && [-.25, 0, .25].map(fraction => {
                const [from, to] = corners(-deckWidth / 2, deckWidth / 2, lane.elevation, deckDepth * fraction)
                return <line key={fraction} className="panorama-floor-line" x1={from.x} y1={from.y} x2={to.x} y2={to.y} />
              })}
              {showZones && lane.zones.map(zone => <polygon key={zone.id} className="panorama-zone" data-muted={!!focusedGroup && zone.id !== focusedGroup}
                style={{ color: `var(${zone.color})` }} points={polygon(zone.left + 4, zone.left + groupWidth - 4, lane.elevation, -deckDepth / 2 + 5, deckDepth / 2 - 5)} />)}
            </g>
          })}
          {floor && highlighted && <line className="panorama-drop-line" x1={highlighted.x} y1={highlighted.y} x2={floor.x} y2={floor.y} />}
          {edges.map(({ edge, path, active, inFocus, crossLayer }) => <path key={edge.id} d={path} className="ontology-layer-edge" data-active={active} data-cross-layer={crossLayer}
            data-connection={edge.kind}
            data-emphasis={!active ? (inFocus ? 'group' : showAllEdges ? 'all' : focusedGroup || selectedId || hoveredId ? 'muted' : undefined) : undefined}
            strokeDasharray={edge.kind === 'dependency' ? '5 4' : undefined} markerMid={active ? `url(#${markerId})` : undefined} />)}
          {edges.filter(item => item.selected).slice(0, 16).map(({ edge, path }) => <circle key={edge.id} r={3.4} className="panorama-flow">
            <animateMotion path={path} dur="2.1s" repeatCount="indefinite" />
          </circle>)}
        </svg>
        {!topView && lanes.map(lane => {
          const anchor = project(-deckWidth / 2, lane.elevation, deckDepth / 2)
          return <span key={lane.id} className="ontology-layer-caption" data-panorama-layer={lane.id} style={position(anchor.x, anchor.y)}>
            <span className="panorama-caption-full">{lane.name} · {lane.id === 'M0' ? '暂无实例' : lane.nodes.length}</span>
            <span className="panorama-caption-short">{lane.id === 'pending' || !lane.id ? lane.name : lane.id} · {lane.nodes.length}</span>
          </span>
        })}
        {showZones && decks.flatMap(lane => lane.zones.map(zone => {
          const anchor = project(zone.left + groupWidth / 2, lane.elevation, deckDepth / 2)
          return zone.name && <span key={`${lane.id}:${zone.id}`} className="panorama-zone-name" data-upper={lane.id === decks.at(-1)?.id} data-muted={!!focusedGroup && zone.id !== focusedGroup}
            style={{ ...position(anchor.x, anchor.y), color: `var(${zone.color})` }}>{zone.name}</span>
        }))}
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
    <div className="ontology-graph-toolbar panorama-camera">
      <div role="group" aria-label="分层画布操作">
        {([['iso', '立体全景'], ['front', '正视分层'], ['top', '俯视分区']] as const).map(([view, name]) =>
          <button key={view} type="button" aria-pressed={camera === cameras[view]} onClick={() => { setCamera(cameras[view]); setPan({ x: 0, y: 0 }) }}>{name}</button>)}
        <button type="button" onClick={() => { setCamera(cameras.iso); setPan({ x: 0, y: 0 }); setZoom(1); viewport.current?.scrollTo?.(0, 0) }}>复位</button>
        <button type="button" disabled={!selectedId || !placed.has(selectedId)} onClick={() => selectedId && focusNode(selectedId)}>聚焦节点</button>
        <button type="button" aria-label="缩小分层画布" disabled={zoom <= 1} onClick={() => setZoom(current => Math.max(1, current - .25))}><ZoomOut size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="放大分层画布" disabled={zoom >= 2} onClick={() => setZoom(current => Math.min(2, current + .25))}><ZoomIn size={15} aria-hidden="true" /></button>
      </div>
      <small>拖动旋转 · 滚轮缩放 · Shift 拖动平移</small>
    </div>
    <div className="ontology-graph-footer"><span>实线 · Link　虚线 · 动作与函数关联</span><span>{nodes.length} 个节点 · {edges.length} 条连线</span></div>
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120 个节点、240 条连线），可通过搜索、筛选或聚焦缩小范围。</p>}
  </section>
}
