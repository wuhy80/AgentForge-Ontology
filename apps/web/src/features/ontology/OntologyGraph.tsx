import { useEffect, useMemo, useRef } from 'react'
import { Background, BackgroundVariant, BaseEdge, EdgeLabelRenderer, Handle, MarkerType, Position, ReactFlow, ReactFlowProvider, useEdgesState, useNodesInitialized, useNodesState, useReactFlow, type Edge, type EdgeProps, type Node, type NodeProps, type Viewport } from '@xyflow/react'
import { Box, Braces, GitBranch, List, Maximize2, RotateCcw, ScanSearch, Waypoints, Workflow, ZoomIn, ZoomOut } from 'lucide-react'
import type { DefinitionRecord, Kind, Organization } from '../../lib/types'
import { arrangeRelationNodes, parseRelationLayout } from '../model/relation-graph'
import { buildOntologyGraph, type OntologyGraphEdge, type OntologyGraphMode, type OntologyGraphModel } from './ontology-graph'
import '@xyflow/react/dist/style.css'
import './ontology-graph.css'

export interface OntologyGraphProps {
  records: DefinitionRecord[]
  categories: Organization[]
  categoryId: string
  mode: OntologyGraphMode
  selectedId?: string
  focusId?: string
  rootIds?: string[]
  onSelect: (id: string) => void
  onFocus: (id?: string) => void
  layoutKey: string
}

const names: Record<Kind, string> = { schema: '对象模型', link: '关系', enum: '枚举', action: '动作', function: '函数' }
const icons = { schema: Box, link: GitBranch, enum: List, action: Workflow, function: Braces }
type OntologyNode = Node<{ record: DefinitionRecord; categoryName?: string; external: boolean; related: boolean }, 'ontology'>
type OntologyEdge = Edge<{ model: OntologyGraphEdge; active: boolean; onSelect: (id: string) => void }, 'ontology'>

function DefinitionNode({ data, selected }: NodeProps<OntologyNode>) {
  const definition = data.record.definition
  const Icon = icons[definition.kind]
  const signature = definition.schema ? `${definition.schema.properties.length} 个属性`
    : definition.enum ? `${definition.enum.values.length} 个成员`
      : definition.link ? definition.link.cardinality
        : `${(definition.action ?? definition.function)?.input?.length ?? 0} 个输入 · ${(definition.action ?? definition.function)?.output?.length ?? 0} 个输出`
  return <div className={`ontology-graph-node kind-${definition.kind}${selected ? ' is-selected' : ''}${data.related ? ' is-related' : ''}`}>
    <Handle type="target" id="target" position={Position.Left} isConnectable={false} />
    <div className="ontology-graph-node-title"><span className="ontology-graph-kind-icon"><Icon size={19} /></span><strong title={definition.name}>{definition.name}</strong></div>
    <div className="ontology-graph-node-meta"><span>{names[definition.kind]}</span><span>{signature}</span></div>
    {data.external && <small className="ontology-graph-external" title={data.categoryName}>跨分类 · {data.categoryName ?? '其他分类'}</small>}
    <Handle type="source" id="source" position={Position.Right} isConnectable={false} />
  </div>
}

function DefinitionEdge({ id, sourceX, sourceY, targetX, targetY, source, target, markerEnd, data, style }: EdgeProps<OntologyEdge>) {
  if (!data) return null
  const { model, active } = data
  const loop = source === target
  const reach = Math.max(95, Math.abs(targetX - sourceX) * .45)
  const lift = 120 + Math.abs(model.offset)
  const path = loop ? `M ${sourceX} ${sourceY} C ${sourceX + 130} ${sourceY - lift}, ${targetX - 130} ${targetY - lift}, ${targetX} ${targetY}`
    : `M ${sourceX} ${sourceY} C ${sourceX + reach} ${sourceY + model.offset}, ${targetX - reach} ${targetY + model.offset}, ${targetX} ${targetY}`
  return <>
    <BaseEdge id={id} path={path} markerEnd={markerEnd} style={{ ...style, strokeWidth: active ? 2.6 : 1.6 }} interactionWidth={26} />
    <EdgeLabelRenderer><button type="button" className={`ontology-graph-edge-label nodrag nopan${active ? ' is-active' : ''}${model.kind === 'dependency' ? ' is-dependency' : ''}`}
      style={{ transform: `translate(-50%, -50%) translate(${(sourceX + targetX) / 2}px,${(sourceY + targetY) / 2 + (loop ? -lift * .75 : model.offset * .75)}px)` }}
      aria-label={model.kind === 'relation' ? `选择关系：${model.record.definition.name}` : `选择引用来源：${model.record.definition.name}，${model.label}`}
      onClick={event => { event.stopPropagation(); data.onSelect(model.record.definition.id) }}>
      <span><strong title={model.label}>{model.label}</strong>{model.cardinality && <b>{model.cardinality}</b>}</span>
      {model.kind === 'relation' && <small title={model.reverseLabel}>反向：{model.reverseLabel || '未填写'}</small>}
    </button></EdgeLabelRenderer>
  </>
}

