import { useEffect, useId, useImperativeHandle, useMemo, useRef, useState, type PointerEvent, type Ref } from 'react'
import { ZoomIn, ZoomOut } from 'lucide-react'
import type { Organization } from '../../lib/types'
import type { OntologyGraphProps } from './OntologyGraph'
import { buildOntologyGraph, type OntologyGraphEdge } from './ontology-graph'
import './ontology-layer-graph.css'

const graphWidth = 1000
const height = 760
const deckWidth = 680
const deckDepth = 270
const cameras = { iso: [-.32, .53], front: [0, .28], top: [0, Math.PI / 2] } as const
type Camera = readonly [number, number]
type SavedScene = { name: string; start: string; end: string }

function loadScenes(key: string): SavedScene[] {
  try {
    const stored: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
    return Array.isArray(stored) ? stored.filter((item): item is SavedScene =>
      item && typeof item.name === 'string' && typeof item.start === 'string' && typeof item.end === 'string').slice(0, 30) : []
  } catch {
    return []
  }
}

type PathStep = { edge: OntologyGraphEdge; from: string; to: string }

function findPath(edges: OntologyGraphEdge[], from: string, to: string) {
  const adjacency = new Map<string, PathStep[]>()
  for (const edge of edges) {
    if (edge.source === edge.target) continue
    for (const [start, end] of [[edge.source, edge.target], [edge.target, edge.source]]) {
      const steps = adjacency.get(start) ?? []
      steps.push({ edge, from: start, to: end })
      adjacency.set(start, steps)
    }
  }

  const queue = [from]
  const previous = new Map<string, PathStep>()
  for (const node of queue) {
    if (node === to) {
      const path: PathStep[] = []
      let step = previous.get(node)
      while (step) {
        path.push(step)
        step = previous.get(step.from)
      }
      return path.reverse()
    }
    for (const step of adjacency.get(node) ?? []) {
      if (step.to === from || previous.has(step.to)) continue
      previous.set(step.to, step)
      queue.push(step.to)
    }
  }
  return []
}
const schemaLayerSymbols: Record<string, string> = { L1: 'P', L2: 'C', L3: 'T' }
const zoneColors = ['--schema', '--link', '--enum', '--action', '--function', '--success']
const kindNames = { schema: '对象模型', link: '关系', enum: '枚举', action: '动作', function: '函数' }
const unassigned = { id: '', name: '未分层' }

type Props = Pick<OntologyGraphProps, 'records' | 'categories' | 'categoryId' | 'selectedId' | 'rootIds' | 'onSelect'> & {
  layers: { layers: { id: string; name: string }[]; assignments: Record<string, string> }
  groups: Organization[]
  layoutScope: string
  focusRef?: Ref<(id: string) => void>
  hiddenLayers?: ReadonlySet<string>
  focusedGroup?: string
  showZones?: boolean
  showLabels?: boolean
  showAllEdges?: boolean
}

