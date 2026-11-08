import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Background, BackgroundVariant, BaseEdge, EdgeLabelRenderer, Handle, MarkerType, Position, ReactFlow, ReactFlowProvider, useEdgesState, useNodesInitialized, useNodesState, useReactFlow, type Connection, type Edge, type EdgeProps, type Node, type NodeProps, type Viewport } from '@xyflow/react'
import { Box, GitBranch, LockKeyhole, Maximize2, RotateCcw, Waypoints, ZoomIn, ZoomOut } from 'lucide-react'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { arrangeRelationNodes, buildRelationGraph, parseRelationLayout, type RelationGraphModel } from './relation-graph'
import '@xyflow/react/dist/style.css'
import './relation-graph.css'

export interface RelationGraphProps {
  schemas: DefinitionRecord[]
  links: DefinitionRecord[]
  categories: Organization[]
  categoryId: string
  focusSchemaId?: string
  selectedLinkId?: string
  onSelectLink?: (record: DefinitionRecord) => void
  onSelectSchema?: (record: DefinitionRecord) => void
  onConnect?: (sourceSchemaId: string, targetSchemaId: string) => void
  layoutKey?: string
  compact?: boolean
  disabled?: boolean
}

type SchemaNode = Node<{
  record?: DefinitionRecord
  restricted: boolean
  categoryName?: string
  external: boolean
  canConnect: boolean
}, 'schema'>
type RelationEdge = Edge<{
  record: DefinitionRecord
  offset: number
  restricted: boolean
  onSelect?: (record: DefinitionRecord) => void
}, 'relation'>

const statusText = (record: DefinitionRecord) => record.status === 'active' ? '已生效' : record.hasCurrent ? '待发布变更' : '草稿'

function SchemaGraphNode({ data, selected }: NodeProps<SchemaNode>) {
  if (data.restricted || !data.record) return <div className="relation-schema-node relation-schema-restricted">
    <Handle type="target" id="target" position={Position.Left} isConnectable={false} />
    <LockKeyhole size={17} /><strong>受限对象</strong><small>当前无法查看此端点</small>
    <Handle type="source" id="source" position={Position.Right} isConnectable={false} />
  </div>
  const { definition } = data.record
  return <div className={`relation-schema-node ${selected ? 'is-selected' : ''} ${data.record.status !== 'active' ? 'is-draft' : ''}`}>
    <Handle type="target" id="target" position={Position.Left} isConnectable={data.canConnect} aria-label={`${definition.name}的关系目标连接点`} />
    <div className="relation-schema-heading"><span className="relation-schema-icon"><Box size={18} /></span><strong title={definition.name}>{definition.name}</strong></div>
    <div className="relation-schema-meta"><span>{definition.schema?.properties.length ?? 0} 个属性</span><span className={`relation-state ${data.record.status === 'active' ? 'active' : 'draft'}`}>{statusText(data.record)}</span></div>
    {data.external && <small className="relation-external-category" title={data.categoryName}>来自 {data.categoryName ?? '其他分类'}</small>}
    <Handle type="source" id="source" position={Position.Right} isConnectable={data.canConnect} aria-label={`${definition.name}的关系来源连接点`} />
  </div>
}

