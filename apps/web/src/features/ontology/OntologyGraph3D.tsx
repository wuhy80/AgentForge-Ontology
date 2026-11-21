import { useId, useMemo, useRef, useState } from 'react'
import { RotateCcw, RotateCw, ZoomIn, ZoomOut } from 'lucide-react'
import { buildOntologyGraph } from './ontology-graph'
import type { OntologyGraphProps } from './OntologyGraph'
import type { OntologyLayers } from './OntologyLayers'
import './ontology-graph-3d.css'

const initialCamera = { yaw: -.25, pitch: .38, zoom: 1 }
const depthByKind = { schema: 0, link: 1, enum: -1, action: 1, function: 2 }
const kindNames = { schema: '对象模型', link: '关系', enum: '枚举', action: '动作', function: '函数' }
const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(max, value))

export function OntologyGraph3D({ flat = false, layers, layerFocus, onLayerFocus, onSelect, ...input }: OntologyGraphProps & {
  flat?: boolean; layers?: OntologyLayers; layerFocus?: string; onLayerFocus?: (id?: string) => void
}) {
  const { records, categories, categoryId, mode, selectedId, focusId, rootIds } = input
  const model = useMemo(() => buildOntologyGraph({ records, categories, categoryId, mode, selectedId, focusId, rootIds }),
    [records, categories, categoryId, mode, selectedId, focusId, rootIds])
  const [camera, setCamera] = useState(initialCamera)
  const drag = useRef<{ id: number; x: number; y: number } | null>(null)
  const markerId = useId()
  const xs = model.nodes.map(node => node.position.x)
  const ys = model.nodes.map(node => node.position.y)
  const middleX = xs.length ? (Math.min(...xs) + Math.max(...xs)) / 2 : 0
  const middleY = ys.length ? (Math.min(...ys) + Math.max(...ys)) / 2 : 0
  const unit = Math.max(500, ...xs.map(x => Math.abs(x - middleX) * 2), ...ys.map(y => Math.abs(y - middleY) * 3))
  const cosYaw = Math.cos(camera.yaw)
  const sinYaw = Math.sin(camera.yaw)
  const cosPitch = Math.cos(camera.pitch)
  const sinPitch = Math.sin(camera.pitch)

  function project(x: number, y: number, z: number) {
    const horizontal = x * cosYaw + z * sinYaw
    const rotatedDepth = z * cosYaw - x * sinYaw
    const vertical = y * cosPitch - rotatedDepth * sinPitch
    const depth = y * sinPitch + rotatedDepth * cosPitch
    const scale = 1250 / (1250 - depth * .65)
    return { x: 500 + horizontal * scale * camera.zoom, y: 300 + vertical * scale * camera.zoom, depth, scale }
  }

  function gridLine(x1: number, z1: number, x2: number, z2: number) {
    const start = project(x1, 220, z1)
    const end = project(x2, 220, z2)
    return `M ${start.x} ${start.y} L ${end.x} ${end.y}`
  }
  const grid = Array.from({ length: 11 }, (_, index) => {
    const offset = (index - 5) * 84
    return gridLine(offset, -420, offset, 420) + ' ' + gridLine(-420, offset, 420, offset)
  }).join(' ')
  const unassigned = ''
  const layerIds = new Set(layers?.layers.map(layer => layer.id))
  function layerOf(id: string) {
    const assignment = layers && Object.hasOwn(layers.assignments, id) ? layers.assignments[id] : undefined
    return assignment && layerIds.has(assignment) ? assignment : unassigned
  }
  const buckets = new Map<string, typeof model.nodes>()
  for (const node of model.nodes) {
    const id = layerOf(node.id)
    const members = buckets.get(id)
    if (members) members.push(node)
    else buckets.set(id, [node])
  }
  const visibleLayers = layers ? [...layers.layers, { id: unassigned, name: '未分层' }]
    .filter(layer => buckets.has(layer.id) && (layerFocus === undefined || layerFocus === layer.id)) : []
  const lanes = visibleLayers.map((layer, index) => {
    const members = buckets.get(layer.id) ?? []
    const columns = Math.min(4, members.length)
    const rows = Math.ceil(members.length / columns)
    const y = visibleLayers.length === 1 ? 0 : -200 + index * 400 / (visibleLayers.length - 1)
    return { ...layer, members, columns, rows, y }
  })
  const totalRows = lanes.reduce((sum, lane) => sum + lane.rows, 0)
  let precedingRows = 0
  const planes = lanes.map(lane => {
    const top = 600 * precedingRows / totalRows
    precedingRows += lane.rows
    return { ...lane, top, bottom: 600 * precedingRows / totalRows }
  })
  const nodes = (layers ? planes.flatMap(lane => lane.members.map((node, index) => {
    const column = index % lane.columns
    const row = Math.floor(index / lane.columns)
    const x = (column - (lane.columns - 1) / 2) * 174
    const z = (row - (lane.rows - 1) / 2) * Math.min(95, 660 / lane.rows)
    const point = flat
      ? { x: (column + .5) * 1000 / lane.columns, y: lane.top + (row + .65) * (lane.bottom - lane.top) / lane.rows, depth: 0, scale: 1 }
      : project(x, lane.y, z)
    return { ...node, point }
  })) : model.nodes.map(node => ({
    ...node,
    point: project((node.position.x - middleX) * 660 / unit, (node.position.y - middleY) * 660 / unit,
      depthByKind[node.record.definition.kind] * 84 + Math.sin(node.position.y / 170) * 62),
  }))).sort((a, b) => a.point.depth - b.point.depth)
  const points = new Map(nodes.map(node => [node.id, node.point]))
  const edges = model.edges.flatMap(edge => {
    const from = points.get(edge.source)
    const to = points.get(edge.target)
    if (!from || !to) return []
    const loop = edge.source === edge.target
    const offset = loop ? -70 - Math.abs(edge.offset) * .4 : edge.offset * .65
    const x = (from.x + to.x) / 2
    const y = (from.y + to.y) / 2 + offset
    const active = edge.record.definition.id === input.selectedId || edge.source === input.selectedId || edge.target === input.selectedId
    const path = loop
      ? `M ${from.x - 22} ${from.y} Q ${x - 95} ${y}, ${x} ${y} Q ${x + 95} ${y}, ${to.x + 22} ${to.y}`
      : `M ${from.x} ${from.y} Q ${(from.x + x) / 2} ${(from.y + y) / 2 + offset / 2}, ${x} ${y} Q ${(to.x + x) / 2} ${(to.y + y) / 2 + offset / 2}, ${to.x} ${to.y}`
    return [{ edge, x, y, active, path, crossLayer: !!layers && layerOf(edge.source) !== layerOf(edge.target) }]
  })
  function orbit(yaw: number, pitch = 0) {
    setCamera(current => ({ ...current, yaw: current.yaw + yaw, pitch: clamp(current.pitch + pitch, -1.2, 1.2) }))
  }
  function zoom(delta: number) {
    setCamera(current => ({ ...current, zoom: clamp(current.zoom + delta, .55, 1.75) }))
  }

  const sceneLabel = flat ? '二维分层画布' : input.mode === 'relations' ? '本体对象关系三维画布' : '本体定义依赖三维画布'
  return <section className="ontology-graph" aria-label={sceneLabel}>
    {layers && <div className="ontology-graph-toolbar"><label>聚焦层 <select value={layerFocus === undefined ? 'all' : `layer:${layerFocus}`} onChange={event => {
      onLayerFocus?.(event.target.value === 'all' ? undefined : event.target.value.slice(6))
    }}><option value="all">全部层</option>{[...layers.layers, { id: '', name: '未分层' }].map(layer => <option key={layer.id} value={`layer:${layer.id}`}>{layer.name}</option>)}</select></label></div>}
    {!flat && <div className="ontology-graph-toolbar"><span>拖动画布旋转</span>
      <div role="group" aria-label="三维画布操作">
        <button type="button" aria-label="向左旋转" onClick={() => orbit(-.28)}><RotateCcw size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="向右旋转" onClick={() => orbit(.28)}><RotateCw size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="放大三维画布" onClick={() => zoom(.15)}><ZoomIn size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="缩小三维画布" onClick={() => zoom(-.15)}><ZoomOut size={15} aria-hidden="true" /></button>
        <button type="button" onClick={() => setCamera(initialCamera)}>复位</button>
      </div>
    </div>}
    {nodes.length ? <div className={`ontology-graph-surface ontology-space${flat ? ' ontology-space-flat' : ''}`}
      style={flat ? { height: Math.max(420, totalRows * 86) } : undefined} aria-label={flat ? '二维泳道' : '三维视角，方向键旋转'} tabIndex={0}
      onKeyDown={event => {
        if (flat || event.target !== event.currentTarget) return
        const step = { ArrowLeft: [-.2, 0], ArrowRight: [.2, 0], ArrowUp: [0, .15], ArrowDown: [0, -.15] }[event.key]
        if (!step) return
        event.preventDefault()
        orbit(step[0], step[1])
      }}
      onPointerDown={event => {
        if (flat || event.button !== 0 || !event.isPrimary || (event.target as Element).closest('button')) return
        event.currentTarget.setPointerCapture(event.pointerId)
        drag.current = { id: event.pointerId, x: event.clientX, y: event.clientY }
      }}
      onPointerMove={event => {
        const previous = drag.current
        if (!previous || previous.id !== event.pointerId) return
        orbit((event.clientX - previous.x) * .008, (event.clientY - previous.y) * .008)
        drag.current = { id: event.pointerId, x: event.clientX, y: event.clientY }
      }}
      onLostPointerCapture={() => { drag.current = null }}>
      <svg viewBox="0 0 1000 600" preserveAspectRatio="none" aria-hidden="true">
        <defs><marker id={markerId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 Z" fill="var(--edge)" /></marker></defs>
        {!layers && <path d={grid} className="ontology-space-grid" />}
        {planes.map(plane => {
          const corners = flat ? [{ x: 0, y: plane.top }, { x: 1000, y: plane.top }, { x: 1000, y: plane.bottom }, { x: 0, y: plane.bottom }]
            : [project(-380, plane.y, -340), project(380, plane.y, -340), project(380, plane.y, 340), project(-380, plane.y, 340)]
          return <polygon key={plane.id} className="ontology-layer-plane" points={corners.map(point => `${point.x},${point.y}`).join(' ')} />
        })}
        {edges.map(({ edge, path, active, crossLayer }) => <path key={edge.id} d={path} className="ontology-space-edge" data-active={active} data-cross-layer={crossLayer} strokeDasharray={edge.kind === 'dependency' ? '5 4' : undefined} markerMid={`url(#${markerId})`} />)}
      </svg>
      {planes.map(plane => {
        const label = flat ? { x: 16, y: plane.top } : project(-380, plane.y, -340)
        return <span key={plane.id} className="ontology-layer-caption" style={{ left: `${label.x / 10}%`, top: `${label.y / 6}%` }}>{plane.name} · {plane.members.length}</span>
      })}
      {edges.map(({ edge, x, y, active }) => <button type="button" key={edge.id} className="ontology-space-label" data-active={active}
        style={{ left: `${x / 10}%`, top: `${y / 6}%` }} aria-label={`查看${edge.record.definition.name}，${edge.label}`} onClick={() => onSelect(edge.record.definition.id)}>
        <span>{edge.label}{edge.cardinality && ` · ${edge.cardinality}`}</span>
      </button>)}
      {nodes.map(({ id, record: { definition }, point }) => <button type="button" key={id} className={`ontology-space-node kind-${definition.kind}`}
        style={{ left: `${point.x / 10}%`, top: `${point.y / 6}%`, transform: `translate(-50%, -50%) scale(${point.scale})` }}
        data-layer={layers ? layerOf(id) : undefined} aria-pressed={input.selectedId === id} aria-label={`${kindNames[definition.kind]}：${definition.name}`} title={definition.name} onClick={() => onSelect(id)}>
        <span>{kindNames[definition.kind]}{definition.categoryId !== input.categoryId && ' · 跨分类'}</span><strong>{definition.name}</strong>
      </button>)}
    </div> : <div className="ontology-graph-surface"><div className="ontology-graph-empty">当前范围没有可显示的定义，请调整目录筛选。</div></div>}
    <div className="ontology-graph-footer"><span>{input.mode === 'relations' ? 'Link 业务关系' : '定义引用 · 虚线'}</span><span>{nodes.length} 个节点 · {edges.length} 条连线</span></div>
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120 个节点、240 条连线），可通过搜索、筛选或聚焦缩小范围。</p>}
  </section>
}
