import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { RotateCcw, RotateCw, ZoomIn, ZoomOut } from 'lucide-react'
import { buildOntologyGraph } from './ontology-graph'
import type { OntologyGraphProps } from './OntologyGraph'
import type { Organization } from '../../lib/types'
import type { OntologyLayers } from './OntologyLayers'
import './ontology-graph-3d.css'

const initialCamera = { yaw: -.25, pitch: .38, zoom: 1, x: 0, y: 0 }
const depthByKind = { schema: 0, link: 1, enum: -1, action: 1, function: 2 }
const kindNames = { schema: '对象模型', link: '关系', enum: '枚举', action: '动作', function: '函数' }
const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(max, value))

export function OntologyGraph3D({ flat = false, panorama = false, groups = [], layers, layerFocus, onLayerFocus, onSelect, ...input }: OntologyGraphProps & {
  flat?: boolean; panorama?: boolean; groups?: Organization[]; layers?: OntologyLayers; layerFocus?: string; onLayerFocus?: (id?: string) => void
}) {
  const { records, categories, categoryId, mode, selectedId, focusId, rootIds } = input
  const model = useMemo(() => buildOntologyGraph({ records, categories, categoryId, mode, selectedId, focusId, rootIds, panorama }),
    [records, categories, categoryId, mode, selectedId, focusId, rootIds, panorama])
  const defaultCamera = panorama ? { ...initialCamera, yaw: -.46, pitch: .29, zoom: .82 } : initialCamera
  const [camera, setCamera] = useState(defaultCamera)
  const drag = useRef<{ id: number; x: number; y: number; pan: boolean } | null>(null)
  const surface = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const element = surface.current
    if (!panorama || !element) return
    function onWheel(event: WheelEvent) {
      if (!event.ctrlKey && document.activeElement !== element) return
      event.preventDefault()
      setCamera(current => ({ ...current, zoom: clamp(current.zoom * (event.deltaY < 0 ? 1.08 : .92), .55, 1.75) }))
    }
    element.addEventListener('wheel', onWheel, { passive: false })
    return () => element.removeEventListener('wheel', onWheel)
  }, [panorama])
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
    const vertical = y * cosPitch + rotatedDepth * sinPitch * (panorama ? 1 : -1)
    const depth = y * sinPitch * (panorama ? -1 : 1) + rotatedDepth * cosPitch
    const scale = 1250 / (1250 - depth * .55)
    return { x: 500 + camera.x + horizontal * scale * camera.zoom, y: 300 + camera.y + vertical * scale * camera.zoom, depth, scale }
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
    .filter(layer => (buckets.has(layer.id) || panorama && layer.id !== unassigned) && (layerFocus === undefined || layerFocus === layer.id)) : []
  const lanes = visibleLayers.map((layer, index) => {
    const members = buckets.get(layer.id) ?? []
    const divisions = new Map<string, typeof members>()
    if (panorama) for (const node of members) {
      const group = node.record.definition.groupId
      const entries = divisions.get(group) ?? []
      entries.push(node)
      divisions.set(group, entries)
    }
    const zones = [...divisions].sort(([left], [right]) => left.localeCompare(right)).map(([id, entries], offset) => {
      const width = 708 / divisions.size
      const left = -354 + offset * width
      const columns = Math.min(entries.length, Math.max(1, Math.floor(width / 120)))
      return { id, entries, columns, left, width, rows: Math.ceil(entries.length / columns) }
    })
    const columns = Math.max(1, Math.min(4, members.length))
    const rows = Math.max(1, Math.ceil(members.length / columns))
    const y = visibleLayers.length === 1 ? 0 : -(panorama ? 165 : 200) + index * (panorama ? 345 : 400) / (visibleLayers.length - 1)
    return { ...layer, members, columns, rows, y, zones }
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
    const zone = lane.zones.find(area => area.entries.includes(node))
    const zoneIndex = zone?.entries.indexOf(node) ?? 0
    const x = zone ? zone.left + (zoneIndex % zone.columns + .5) * zone.width / zone.columns : (column - (lane.columns - 1) / 2) * 174
    const z = zone ? -255 + (Math.floor(zoneIndex / zone.columns) + .5) * 510 / zone.rows : (row - (lane.rows - 1) / 2) * Math.min(95, 660 / lane.rows)
    const point = flat
      ? { x: (column + .5) * 1000 / lane.columns, y: lane.top + (row + .65) * (lane.bottom - lane.top) / lane.rows, depth: 0, scale: 1 }
      : project(x, lane.y, z)
    return { ...node, point }
  })) : model.nodes.map(node => ({
    ...node,
    point: project((node.position.x - middleX) * 660 / unit, (node.position.y - middleY) * 660 / unit,
      depthByKind[node.record.definition.kind] * 84 + Math.sin(node.position.y / 170) * 62),
  }))).sort((a, b) => a.point.depth - b.point.depth)
  const slabCorners = [[-380, -340], [380, -340], [380, 340], [-380, 340]]
  const coordinates = (points: { x: number; y: number }[]) => points.map(point => `${point.x},${point.y}`).join(' ')
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
    {layers && <div className="ontology-graph-toolbar"><label>聚焦层 <select className={panorama ? "field" : undefined} value={layerFocus === undefined ? 'all' : `layer:${layerFocus}`} onChange={event => {
      onLayerFocus?.(event.target.value === 'all' ? undefined : event.target.value.slice(6))
    }}><option value="all">全部层</option>{[...layers.layers, { id: '', name: '未分层' }].map(layer => <option key={layer.id} value={`layer:${layer.id}`}>{layer.name}</option>)}</select></label></div>}
    {!flat && <div className="ontology-graph-toolbar"><span>{panorama ? '拖动旋转 · Shift 拖动平移 · 聚焦后滚轮缩放' : '拖动画布旋转'}</span>
      <div role="group" aria-label="三维画布操作">
        <button type="button" aria-label="向左旋转" onClick={() => orbit(-.28)}><RotateCcw size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="向右旋转" onClick={() => orbit(.28)}><RotateCw size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="放大三维画布" onClick={() => zoom(.15)}><ZoomIn size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="缩小三维画布" onClick={() => zoom(-.15)}><ZoomOut size={15} aria-hidden="true" /></button>
        <button type="button" onClick={() => setCamera(defaultCamera)}>复位</button>
        {panorama && <><button type="button" onClick={() => setCamera({ ...defaultCamera, yaw: 0, pitch: .06 })}>正视</button>
          <button type="button" onClick={() => setCamera({ ...defaultCamera, yaw: 0, pitch: 1.2 })}>俯视</button></>}
      </div>
    </div>}
    {(nodes.length > 0 || panorama) ? <div ref={surface} className={`ontology-graph-surface ontology-space${flat ? ' ontology-space-flat' : ''}`}
      style={flat ? { height: Math.max(420, totalRows * 86) } : undefined} aria-label={flat ? '二维泳道' : '三维视角，方向键旋转'} tabIndex={0}
      onKeyDown={event => {
        if (flat || event.target !== event.currentTarget) return
        const step = { ArrowLeft: [-.2, 0], ArrowRight: [.2, 0], ArrowUp: [0, .15], ArrowDown: [0, -.15] }[event.key]
        if (!step) return
        event.preventDefault()
        orbit(step[0], step[1])
      }}
      onDoubleClick={event => {
        const node = (event.target as Element).closest<HTMLButtonElement>('[data-panorama-node]')
        if (panorama && node?.dataset.panoramaNode) input.onFocus(node.dataset.panoramaNode)
      }}
      onPointerDown={event => {
        if (flat || event.button !== 0 || !event.isPrimary || (event.target as Element).closest('button')) return
        if (panorama) event.currentTarget.focus({ preventScroll: true })
        event.currentTarget.setPointerCapture(event.pointerId)
        drag.current = { id: event.pointerId, x: event.clientX, y: event.clientY, pan: panorama && event.shiftKey }
      }}
      onPointerMove={event => {
        const previous = drag.current
        if (!previous || previous.id !== event.pointerId) return
        const dx = event.clientX - previous.x, dy = event.clientY - previous.y
        if (previous.pan) {
          const { clientWidth, clientHeight } = event.currentTarget
          setCamera(current => ({ ...current, x: current.x + dx * 1000 / clientWidth, y: current.y + dy * 600 / clientHeight }))
        }
        else orbit(dx * .008, dy * .008)
        drag.current = { ...previous, x: event.clientX, y: event.clientY }
      }}
      onLostPointerCapture={() => { drag.current = null }}>
      <svg viewBox="0 0 1000 600" preserveAspectRatio="none" aria-hidden="true">
        <defs><marker id={markerId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 Z" fill="var(--edge)" /></marker></defs>
        {!layers && <path d={grid} className="ontology-space-grid" />}
        {panorama && planes.length > 1 && slabCorners.map(([x, z]) => {
          const top = project(x, planes[0].y, z)
          const bottom = project(x, planes[planes.length - 1].y + 11, z)
          return <line key={`${x}:${z}`} className="panorama-pillar" x1={top.x} x2={bottom.x} y1={top.y} y2={bottom.y} />
        })}
        {planes.map(plane => {
          const corners = flat ? [{ x: 0, y: plane.top }, { x: 1000, y: plane.top }, { x: 1000, y: plane.bottom }, { x: 0, y: plane.bottom }]
            : slabCorners.map(([x, z]) => project(x, plane.y, z))
          return <g key={plane.id} data-slab={panorama ? plane.id : undefined}>
            {panorama && [[2, 3], camera.yaw < 0 ? [1, 2] : [3, 0]].map(([a, b]) => <polygon key={a} className="panorama-slab-wall"
              points={coordinates([corners[a], corners[b], ...[b, a].map(index => project(slabCorners[index][0], plane.y + 10, slabCorners[index][1]))])} />)}
            <polygon className="ontology-layer-plane" data-panorama-layer={panorama ? plane.id : undefined} points={coordinates(corners)} />
            {plane.zones.map(zone => {
              const left = zone.left + 5, right = zone.left + zone.width - 5
              const pad = [project(left, plane.y, -298), project(right, plane.y, -298), project(right, plane.y, 298), project(left, plane.y, 298)]
              const label = project((left + right) / 2, plane.y, 310)
              return <g key={zone.id}><polygon className="panorama-zone" points={pad.map(point => `${point.x},${point.y}`).join(' ')} />
                <text className="panorama-zone-name" x={label.x} y={label.y} textAnchor="middle">{groups.find(group => group.id === zone.id)?.name}</text></g>
            })}
          </g>
        })}
        {edges.map(({ edge, path, active, crossLayer }) => <path key={edge.id} d={path} className="ontology-space-edge" data-active={active} data-cross-layer={crossLayer} strokeDasharray={edge.kind === 'dependency' ? '5 4' : undefined} markerMid={`url(#${markerId})`} />)}
      </svg>
      {planes.map(plane => {
        const label = flat ? { x: 16, y: plane.top } : panorama ? project(-385, plane.y, -45) : project(-380, plane.y, -340)
        return <span key={plane.id} className="ontology-layer-caption" data-slab={panorama ? plane.id : undefined} style={{ left: `${label.x / 10}%`, top: `${label.y / 6}%` }}>{panorama ? <><b>{plane.id === 'pending' ? '待分层' : plane.id}</b><small>{plane.id === 'M0' ? '预留' : `${plane.members.length} 项`}</small></> : `${plane.name} · ${plane.members.length}`}</span>
      })}
      {edges.map(({ edge, x, y, active }) => <button type="button" key={edge.id} className="ontology-space-label" data-active={active}
        style={{ left: `${x / 10}%`, top: `${y / 6}%` }} aria-label={`查看${edge.record.definition.name}，${edge.label}`} onClick={() => onSelect(edge.record.definition.id)}>
        <span>{edge.label}{edge.cardinality && ` · ${edge.cardinality}`}</span>
      </button>)}
      {nodes.map(({ id, record: { definition }, point }) => <button type="button" key={id} className={`ontology-space-node kind-${definition.kind}`}
        style={{ left: `${point.x / 10}%`, top: `${point.y / 6}%`, transform: `translate(-50%, -50%) scale(${panorama ? Math.min(1.17, point.scale) : point.scale})` }}
        data-panorama-kind={panorama ? definition.kind : undefined} data-panorama-node={panorama ? id : undefined} data-layer={layers ? layerOf(id) : undefined} aria-pressed={input.selectedId === id} aria-label={`${kindNames[definition.kind]}：${definition.name}`} title={definition.name} onClick={() => onSelect(id)}>
        <span aria-hidden={panorama || undefined}>{panorama ? (definition.kind === 'function' ? 'ƒ' : definition.kind === 'action' ? 'A' : '') : kindNames[definition.kind]}{!panorama && definition.categoryId !== input.categoryId && ' · 跨分类'}</span><strong>{definition.name}</strong>
      </button>)}
    </div> : <div className="ontology-graph-surface"><div className="ontology-graph-empty">当前范围没有可显示的定义，请调整目录筛选。</div></div>}
    <div className="ontology-graph-footer"><span>{panorama ? '实线 · Link　虚线 · 动作与函数关联' : input.mode === 'relations' ? 'Link 业务关系' : '定义引用 · 虚线'}</span><span>{nodes.length} 个节点 · {edges.length} 条连线</span></div>
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120 个节点、240 条连线），可通过搜索、筛选或聚焦缩小范围。</p>}
  </section>
}
