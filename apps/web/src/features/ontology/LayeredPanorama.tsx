import { useMemo, useState, type ReactNode } from 'react'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { OntologyGraph3D } from './OntologyGraph3D'
import { collectCallableTargets } from './panorama-references'

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
  return <div className="ontology-panorama">
    <div className="ontology-graph-toolbar panorama-filters">
      <label>分区 <select className="field" value={group} onChange={event => setGroup(event.target.value)}><option value="">全部分区</option>{groups.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Schema 标签 <select className="field" value={tag} onChange={event => setTag(event.target.value)}><option value="">全部标签</option>{tags.map(item => <option key={item}>{item}</option>)}</select></label>
      <label>名称 <input type="search" className="field" value={search} placeholder="搜索节点" onChange={event => setSearch(event.target.value)} /></label>
      {selectedId && <button className="button subtle" type="button" aria-pressed={focusId === selectedId} onClick={() => setFocusId(focusId === selectedId ? undefined : selectedId)}>聚焦关联</button>}
    </div>
    <div className="panorama-layout">
      <OntologyGraph3D panorama groups={groups} records={records} categories={categories} categoryId={categoryId} layoutKey={layoutKey}
        mode="dependencies" layers={configuration} layerFocus={layerFocus} onLayerFocus={setLayerFocus} rootIds={rootIds}
        selectedId={selectedId} focusId={focusId} onSelect={id => { onSelect(id); setFocusId(undefined) }} onFocus={setFocusId} />
      <aside className="ontology-canvas-panel" aria-label="分层节点详情">
        {inspector}
        {selected?.definition.schema?.tags?.length ? <div className="panorama-evidence"><h3>Schema 标签</h3><div className="model-tag-list">{selected.definition.schema.tags.map(name => <button type="button" className="button subtle" key={name} onClick={() => setTag(name)}>{name}</button>)}</div></div> : null}
        {!!evidence.length && <div className="panorama-evidence"><h3>执行关联</h3>{evidence.map(item => <button type="button" className="button subtle" key={JSON.stringify(item)} onClick={() => { onSelect(item.id); setFocusId(undefined) }}>{item.label} · {item.name}</button>)}</div>}
      </aside>
    </div>
  </div>
}
