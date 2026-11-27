import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Dialog } from '../../components/Dialog'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { OntologyLayerGraph } from './OntologyLayerGraph'
import { collectCallableTargets } from './panorama-references'
import './layered-panorama.css'

const layers = [
  { id: 'L4', name: 'L4 · 动作与函数' },
  { id: 'L3', name: 'L3 · 事件与单据' },
  { id: 'L2', name: 'L2 · 概念与规则' },
  { id: 'L1', name: 'L1 · 底座与主数据' },
  { id: 'pending', name: '待分层' },
  { id: 'M0', name: 'M0 · 预留' },
]

export function LayeredPanorama({ records, categories, groups, categoryId, layoutScope, selectedId, onSelect, inspector }: {
  records: DefinitionRecord[]
  categories: Organization[]
  groups: Organization[]
  categoryId: string
  layoutScope: string
  selectedId?: string
  onSelect: (id: string) => void
  inspector: (onTag: (tag: string) => void) => ReactNode
}) {
  const focusNode = useRef<(id: string) => void>(null)
  const [narrow, setNarrow] = useState(() => window.matchMedia?.('(max-width: 850px)').matches ?? false)
  const [mobileDetail, setMobileDetail] = useState(false)
  useEffect(() => {
    const query = window.matchMedia?.('(max-width: 850px)')
    if (!query) return
    const update = () => { setNarrow(query.matches); setMobileDetail(false) }
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])
  const [drawer, setDrawer] = useState<'catalog' | 'detail' | 'closed'>(selectedId ? 'detail' : 'catalog')
  const [showZones, setShowZones] = useState(true)
  const [showLabels, setShowLabels] = useState(false)
  const [showAllEdges, setShowAllEdges] = useState(false)
  const [tag, setTag] = useState('')
  const [group, setGroup] = useState('')
  const [search, setSearch] = useState('')
  const [hiddenLayers, setHiddenLayers] = useState<Set<string>>(() => new Set())
  function toggleLayer(id: string) {
    setHiddenLayers(previous => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  const configuration = useMemo(() => ({
    layers: layers.filter(layer => layer.id !== 'pending' || records.some(({ definition }) => definition.schema && !definition.schema.panoramaLayer)),
    assignments: Object.fromEntries(records.flatMap(({ definition }) => definition.schema
      ? [[definition.id, definition.schema.panoramaLayer ?? 'pending']]
      : definition.action || definition.function ? [[definition.id, 'L4']] : [])),
  }), [records])
  const local = records.filter(({ definition }) => definition.categoryId === categoryId)
  const tags = [...new Set(local.flatMap(({ definition }) => definition.schema?.tags ?? []))].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  const matchingSchemas = new Set(local.filter(({ definition }) => definition.schema && (!tag || definition.schema.tags?.includes(tag))).map(({ definition }) => definition.id))
  const term = search.trim().toLowerCase()
  const rootIds = tag ? local.filter(record => {
    const { definition } = record
    return matchingSchemas.has(definition.id) || collectCallableTargets(record).some(reference => matchingSchemas.has(reference.targetId))
  }).map(({ definition }) => definition.id) : undefined
  const selected = records.find(record => record.definition.id === selectedId)
  const visible = new Map(records.filter(record => record.status === 'active').map(record => [record.definition.id, record.definition]))
  const evidence = records.flatMap(record => collectCallableTargets(record).flatMap(reference => {
    const source = visible.get(record.definition.id)
    const target = visible.get(reference.targetId)
    if (!source || !target || target.kind !== (reference.kind === 'function' ? 'function' : 'schema') ||
      (source.id !== selectedId && target.id !== selectedId)) return []
    return [{ id: source.id === selectedId ? target.id : source.id, name: source.id === selectedId ? target.name : source.name, label: reference.label }]
  }))
  const groupNames = new Map(groups.map(item => [item.id, item.name]))
  const directory = local.filter(({ definition }) => ['schema', 'function', 'action'].includes(definition.kind) &&
    (!term || [definition.name, groupNames.get(definition.groupId) ?? '', ...(definition.schema?.tags ?? [])].some(value => value.toLowerCase().includes(term))) &&
    (!rootIds || rootIds.includes(definition.id)) && !hiddenLayers.has(configuration.assignments[definition.id]))
  function inspect(id: string, locate = false) {
    if (narrow) setMobileDetail(true)
    else setDrawer('detail')
    onSelect(id)
    if (locate) focusNode.current?.(id)
  }
  const detail = <div className="panorama-detail">
    {inspector(setTag)}
    {!!evidence.length && <div className="panorama-evidence"><h3>执行关联</h3>{evidence.map(item =>
      <button type="button" className="button subtle" key={JSON.stringify(item)} onClick={() => inspect(item.id)}>{item.label} · {item.name}</button>)}</div>}
  </div>
  return <div className="ontology-panorama">
    <div className="panorama-layout" data-drawer={drawer}>
      <div className="panorama-hud">
        <div className="panorama-filter-row" role="group" aria-label="聚焦分区"><strong>分区</strong><button type="button" aria-pressed={!group} onClick={() => setGroup('')}>全部</button>
          {groups.map(item => <button type="button" key={item.id} aria-pressed={group === item.id} onClick={() => setGroup(group === item.id ? '' : item.id)}>{item.name}</button>)}</div>
        <div className="panorama-filter-row" role="group" aria-label="图层筛选"><strong>图层</strong>{configuration.layers.map(layer => <button type="button" key={layer.id} data-panorama-layer={layer.id} aria-label={layer.name} aria-pressed={!hiddenLayers.has(layer.id)}
          onClick={() => toggleLayer(layer.id)}><span>{layer.id === 'pending' ? layer.name : layer.id}</span><span className="panorama-layer-button-name">{layer.id !== 'pending' && layer.name.slice(layer.id.length)}</span></button>)}</div>
        <div className="panorama-filter-row">
          <label>标签 <select aria-label="Schema 标签" value={tag} onChange={event => setTag(event.target.value)}><option value="">全部标签</option>{tags.map(item => <option key={item}>{item}</option>)}</select></label>
          <div className="panorama-tools" role="group" aria-label="画布显示">
            <button type="button" aria-pressed={showZones} onClick={() => setShowZones(!showZones)}>分区网格</button>
            <button type="button" aria-pressed={showLabels} onClick={() => setShowLabels(!showLabels)}>节点名称</button>
            <button type="button" aria-pressed={showAllEdges} onClick={() => setShowAllEdges(!showAllEdges)}>全部连线</button>
          </div>
          {drawer === 'closed' && <button type="button" onClick={() => setDrawer('catalog')}>展开目录</button>}
        </div>
      </div>
      <OntologyLayerGraph showZones={showZones} showLabels={showLabels} showAllEdges={showAllEdges} groups={groups} records={records} categories={categories} categoryId={categoryId}
        layers={configuration} hiddenLayers={hiddenLayers} focusedGroup={group} rootIds={rootIds}
        selectedId={selectedId} layoutScope={layoutScope} focusRef={focusNode} onSelect={inspect} />
      {drawer !== 'closed' && <aside className="ontology-canvas-panel" aria-label="分层节点详情">
        <div className="panorama-drawer-tabs">
          <button type="button" aria-pressed={narrow ? mobileDetail : drawer === 'detail'} disabled={!selected} onClick={() => narrow ? setMobileDetail(true) : setDrawer('detail')}>节点详情</button>
          <button type="button" aria-pressed={drawer === 'catalog'} onClick={() => setDrawer('catalog')}>节点目录 · {directory.length}</button>
          <button type="button" onClick={() => setDrawer('closed')}>收起</button></div>
        {(drawer === 'catalog' || narrow) ? <div className="panorama-index">
          <input className="field" type="search" value={search} aria-label="搜索节点" placeholder="按名称、标签或分区搜索" onChange={event => setSearch(event.target.value)} />
          {configuration.layers.map(layer => {
            const members = directory.filter(item => configuration.assignments[item.definition.id] === layer.id)
            if (!members.length) return null
            return <section key={layer.id}><h3>{layer.name}<small>{members.length}</small></h3>{members.map(({ definition }) =>
              <button type="button" key={definition.id} onClick={() => inspect(definition.id, true)}>{definition.name}<small>{definition.kind === 'schema' ? definition.schema?.tags?.join(' · ') : definition.kind === 'action' ? '动作' : '函数'}</small></button>)}</section>
          })}
          {!directory.length && <p className="muted">当前筛选下没有节点。</p>}
        </div> : detail}
      </aside>}
      {narrow && mobileDetail && selected && <Dialog title="节点详情" bottomSheet onClose={() => setMobileDetail(false)}>
        {detail}
      </Dialog>}
    </div>
  </div>
}
