import { useState } from 'react'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { Dialog } from '../../components/Dialog'

export interface OntologyLayer { id: string; name: string }
export interface OntologyLayers { layers: OntologyLayer[]; assignments: Record<string, string> }

export function readOntologyLayers(key: string): OntologyLayers {
  try {
    const data = JSON.parse(localStorage.getItem(key) ?? 'null')
    if (Array.isArray(data?.layers) && data.layers.every((layer: OntologyLayer) => typeof layer?.id === 'string' && !!layer.id && typeof layer?.name === 'string')
      && new Set(data.layers.map((layer: OntologyLayer) => layer.id)).size === data.layers.length
      && data.assignments && typeof data.assignments === 'object' && !Array.isArray(data.assignments)
      && Object.values(data.assignments).every(assignment => typeof assignment === 'string')) return data
  } catch { /* Browser preferences are optional. */ }
  return { layers: [], assignments: {} }
}

export function OntologyLayerDialog({ value, records, groups, onClose, onSave }: {
  value: OntologyLayers
  records: DefinitionRecord[]
  groups: Organization[]
  onClose: () => void
  onSave: (value: OntologyLayers) => void
}) {
  const [draft, setDraft] = useState(value)
  const [search, setSearch] = useState('')
  function initialize() {
    const layers = [...draft.layers]
    const assignments = { ...draft.assignments }
    for (const group of groups) {
      const id = `group:${group.id}`
      if (!layers.some(layer => layer.id === id)) layers.push({ id, name: group.name })
      for (const { definition } of records) {
        if (definition.groupId === group.id && !Object.hasOwn(assignments, definition.id)) assignments[definition.id] = id
      }
    }
    setDraft({ layers, assignments })
  }
  function move(index: number, offset: number) {
    const layers = [...draft.layers]
    const [layer] = layers.splice(index, 1)
    layers.splice(index + offset, 0, layer)
    setDraft({ ...draft, layers })
  }
  const matches = records.filter(({ definition }) => definition.name.includes(search.trim()))
  return <Dialog title="配置业务分层" description="层按列表顺序从上到下显示。配置保存在当前浏览器。" wide onClose={onClose}>
    <form className="ontology-layer-form" onSubmit={event => {
      event.preventDefault()
      onSave({ ...draft, layers: draft.layers.map(layer => ({ ...layer, name: layer.name.trim() })) })
    }}>
      <section className="ontology-layer-section" aria-labelledby="ontology-layer-order">
        <div className="ontology-layer-heading">
          <h3 id="ontology-layer-order">层名称与顺序</h3>
          <div className="ontology-layer-actions">
            <button type="button" className="button subtle" disabled={!groups.length} onClick={initialize}>从分组初始化</button>
            <button type="button" className="button" onClick={() => setDraft({ ...draft, layers: [...draft.layers, { id: crypto.randomUUID(), name: '' }] })}>新增层</button>
          </div>
        </div>
        {!draft.layers.length && <p className="muted">添加层或从分组初始化后，即可为定义分配层。</p>}
        {draft.layers.map((layer, index) => <div className="ontology-layer-row" key={layer.id}>
          <label className="field">层名称<input name={`layer-${layer.id}`} autoComplete="off" required maxLength={60} value={layer.name} onChange={event => setDraft({ ...draft, layers: draft.layers.map(item => item.id === layer.id ? { ...item, name: event.target.value } : item) })} /></label>
          <div className="ontology-layer-actions">
          <button type="button" className="button" disabled={index === 0} aria-label={`上移第 ${index + 1} 层`} onClick={() => move(index, -1)}>上移</button>
          <button type="button" className="button" disabled={index === draft.layers.length - 1} aria-label={`下移第 ${index + 1} 层`} onClick={() => move(index, 1)}>下移</button>
          <button type="button" className="button" aria-label={`移除第 ${index + 1} 层`} onClick={() => setDraft({
            layers: draft.layers.filter(item => item.id !== layer.id),
            assignments: Object.fromEntries(Object.entries(draft.assignments).map(([id, assigned]) => [id, assigned === layer.id ? '' : assigned])),
          })}>移除</button>
          </div>
        </div>)}
      </section>
      <section className="ontology-layer-section" aria-labelledby="ontology-layer-definitions">
        <h3 id="ontology-layer-definitions">定义归层</h3>
        <label className="field">搜索要归层的定义<input type="search" name="definition-search" autoComplete="off" value={search} onChange={event => setSearch(event.target.value)} /></label>
        <div className="form-grid ontology-layer-assignments">
          {matches.map(({ definition }) => <label className="field" key={definition.id}>
            {definition.name}<select name={`assignment-${definition.id}`} value={Object.hasOwn(draft.assignments, definition.id) ? draft.assignments[definition.id] : ''}
              onChange={event => setDraft({ ...draft, assignments: { ...draft.assignments, [definition.id]: event.target.value } })}>
              <option value="">未分层</option>
              {draft.layers.map((layer, index) => <option key={layer.id} value={layer.id}>{layer.name || `第 ${index + 1} 层`}</option>)}
            </select>
          </label>)}
        </div>
        {!matches.length && <p className="muted" role="status">没有匹配的定义。</p>}
      </section>
      <div className="dialog-actions">
        <button type="button" className="button" onClick={onClose}>取消</button>
        <button type="submit" className="button primary" disabled={draft.layers.some(layer => !layer.name.trim())}>保存分层</button>
      </div>
    </form>
  </Dialog>
}
