import { useMemo, useState, type ReactNode } from 'react'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { OntologyGraph3D } from './OntologyGraph3D'
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

export function LayeredPanorama({ records, categories, groups, categoryId, layoutKey, selectedId, onSelect, inspector }: {
  records: DefinitionRecord[]
  categories: Organization[]
  groups: Organization[]
  categoryId: string
  layoutKey: string
  selectedId?: string
  onSelect: (id: string) => void
  inspector: ReactNode
}) {
  const [drawer, setDrawer] = useState<'catalog' | 'detail' | 'closed'>('catalog')
  const [showZones, setShowZones] = useState(true)
  const [showLabels, setShowLabels] = useState(false)
  const [showAllEdges, setShowAllEdges] = useState(false)
  const [tag, setTag] = useState('')
  const [group, setGroup] = useState('')
  const [search, setSearch] = useState('')
  const [layerFocus, setLayerFocus] = useState<string>()
  const [focusId, setFocusId] = useState<string>()
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
  const rootIds = group || tag || term ? local.filter(record => {
    const { definition } = record
    return (!group || definition.groupId === group) && (!term || definition.name.toLowerCase().includes(term)) &&
      (!tag || matchingSchemas.has(definition.id) || collectCallableTargets(record).some(reference => matchingSchemas.has(reference.targetId)))
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
  const directory = local.filter(({ definition }) => ['schema', 'function', 'action'].includes(definition.kind) &&
    (!rootIds || rootIds.includes(definition.id)) && (!layerFocus || configuration.assignments[definition.id] === layerFocus))
  function inspect(id: string) {
    setDrawer('detail')
    setFocusId(undefined)
    onSelect(id)
  }
  return <div className="ontology-panorama">
    <div className="panorama-layout" data-drawer={drawer}>
      <div className="panorama-tools" role="group" aria-label="画布显示">
        <button type="button" aria-pressed={showZones} onClick={() => setShowZones(!showZones)}>分区网格</button>
        <button type="button" aria-pressed={showLabels} onClick={() => setShowLabels(!showLabels)}>节点名称</button>
        <button type="button" aria-pressed={showAllEdges} onClick={() => setShowAllEdges(!showAllEdges)}>全部连线</button>
      </div>
      <div className="panorama-hud">
        <div className="panorama-filter-row" role="group" aria-label="分区筛选"><strong>分区</strong><button type="button" aria-pressed={!group} onClick={() => setGroup('')}>全部</button>
          {groups.map(item => <button type="button" key={item.id} aria-pressed={group === item.id} onClick={() => setGroup(group === item.id ? '' : item.id)}>{item.name}</button>)}</div>
        <div className="panorama-filter-row" role="group" aria-label="图层筛选"><strong>图层</strong>{configuration.layers.map(layer => <button type="button" key={layer.id} data-slab={layer.id} aria-label={layer.name} aria-pressed={layerFocus === layer.id}
          onClick={() => setLayerFocus(layerFocus === layer.id ? undefined : layer.id)}><span>{layer.id === 'pending' ? layer.name : layer.id}</span><span className="panorama-layer-button-name">{layer.id !== 'pending' && layer.name.slice(layer.id.length)}</span></button>)}</div>
        <div className="panorama-filter-row"><label>标签 <select aria-label="Schema 标签" value={tag} onChange={event => setTag(event.target.value)}><option value="">全部标签</option>{tags.map(item => <option key={item}>{item}</option>)}</select></label>
          {selectedId && <button type="button" aria-pressed={focusId === selectedId} onClick={() => setFocusId(focusId === selectedId ? undefined : selectedId)}>聚焦关联</button>}
        </div>
      </div>
      <OntologyGraph3D panorama showZones={showZones} showLabels={showLabels} showAllEdges={showAllEdges} groups={groups} records={records} categories={categories} categoryId={categoryId} layoutKey={layoutKey}
        mode="dependencies" layers={configuration} layerFocus={layerFocus} rootIds={rootIds}
        selectedId={selectedId} focusId={focusId} onSelect={inspect} onFocus={setFocusId} />
      {drawer === 'closed' && <button type="button" className="button panorama-open" onClick={() => setDrawer('catalog')}>展开目录</button>}
      {drawer !== 'closed' && <aside className="ontology-canvas-panel" aria-label="分层节点详情">
        <div className="panorama-drawer-tabs">
          <button type="button" aria-pressed={drawer === 'detail'} disabled={!selected} onClick={() => setDrawer('detail')}>节点详情</button>
          <button type="button" aria-pressed={drawer === 'catalog'} onClick={() => setDrawer('catalog')}>节点目录 · {directory.length}</button>
          <button type="button" onClick={() => setDrawer('closed')}>收起</button></div>
        {drawer === 'catalog' ? <div className="panorama-index">
          <input className="field" type="search" value={search} aria-label="搜索节点" placeholder="搜索节点名称" onChange={event => setSearch(event.target.value)} />
          {configuration.layers.map(layer => {
            const members = directory.filter(item => configuration.assignments[item.definition.id] === layer.id)
            if (!members.length) return null
            return <section key={layer.id}><h3>{layer.name}<small>{members.length}</small></h3>{members.map(({ definition }) =>
              <button type="button" key={definition.id} onClick={() => inspect(definition.id)}>{definition.name}<small>{definition.kind === 'schema' ? definition.schema?.tags?.join(' · ') : definition.kind === 'action' ? '动作' : '函数'}</small></button>)}</section>
          })}
          {!directory.length && <p className="muted">当前筛选下没有节点。</p>}
        </div> : <>{inspector}
        {selected?.definition.schema?.tags?.length ? <div className="panorama-evidence"><h3>Schema 标签</h3><div className="model-tag-list">{selected.definition.schema.tags.map(name => <button type="button" className="button subtle" key={name} onClick={() => setTag(name)}>{name}</button>)}</div></div> : null}
        {!!evidence.length && <div className="panorama-evidence"><h3>执行关联</h3>{evidence.map(item => <button type="button" className="button subtle" key={JSON.stringify(item)} onClick={() => { onSelect(item.id); setFocusId(undefined) }}>{item.label} · {item.name}</button>)}</div>}
        </>}
      </aside>}
    </div>
  </div>
}