function RelationGraphEdge({ id, sourceX, sourceY, targetX, targetY, source, target, markerEnd, data, selected, style }: EdgeProps<RelationEdge>) {
  if (!data) return null
  const relation = data.record.definition.link!
  const loop = source === target
  const reach = Math.max(95, Math.abs(targetX - sourceX) * 0.45)
  const bend = data.offset
  const lift = 120 + Math.abs(bend)
  const path = loop
    ? `M ${sourceX} ${sourceY} C ${sourceX + 130} ${sourceY - lift}, ${targetX - 130} ${targetY - lift}, ${targetX} ${targetY}`
    : `M ${sourceX} ${sourceY} C ${sourceX + reach} ${sourceY + bend}, ${targetX - reach} ${targetY + bend}, ${targetX} ${targetY}`
  const labelX = (sourceX + targetX) / 2
  const labelY = (sourceY + targetY) / 2 + (loop ? -lift * 0.75 : bend * 0.75)
  return <>
    <BaseEdge id={id} path={path} markerEnd={markerEnd} style={{ ...style, strokeWidth: selected ? 2.7 : 1.7 }} interactionWidth={24} />
    <EdgeLabelRenderer><button type="button" className={`relation-edge-label nodrag nopan ${selected ? 'is-selected' : ''} ${data.record.status !== 'active' ? 'is-draft' : ''}`}
      style={{ transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)` }}
      aria-label={`选择关系：${data.record.definition.name}（${statusText(data.record)}）`}
      onClick={(event) => { event.stopPropagation(); data.onSelect?.(data.record) }}>
      <span><strong title={data.record.definition.name}>{relation.forwardName || data.record.definition.name}</strong><b>{relation.cardinality}</b></span>
      <small title={relation.reverseName}>反向：{relation.reverseName || '未填写'} · {statusText(data.record)}</small>
    </button></EdgeLabelRenderer>
  </>
}

const nodeTypes = { schema: SchemaGraphNode }
const edgeTypes = { relation: RelationGraphEdge }

export function RelationGraph(props: RelationGraphProps) {
  return <ReactFlowProvider><RelationGraphContent {...props} /></ReactFlowProvider>
}

function RelationGraphContent({ schemas, links, categories, categoryId, focusSchemaId, selectedLinkId, onSelectLink, onSelectSchema, onConnect, layoutKey, compact = false, disabled = false }: RelationGraphProps) {
  const model = useMemo(() => buildRelationGraph({ schemas, links, categories, categoryId, focusSchemaId, selectedLinkId }), [schemas, links, categories, categoryId, focusSchemaId, selectedLinkId])
  const [selectedSchema, setSelectedSchema] = useState(focusSchemaId)
  const [localLinkId, setLocalLinkId] = useState<string>()
  const [nodes, setNodes, onNodesChange] = useNodesState<SchemaNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<RelationEdge>([])
  const flow = useReactFlow<SchemaNode, RelationEdge>()
  const initialized = useNodesInitialized()
  const latestModel = useRef<RelationGraphModel>(model)
  latestModel.current = model
  const graphScope = `${categoryId}:${layoutKey ?? ''}:${focusSchemaId ?? ''}`
  const previousScope = useRef('')
  const storageName = layoutKey ? `agentforge:relation-layout:${layoutKey}` : undefined
  const initialViewport = useRef<Viewport | undefined>(undefined)
  const viewportPending = useRef(true)
  const selectedLink = selectedLinkId ?? localLinkId
  const selectLink = useCallback((record: DefinitionRecord) => {
    setLocalLinkId(record.definition.id); setSelectedSchema(undefined); onSelectLink?.(record)
  }, [onSelectLink])

  useEffect(() => {
    const scopeChanged = previousScope.current !== graphScope
    previousScope.current = graphScope
    let stored: ReturnType<typeof parseRelationLayout>
    if (storageName) {
      try {
        const existing = parseRelationLayout(localStorage.getItem(storageName), new Set(model.nodes.filter((node) => !node.restricted).map((node) => node.id)))
        if (scopeChanged) stored = existing
        // Coordinates contain no labels, but removed/revoked identities should
        // also leave this local preference at the next directory boundary.
        if (existing) localStorage.setItem(storageName, JSON.stringify({ version: 1, ...existing }))
      } catch { /* Private browsing can disable storage. */ }
    }
    if (scopeChanged) {
      setSelectedSchema(focusSchemaId); setLocalLinkId(undefined)
      initialViewport.current = stored?.viewport
      viewportPending.current = true
    }
    setNodes((previous) => {
      const existing = new Map(previous.map((node) => [node.id, node]))
      return model.nodes.map((node) => ({
        // React Flow returns measured dimensions through onNodesChange. Keep
        // those dimensions when updating labels/selection instead of forcing
        // every unchanged node through unobserve/observe within a resize pass.
        ...(!scopeChanged ? existing.get(node.id) : undefined),
        id: node.id, type: 'schema', position: stored?.positions[node.id] ?? (!scopeChanged ? existing.get(node.id)?.position : undefined) ?? node.position,
        selected: node.id === selectedSchema,
        ariaLabel: node.record ? `对象：${node.record.definition.name}（${statusText(node.record)}）` : '受限对象',
        focusable: !node.restricted, selectable: !node.restricted, draggable: !disabled && !node.restricted,
        connectable: !disabled && !!onConnect && !node.restricted && node.record?.status === 'active',
        data: { record: node.record, restricted: node.restricted,
          categoryName: categories.find((category) => category.id === node.record?.definition.categoryId)?.name,
          external: !!node.record && node.record.definition.categoryId !== categoryId,
          canConnect: !disabled && !!onConnect && !node.restricted && node.record?.status === 'active' },
      }))
    })
    setEdges(model.edges.map((edge) => {
      const current = edge.record.status === 'active'
      const color = edge.restricted ? '#87968e' : current ? '#5c8b75' : '#b08b40'
      return { id: edge.id, source: edge.source, target: edge.target, sourceHandle: 'source', targetHandle: 'target', type: 'relation',
        selected: edge.record.definition.id === selectedLink, selectable: true, focusable: true,
        ariaLabel: `关系：${edge.record.definition.name}（${statusText(edge.record)}）`,
        markerEnd: { type: MarkerType.ArrowClosed, color, width: 18, height: 18 },
        style: { stroke: color, ...(current ? {} : { strokeDasharray: '6 4' }) },
        data: { record: edge.record, offset: edge.offset, restricted: edge.restricted, onSelect: selectLink } }
    }))
    // Model data updates preserve user positions. Selection updates may rebuild
    // node data, but never save or modify the supplied definition records.
  }, [model, categories, categoryId, disabled, onConnect, selectedLink, selectedSchema, selectLink, graphScope, storageName, focusSchemaId, setNodes, setEdges])

  useEffect(() => {
    if (!initialized || !viewportPending.current) return
    viewportPending.current = false
    if (initialViewport.current) void flow.setViewport(initialViewport.current)
    else void flow.fitView({ padding: 0.3, maxZoom: 1.15 })
  }, [initialized, flow, graphScope])

  const saveLayout = useCallback((viewport?: Viewport) => {
    if (!storageName) return
    const allowed = new Set(latestModel.current.nodes.filter((node) => !node.restricted).map((node) => node.id))
    const positions = Object.fromEntries(flow.getNodes().filter((node) => allowed.has(node.id)).map((node) => [node.id, node.position]))
    try { localStorage.setItem(storageName, JSON.stringify({ version: 1, positions, viewport: viewport ?? flow.getViewport() })) } catch { /* Layout storage is optional. */ }
  }, [flow, storageName])

  function autoLayout(reset = false) {
    const current = latestModel.current
    const positions = arrangeRelationNodes(current.nodes, current.edges, focusSchemaId)
    setNodes((items) => items.map((node) => ({ ...node, position: positions[node.id] ?? node.position })))
    if (reset && storageName) try { localStorage.removeItem(storageName) } catch { /* Optional preference. */ }
    requestAnimationFrame(() => { void flow.fitView({ padding: 0.3, maxZoom: 1.15, duration: 200 }).then(() => { if (!reset) saveLayout() }) })
  }
  function connect(connection: Connection) {
    const source = model.nodes.find((node) => node.id === connection.source)
    const target = model.nodes.find((node) => node.id === connection.target)
    if (disabled || !source?.record || !target?.record || source.record.status !== 'active' || target.record.status !== 'active') return
    onConnect?.(source.record.definition.id, target.record.definition.id)
  }
  function validConnection(connection: Edge | Connection) {
    const source = model.nodes.find((node) => node.id === connection.source)
    const target = model.nodes.find((node) => node.id === connection.target)
    return !disabled && !!onConnect && source?.record?.status === 'active' && target?.record?.status === 'active'
  }

  return <section className={`relation-graph ${compact ? 'compact' : ''}`} aria-label="关系建模画布"
    onKeyDownCapture={(event) => {
      if (event.key !== 'Enter' && event.key !== ' ') return
      if (!(event.target instanceof Element) || event.target.closest('button')) return
      const nodeId = event.target.closest('.react-flow__node')?.getAttribute('data-id')
      const node = nodeId ? model.nodes.find((item) => item.id === nodeId) : undefined
      if (node?.record) {
        event.preventDefault(); event.stopPropagation(); setSelectedSchema(node.id); setLocalLinkId(undefined); onSelectSchema?.(node.record)
        return
      }
      const edgeId = event.target.closest('.react-flow__edge')?.getAttribute('data-id')
      const edge = edgeId ? model.edges.find((item) => item.id === edgeId) : undefined
      if (edge) { event.preventDefault(); event.stopPropagation(); selectLink(edge.record) }
    }}
    onKeyUpCapture={(event) => {
      if (event.key.startsWith('Arrow') && event.target instanceof Element && event.target.closest('.react-flow__node')) requestAnimationFrame(() => saveLayout())
    }}>
    <div className="relation-graph-toolbar"><span><GitBranch size={15} />{focusSchemaId ? '对象关系' : '关系画布'}</span><div role="group" aria-label="画布操作">
      <button type="button" className="relation-tool" aria-label="放大画布" title="放大" onClick={() => void flow.zoomIn({ duration: 150 })}><ZoomIn size={16} /></button>
      <button type="button" className="relation-tool" aria-label="缩小画布" title="缩小" onClick={() => void flow.zoomOut({ duration: 150 })}><ZoomOut size={16} /></button>
      <button type="button" className="relation-tool" aria-label="适配画布" title="适配画布" onClick={() => void flow.fitView({ padding: 0.3, maxZoom: 1.15, duration: 200 })}><Maximize2 size={16} /></button>
      <button type="button" className="relation-tool" aria-label="自动布局" title="自动布局" onClick={() => autoLayout()}><Waypoints size={16} /></button>
      {storageName && <button type="button" className="relation-tool" aria-label="重置布局" title="重置个人布局" onClick={() => autoLayout(true)}><RotateCcw size={16} /></button>}
    </div></div>
    <div className="relation-graph-surface">
      {model.nodes.length ? <ReactFlow<SchemaNode, RelationEdge>
        nodes={nodes} edges={edges} nodeTypes={nodeTypes} edgeTypes={edgeTypes} onNodesChange={onNodesChange} onEdgesChange={onEdgesChange}
        onNodeClick={(_, node) => { if (node.data.record) { setSelectedSchema(node.id); setLocalLinkId(undefined); onSelectSchema?.(node.data.record) } }}
        onEdgeClick={(_, edge) => { if (edge.data?.record) selectLink(edge.data.record) }}
        onConnect={connect} isValidConnection={validConnection} onNodeDragStop={() => saveLayout()} onMoveEnd={(_, viewport) => saveLayout(viewport)}
        onInit={(instance) => { if (initialViewport.current) void instance.setViewport(initialViewport.current); else void instance.fitView({ padding: 0.3, maxZoom: 1.15 }) }}
        minZoom={0.15} maxZoom={2.5} fitView fitViewOptions={{ padding: 0.3, maxZoom: 1.15 }}
        nodesConnectable={!disabled && !!onConnect} nodesDraggable={!disabled} edgesReconnectable={false} deleteKeyCode={null}
        panOnScroll selectionOnDrag={false} proOptions={{ hideAttribution: true }}
        ariaLabelConfig={{ 'node.a11yDescription.default': '按回车选择对象，方向键移动布局，按 Escape 取消选择。', 'node.a11yDescription.keyboardDisabled': '按回车选择对象。', 'edge.a11yDescription.default': '按回车选择关系。', 'controls.ariaLabel': '画布操作' }}>
        <Background variant={BackgroundVariant.Dots} gap={22} size={1} color="#d4e3db" />
      </ReactFlow> : <div className="relation-graph-empty"><GitBranch size={30} /><strong>暂无可显示的关系模型</strong><p>添加对象模型后，可在这里连接和管理关系。</p></div>}
    </div>
    <div className="relation-graph-footer"><span><i className="relation-legend-current" />当前态<i className="relation-legend-draft" />草稿</span><span>{model.nodes.filter((node) => !node.restricted).length} 个对象 · {model.edges.length} 条关系</span></div>
    {(model.omittedNodes > 0 || model.omittedLinks > 0) && <p className="relation-graph-budget" role="status">已达到画布显示上限，请通过分组、搜索或对象局部图缩小范围；当前视图未包含全部定义。</p>}
  </section>
}