const nodeTypes = { ontology: DefinitionNode }
const edgeTypes = { ontology: DefinitionEdge }

export function OntologyGraph(props: OntologyGraphProps) {
  return <ReactFlowProvider><OntologyGraphContent {...props} /></ReactFlowProvider>
}

function OntologyGraphContent({ records, categories, categoryId, mode, selectedId, focusId, rootIds, onSelect, onFocus, layoutKey }: OntologyGraphProps) {
  const model = useMemo(() => buildOntologyGraph({ records, categories, categoryId, mode, selectedId, focusId, rootIds }), [records, categories, categoryId, mode, selectedId, focusId, rootIds])
  const [nodes, setNodes, onNodesChange] = useNodesState<OntologyNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<OntologyEdge>([])
  const flow = useReactFlow<OntologyNode, OntologyEdge>()
  const initialized = useNodesInitialized()
  const latestModel = useRef<OntologyGraphModel>(model); latestModel.current = model
  const selectedRecord = records.find(record => record.definition.id === selectedId)
  const storageName = `agentforge:ontology-layout:${layoutKey}:${categoryId}:${mode}`
  const scope = `${storageName}:${focusId ?? ''}`
  const previousScope = useRef('')
  const previousMembership = useRef('')
  const initialViewport = useRef<Viewport | undefined>(undefined)
  const viewportPending = useRef(true)
  const pendingSelection = useRef(selectedId)
  const visibleRecords = useMemo(() => new Set(records.filter(record => record.status === 'active' && record.definition.tenantId === categories.find(category => category.id === categoryId)?.tenantId).map(record => record.definition.id)), [records, categories, categoryId])
  const selectedEdge = model.edges.find(edge => edge.record.definition.id === selectedId && edge.kind === 'relation')
  const relatedIds = new Set<string>()
  for (const edge of model.edges) if (edge.source === selectedId || edge.target === selectedId || edge === selectedEdge) { relatedIds.add(edge.source); relatedIds.add(edge.target) }

  useEffect(() => {
    const scopeChanged = previousScope.current !== scope
    previousScope.current = scope
    const membership = JSON.stringify(model.nodes.map(node => node.id))
    const membershipChanged = previousMembership.current !== membership
    previousMembership.current = membership
    let stored: ReturnType<typeof parseRelationLayout>
    try {
      const existing = parseRelationLayout(localStorage.getItem(storageName), visibleRecords)
      if (scopeChanged) stored = existing
      if (existing) localStorage.setItem(storageName, JSON.stringify({ version: 1, ...existing }))
    } catch { /* Layout is an optional preference. */ }
    if (scopeChanged || membershipChanged) {
      initialViewport.current = scopeChanged && !focusId ? stored?.viewport : undefined
      viewportPending.current = true
    }
    setNodes(previous => {
      const existing = new Map(previous.map(node => [node.id, node]))
      return model.nodes.map(node => ({
        // Retain measured dimensions reported by ReactFlow. Recreating them
        // during selection changes causes ResizeObserver feedback loops.
        ...(!scopeChanged ? existing.get(node.id) : undefined),
        id: node.id, type: 'ontology', position: stored?.positions[node.id] ?? (!scopeChanged ? existing.get(node.id)?.position : undefined) ?? node.position,
        selected: node.id === selectedId, ariaLabel: `${names[node.record.definition.kind]}：${node.record.definition.name}`,
        draggable: true, connectable: false, selectable: true, focusable: true,
        data: { record: node.record, external: node.record.definition.categoryId !== categoryId,
          categoryName: categories.find(category => category.id === node.record.definition.categoryId)?.name, related: relatedIds.has(node.id) },
      }))
    })
    setEdges(model.edges.map(edge => {
      const active = edge.record.definition.id === selectedId && edge.kind === 'relation' || edge.source === selectedId || edge.target === selectedId
      const color = active ? 'var(--accent)' : edge.kind === 'relation' ? 'var(--edge)' : 'var(--edge-dependency)'
      return { id: edge.id, source: edge.source, target: edge.target, sourceHandle: 'source', targetHandle: 'target', type: 'ontology',
        selectable: true, focusable: true, selected: edge.record.definition.id === selectedId,
        ariaLabel: edge.kind === 'relation' ? `关系：${edge.record.definition.name}` : `${edge.record.definition.name}：${edge.label}`,
        markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16, color },
        style: { stroke: color, ...(edge.kind === 'dependency' ? { strokeDasharray: '5 4' } : {}) },
        data: { model: edge, active, onSelect },
      }
    }))
  // relatedIds is derived from model and selection; storing it as a dependency
  // would invalidate this effect at every ReactFlow measurement update.
  }, [model, categories, categoryId, selectedId, scope, storageName, visibleRecords, onSelect, setNodes, setEdges])

  useEffect(() => { pendingSelection.current = selectedId }, [selectedId])
  useEffect(() => {
    if (!initialized || !model.nodes.length) return
    const current = flow.getNodes()
    if (current.length !== model.nodes.length || model.nodes.some(node => !current.some(item => item.id === node.id && !!item.measured?.width && !!item.measured?.height))) return
    if (viewportPending.current) {
      viewportPending.current = false
      if (initialViewport.current) void flow.setViewport(initialViewport.current)
      else void flow.fitView({ padding: .26, maxZoom: 1.1 })
    }
    const selected = pendingSelection.current
    if (!selected) return
    const node = model.nodes.find(item => item.id === selected)
    const relation = model.edges.find(edge => edge.kind === 'relation' && edge.record.definition.id === selected)
    const ids = node ? [node.id] : relation ? [relation.source, relation.target] : []
    pendingSelection.current = undefined
    if (ids.length) void flow.fitView({ nodes: ids.map(id => ({ id })), padding: .75, maxZoom: 1.1, duration: 160 })
  }, [initialized, model, nodes, flow, selectedId])

  function saveLayout(viewport?: Viewport) {
    const positions: Record<string, { x: number; y: number }> = Object.create(null)
    try {
      const existing = parseRelationLayout(localStorage.getItem(storageName), visibleRecords)
      for (const node of flow.getNodes()) if (visibleRecords.has(node.id)) positions[node.id] = { x: node.position.x, y: node.position.y }
      for (const [id, position] of Object.entries(existing?.positions ?? {})) {
        if (Object.keys(positions).length >= 120) break
        if (!positions[id]) positions[id] = position
      }
      localStorage.setItem(storageName, JSON.stringify({ version: 1, positions, viewport: focusId ? existing?.viewport : viewport ?? flow.getViewport() }))
    } catch { /* A disabled browser store does not disable browsing. */ }
  }
  function arrange(reset = false) {
    const current = latestModel.current
    const positions = arrangeRelationNodes(current.nodes, current.edges, focusId)
    setNodes(previous => previous.map(node => ({ ...node, position: positions[node.id] ?? node.position })))
    if (reset) try { localStorage.removeItem(storageName) } catch { /* Optional preference. */ }
    requestAnimationFrame(() => { void flow.fitView({ padding: .26, maxZoom: 1.1, duration: 180 }).then(() => { if (!reset) saveLayout() }) })
  }

  return <section className="ontology-graph" aria-label={mode === 'relations' ? '本体对象关系画布' : '本体定义依赖画布'}
    onKeyDownCapture={event => {
      if (event.key !== 'Enter' && event.key !== ' ') return
      if (!(event.target instanceof Element) || event.target.closest('button')) return
      const nodeId = event.target.closest('.react-flow__node')?.getAttribute('data-id')
      if (nodeId && model.nodes.some(node => node.id === nodeId)) { event.preventDefault(); event.stopPropagation(); onSelect(nodeId); return }
      const edgeId = event.target.closest('.react-flow__edge')?.getAttribute('data-id')
      const edge = model.edges.find(item => item.id === edgeId)
      if (edge) { event.preventDefault(); event.stopPropagation(); onSelect(edge.record.definition.id) }
    }} onKeyUpCapture={event => { if (event.key.startsWith('Arrow') && event.target instanceof Element && event.target.closest('.react-flow__node')) requestAnimationFrame(() => saveLayout()) }}>
    <div className="ontology-graph-toolbar"><span><GitBranch size={15} />{focusId ? '已聚焦相邻定义' : mode === 'relations' ? '对象与业务关系' : '定义与引用依赖'}</span>
      <div role="group" aria-label="本体画布操作">
        <button type="button" aria-label="放大本体画布" title="放大" onClick={() => void flow.zoomIn({ duration: 120 })}><ZoomIn size={15} /></button>
        <button type="button" aria-label="缩小本体画布" title="缩小" onClick={() => void flow.zoomOut({ duration: 120 })}><ZoomOut size={15} /></button>
        <button type="button" aria-label="适配本体画布" title="适配画布" onClick={() => void flow.fitView({ padding: .26, maxZoom: 1.1, duration: 180 })}><Maximize2 size={15} /></button>
        <button type="button" aria-label="自动布局本体画布" title="自动布局" onClick={() => arrange()}><Waypoints size={15} /></button>
        <button type="button" aria-label="重置本体个人布局" title="重置个人布局" onClick={() => arrange(true)}><RotateCcw size={15} /></button>
      </div>
    </div>
    {mode === 'relations' && selectedRecord && !['schema', 'link'].includes(selectedRecord.definition.kind) && <p className="ontology-graph-hint">当前选择为{names[selectedRecord.definition.kind]}，可在详情中查看信息，或切换“定义依赖图”定位引用。</p>}
    <div className="ontology-graph-surface">{model.nodes.length ? <ReactFlow<OntologyNode, OntologyEdge>
      nodes={nodes} edges={edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes} onNodesChange={onNodesChange} onEdgesChange={onEdgesChange}
      onNodeClick={(_, node) => onSelect(node.id)} onNodeDoubleClick={(_, node) => onFocus(node.id)}
      onEdgeClick={(_, edge) => { if (edge.data) onSelect(edge.data.model.record.definition.id) }}
      onNodeDragStop={() => saveLayout()} onMoveEnd={(_, viewport) => saveLayout(viewport)}
      minZoom={.15} maxZoom={2.5} fitView fitViewOptions={{ padding: .26, maxZoom: 1.1 }}
      nodesConnectable={false} edgesReconnectable={false} deleteKeyCode={null} panOnScroll selectionOnDrag={false} zoomOnDoubleClick={false}
      proOptions={{ hideAttribution: true }} ariaLabelConfig={{ 'node.a11yDescription.default': '按回车查看定义，方向键调整个人布局。', 'edge.a11yDescription.default': '按回车查看关系或引用来源。' }}>
      <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="var(--grid)" />
    </ReactFlow> : <div className="ontology-graph-empty"><ScanSearch size={30} /><strong>当前范围没有可显示的{mode === 'relations' ? '对象关系' : '定义依赖'}</strong><p>可调整目录筛选，或选择其他分类。</p></div>}</div>
    <div className="ontology-graph-footer"><span><i className={mode === 'relations' ? 'ontology-legend-relation' : 'ontology-legend-dependency'} />{mode === 'relations' ? 'Link 业务关系' : '定义引用 · 虚线'}</span><span>当前画布 {model.nodes.length} 个节点 · {model.edges.length} 条连线</span></div>
    {focusId && <div className="ontology-graph-focus"><span>仅显示当前对象的相邻定义</span><button type="button" onClick={() => onFocus(undefined)}>展开当前范围</button></div>}
    {model.unavailableRelations && <p className="ontology-graph-hint" role="status">部分关系端点当前不可查看，相关连线未绘制。</p>}
    {(model.omittedNodes > 0 || model.omittedEdges > 0) && <p className="ontology-graph-budget" role="status">已达到画布显示上限（120个节点、240条连线），当前未显示全部已加载定义。请搜索、筛选或聚焦相邻定义。</p>}
  </section>
}