export function OntologyLayerGraph({
  records, categories, categoryId, selectedId, rootIds, onSelect, layers, groups, layoutScope, focusRef,
  hiddenLayers, focusedGroup, showZones = true, showLabels = false, showAllEdges = false,
}: Props) {
  const model = useMemo(() => buildOntologyGraph({ records, categories, categoryId, mode: 'dependencies', selectedId, rootIds, panorama: true, focusedGroup }),
    [records, categories, categoryId, selectedId, rootIds, focusedGroup])
  const [zoom, setZoom] = useState(1)
  const [cameraOverride, setCameraOverride] = useState<Camera>()
  const [viewportSize, setViewportSize] = useState({ width: graphWidth, height })
  const narrow = viewportSize.width < 520
  const defaultCamera = narrow ? cameras.front : cameras.iso
  const camera = cameraOverride ?? defaultCamera
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const drag = useRef<{ x: number; y: number; camera: Camera; pan: typeof pan; translate: boolean }>(undefined)
  const sceneKey = `agentforge:panorama-scenes:${JSON.stringify([layoutScope, categoryId])}`
  const [scenes, setScenes] = useState(() => loadScenes(sceneKey))
  const [sceneName, setSceneName] = useState('')
  const [sceneNotice, setSceneNotice] = useState('')
  const [activeStep, setActiveStep] = useState('')
  function storeScenes(next: SavedScene[]) {
    try {
      localStorage.setItem(sceneKey, JSON.stringify(next))
      setScenes(next)
      setSceneNotice('场景列表已更新。')
    } catch {
      setSceneNotice('浏览器无法保存场景。')
    }
  }
  const [pathOpen, setPathOpen] = useState(false)
  const [pathStart, setPathStart] = useState('')
  const [pathEnd, setPathEnd] = useState('')
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
    const observer = new ResizeObserver(([entry]) => {
      if (entry?.contentRect.width && entry.contentRect.height) setViewportSize({ width: entry.contentRect.width, height: entry.contentRect.height })
    })
    observer.observe(area)
    area.addEventListener('wheel', zoomOnWheel, { passive: false })
    return () => {
      observer.disconnect()
      area.removeEventListener('wheel', zoomOnWheel)
    }
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
  const startId = placed.has(pathStart) ? pathStart : ''
  const endId = placed.has(pathEnd) ? pathEnd : ''
  const pathReady = pathOpen && !!startId && !!endId && startId !== endId
  const pathSteps = pathReady ? findPath(model.edges.filter(edge => placed.has(edge.source) && placed.has(edge.target)), startId, endId) : []
  const pathEdges = new Set(pathSteps.map(step => step.edge.id))
  const stepsByEdge = new Map(pathSteps.map(step => [step.edge.id, step]))
  const pathNodes = new Set(pathSteps.flatMap(step => [step.from, step.to]))
  const pathOptions = pathOpen ? [...nodes].sort((a, b) => a.record.definition.name.localeCompare(b.record.definition.name, 'zh-CN')) : []
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
    const onPath = pathEdges.has(edge.id)
    const active = pathSteps.length ? onPath : selected || related(hoveredId)
    const step = stepsByEdge.get(edge.id)
    const reverse = !!step && step.from !== edge.source
    const direction = selectedId === edge.target && selectedId !== edge.source ? 'incoming'
      : selectedId === edge.source ? 'outgoing' : 'related'
    const labelVisible = !pathSteps.length && (edge.record.definition.id === selectedId && selected || nodes.length <= 12 && active)
    const inFocus = inGroup(edge.source) || inGroup(edge.target)
    const path = loop
      ? `M ${from.x - 18} ${from.y} Q ${x - 90} ${y}, ${x} ${y} Q ${x + 90} ${y}, ${to.x + 18} ${to.y}`
      : `M ${from.x} ${from.y} Q ${(from.x + x) / 2} ${y}, ${x} ${y} Q ${(to.x + x) / 2} ${y}, ${to.x} ${to.y}`
    return [{ edge, x: x + (!loop && Math.abs(from.x - to.x) < 50 ? 28 : 0), y, selected, active, onPath, inFocus, path, labelVisible, reverse, direction,
      crossLayer: layerOf(edge.source) !== layerOf(edge.target) }]
  })
  const neighbors = new Set(edges.filter(item => item.active || item.inFocus).flatMap(({ edge }) => [edge.source, edge.target]))
  const position = (x: number, y: number) => ({ left: `${x / graphWidth * 100}%`, top: `${y / height * 100}%` })

  const scaleX = viewportSize.width * zoom / graphWidth
  const scaleY = viewportSize.height * zoom / height
  type LabelBox = { x: number; y: number; width: number; height: number }
  const labelBoxes: LabelBox[] = nodes.map(node => ({ x: node.x * scaleX - 10, y: node.y * scaleY - 10, width: 20, height: 20 }))
  function reserveLabel(x: number, y: number, text: string, maxWidth: number, priority = false) {
    const labelWidth = Math.min(maxWidth, 12 + Array.from(text).reduce((sum, ch) => sum + (ch.charCodeAt(0) > 255 ? 11 : 6), 0))
    const box = { x: x * scaleX - labelWidth / 2, y: y * scaleY, width: labelWidth, height: 18 }
    const intersects = labelBoxes.some(other => box.x < other.x + other.width + 3 && box.x + box.width + 3 > other.x
      && box.y < other.y + other.height + 3 && box.y + box.height + 3 > other.y)
    if (intersects && !priority) return false
    labelBoxes.push(box)
    return true
  }

  const visibleNames = new Set<string>()
  const labelLimit = narrow ? 72 : 112
  const namedNodes = [...nodes].sort((left, right) =>
    Number(right.id === selectedId || right.id === hoveredId) - Number(left.id === selectedId || left.id === hoveredId))
  for (const node of namedNodes) {
    const priority = node.id === selectedId || node.id === hoveredId
    if (!(priority || nodes.length <= 12 || !pathSteps.length && (showLabels || zoom >= (narrow ? 1.5 : 1.25)))) continue
    if (reserveLabel(node.x, node.y + 14 / scaleY, node.record.definition.name, labelLimit, priority)) visibleNames.add(node.id)
  }
  const upperDeck = decks.at(-1)
  const zoneLabels = showZones && upperDeck ? upperDeck.zones.flatMap(zone => {
    if (!zone.name || narrow && zone.id !== focusedGroup) return []
    const anchor = project(zone.left + groupWidth / 2, upperDeck.elevation, -deckDepth / 2)
    const y = anchor.y - 22 / scaleY
    if (!reserveLabel(anchor.x, y - 8 / scaleY, zone.name, narrow ? 72 : 126)) return []
    return [{ ...zone, x: anchor.x, y }]
  }) : []

  function focusPoint(node: { x: number; y: number }) {
    setZoom(current => Math.max(1.4, current))
    requestAnimationFrame(() => {
      const area = viewport.current
      const content = stage.current
      if (!area || !content) return
      area.scrollTo?.(node.x / graphWidth * content.clientWidth - area.clientWidth / 2, node.y / height * content.clientHeight - area.clientHeight / 2)
    })
  }

  function focusNode(id: string) {
    const node = placed.get(id)
    if (node) focusPoint(node)
  }

  useImperativeHandle(focusRef, () => focusNode)

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
      setCameraOverride([current.camera[0] + dx / 240, Math.max(.12, Math.min(1.56, current.camera[1] + dy / 240))])
    }
  }

  return <section className="ontology-graph" aria-label="分层全景画布">
    <div ref={viewport} className="ontology-layer-viewport panorama-viewport" aria-label="立体分层图" tabIndex={0}
      onPointerDown={startDrag} onPointerMove={moveDrag} onLostPointerCapture={() => { drag.current = undefined }}
      onContextMenu={event => event.preventDefault()}>
      {lanes.length > 0 ? <div ref={stage} className="ontology-lanes panorama-stage" style={{ width: `${zoom * 100}%`, height: `${zoom * 100}%` }}>
        <svg viewBox={`0 0 ${graphWidth} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          <defs><marker id={markerId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 Z" fill="context-stroke" /></marker><marker id={`${markerId}-reverse`} viewBox="0 0 10 10" refX="5" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 10 0 L 0 5 L 10 10 Z" fill="var(--primary)" /></marker></defs>
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
          {edges.map(({ edge, path, active, onPath, inFocus, crossLayer, reverse, direction }) => <path key={edge.id} d={path} className="ontology-layer-edge" data-active={active} data-path={onPath} data-step={onPath && activeStep === edge.id} data-direction={active && !onPath ? direction : undefined} data-cross-layer={crossLayer}
            data-connection={edge.kind}
            data-emphasis={!active ? (pathSteps.length ? 'muted' : inFocus ? 'group' : showAllEdges ? 'all' : focusedGroup || selectedId || hoveredId ? 'muted' : undefined) : undefined}
            strokeDasharray={edge.kind === 'dependency' ? '5 4' : undefined} markerMid={active ? `url(#${markerId}${reverse ? '-reverse' : ''})` : undefined} />)}
          {edges.filter(item => !pathSteps.length && item.selected).slice(0, 16).map(({ edge, path }) => <circle key={edge.id} r={3.4} className="panorama-flow">
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
        {zoneLabels.map(zone => <span key={zone.id}
          className="panorama-zone-name" data-muted={!!focusedGroup && zone.id !== focusedGroup}
          style={{ ...position(zone.x, zone.y), color: `var(${zone.color})` }}>{zone.name}</span>)}
        {edges.map(({ edge, x, y, labelVisible }) => <button type="button" key={edge.id} className="ontology-layer-label" data-active={labelVisible}
          style={position(x, y)} aria-label={`查看${edge.record.definition.name}，${edge.label}`} onClick={() => onSelect(edge.record.definition.id)}>
          <span>{edge.label}{edge.cardinality && ` · ${edge.cardinality}`}</span>
        </button>)}
        {nodes.map(({ id, record: { definition }, x, y }) => <button type="button" key={id} className="ontology-layer-node"
          style={position(x, y)} data-panorama-kind={definition.kind} data-layer={layerOf(id)}
          data-muted={(pathSteps.length ? !pathNodes.has(id) : !!focusedGroup && !inGroup(id) && !neighbors.has(id)) && id !== selectedId && id !== hoveredId}
          data-path={pathNodes.has(id)}
          data-emphasis={visibleNames.has(id) ? 'label' : undefined}
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
          <button key={view} type="button" aria-pressed={camera === cameras[view]} onClick={() => { setCameraOverride(cameras[view]); setPan({ x: 0, y: 0 }) }}>{name}</button>)}
        <button type="button" onClick={() => { setCameraOverride(undefined); setPan({ x: 0, y: 0 }); setZoom(1); viewport.current?.scrollTo?.(0, 0) }}>复位</button>
        <button type="button" disabled={!selectedId || !placed.has(selectedId)} onClick={() => selectedId && focusNode(selectedId)}>聚焦节点</button>
        <button type="button" aria-label="缩小分层画布" disabled={zoom <= 1} onClick={() => setZoom(current => Math.max(1, current - .25))}><ZoomOut size={15} aria-hidden="true" /></button>
        <button type="button" aria-label="放大分层画布" disabled={zoom >= 2} onClick={() => setZoom(current => Math.min(2, current + .25))}><ZoomIn size={15} aria-hidden="true" /></button>
      </div>
      <small>拖动旋转 · 滚轮缩放 · Shift 拖动平移</small>
    </div>
    <div className="panorama-path">
      <button type="button" className="button subtle" aria-expanded={pathOpen} onClick={() => {
        if (!pathOpen && !startId && selectedId && placed.has(selectedId)) setPathStart(selectedId)
        setPathOpen(!pathOpen)
      }}>{pathOpen ? '收起场景推演' : '场景推演'}</button>
      {pathOpen && <div className="panorama-path-body">
        <div className="panorama-path-controls">
          {([['起点', startId, endId, setPathStart], ['终点', endId, startId, setPathEnd]] as const).map(([label, value, other, update]) =>
            <label key={label}>{label}<select className="field" value={value} onChange={event => { setActiveStep(''); update(event.target.value) }}>
              <option value="">选择节点</option>
              {pathOptions.map(({ id, record }) => <option key={id} value={id} disabled={id === other}>{record.definition.name} · {kindNames[record.definition.kind]}</option>)}
            </select></label>)}
        </div>
        <div role="status">{pathReady ? pathSteps.length ? `最短链路 · ${pathSteps.length} 条关联` : '当前可见节点间没有连通链路。' : '选择起点和终点，在图中查看定义关联。'}</div>
        <div className="panorama-scene-controls">
          <label>场景名称<input className="field" value={sceneName} maxLength={60} onChange={event => setSceneName(event.target.value)} /></label>
          <button type="button" className="button subtle" disabled={!pathSteps.length || !sceneName.trim() || scenes.length >= 30}
            onClick={() => { storeScenes([...scenes, { name: sceneName.trim(), start: startId, end: endId }]); setSceneName('') }}>保存场景</button>
        </div>
        {!!scenes.length && <ul className="panorama-saved-scenes" aria-label="已保存场景">{scenes.map((scene, index) => <li key={index}>
          <button type="button" className="button subtle" onClick={() => {
            setActiveStep(''); setPathStart(scene.start); setPathEnd(scene.end)
            setSceneNotice(placed.has(scene.start) && placed.has(scene.end) ? `已打开：${scene.name}` : '场景端点不在当前可见范围。')
          }}>{scene.name}</button>
          <button type="button" className="button subtle" aria-label={`删除场景：${scene.name}`}
            onClick={() => storeScenes(scenes.filter((_, offset) => offset !== index))}>删除</button>
        </li>)}</ul>}
        {sceneNotice && <div role="status">{sceneNotice}</div>}
        {!!pathSteps.length && <ol aria-label="推演链路">
          {pathSteps.map(({ edge, from, to }) => <li key={edge.id}>
            <button type="button" className="button subtle" onClick={() => onSelect(from)}>{placed.get(from)?.record.definition.name}</button>
            <button type="button" className="button subtle panorama-path-edge" aria-pressed={activeStep === edge.id} onClick={() => {
              const segment = edges.find(item => item.edge.id === edge.id)
              setActiveStep(edge.id)
              if (segment) focusPoint(segment)
              onSelect(edge.record.definition.id)
            }}>
              {from === edge.source ? edge.label : edge.reverseLabel ?? `反向查看：${edge.label}`}
            </button>
            <button type="button" className="button subtle" onClick={() => onSelect(to)}>{placed.get(to)?.record.definition.name}</button>
          </li>)}
        </ol>}
      </div>}
    </div>
    <div className="ontology-graph-footer"><span>实线 · Link　虚线 · 动作与函数关联</span><span>{nodes.length} 个节点 · {edges.length} 条连线</span></div>
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120 个节点、240 条连线），可通过搜索、筛选或聚焦缩小范围。</p>}
  </section>
}
