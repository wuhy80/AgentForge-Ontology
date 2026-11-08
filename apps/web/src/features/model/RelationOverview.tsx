import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { ArrowUpRight, Box, GitBranch, Plus, Search, X } from 'lucide-react'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { RelationGraph } from './RelationGraph'
import './relation-overview.css'

export function relationCatalog(entries: DefinitionRecord[], current: DefinitionRecord[] | undefined, tenantId: string, drafts: boolean) {
  const records = new Map<string, DefinitionRecord>()
  for (const record of current ?? entries.filter((entry) => entry.status === 'active')) {
    if (record.status === 'active' && record.definition.tenantId === tenantId) records.set(record.definition.id, record)
  }
  if (drafts) for (const record of entries) {
    if (record.status === 'draft' && record.definition.tenantId === tenantId) records.set(record.definition.id, record)
  }
  return [...records.values()]
}

export function RelationOverview({ entries, currentEntries, categories, categoryId, groups, selectedLinkId, preview, onSelectLink, onSelectSchema, onCreateLink, editor, layoutScope, disabled = false }: {
  entries: DefinitionRecord[]
  currentEntries?: DefinitionRecord[]
  categories: Organization[]
  categoryId: string
  groups: Organization[]
  selectedLinkId?: string
  preview?: DefinitionRecord
  onSelectLink: (record: DefinitionRecord) => void
  onSelectSchema: (record: DefinitionRecord) => void
  onCreateLink: (sourceSchemaId?: string, targetSchemaId?: string) => void
  editor?: ReactNode
  layoutScope?: string
  disabled?: boolean
}) {
  const [search, setSearch] = useState('')
  const [showDrafts, setShowDrafts] = useState(false)
  const [groupId, setGroupId] = useState('')
  const [binding, setBinding] = useState('all')
  const [focus, setFocus] = useState('')
  const [selectedSchema, setSelectedSchema] = useState('')
  const tenantId = categories.find((category) => category.id === categoryId)?.tenantId ?? ''
  useEffect(() => { setSearch(''); setShowDrafts(false); setGroupId(''); setBinding('all'); setFocus(''); setSelectedSchema('') }, [categoryId, tenantId])
  const catalog = useMemo(() => relationCatalog(entries, currentEntries, tenantId, showDrafts), [entries, currentEntries, tenantId, showDrafts])
  // Draft schema display must never turn a valid current endpoint into a
  // connectable unpublished schema. The editor also validates current IDs.
  const availableSchemas = catalog.filter((entry) => entry.definition.schema)
  const term = search.trim().toLocaleLowerCase()
  const matches = (record: DefinitionRecord) => !term || `${record.definition.name} ${record.definition.description ?? ''}`.toLocaleLowerCase().includes(term)
  const localSchemas = availableSchemas.filter((entry) => entry.definition.categoryId === categoryId && (!groupId || entry.definition.groupId === groupId))
  let links = catalog.filter((entry) => entry.definition.link && entry.definition.categoryId === categoryId && (!groupId || entry.definition.groupId === groupId)
    && (binding === 'all' || (binding === 'unbound' ? !entry.definition.link.storageBinding : entry.definition.link.storageBinding?.mode === binding)))
  if (focus) links = links.filter((entry) => entry.definition.link!.sourceSchemaId === focus || entry.definition.link!.targetSchemaId === focus)
  if (term) links = links.filter((entry) => matches(entry) || availableSchemas.some((schema) => matches(schema)
    && [entry.definition.link!.sourceSchemaId, entry.definition.link!.targetSchemaId].includes(schema.definition.id)))
  if (preview?.definition.link && preview.definition.tenantId === tenantId && preview.definition.categoryId === categoryId) {
    links = [...links.filter((entry) => entry.definition.id !== preview.definition.id), preview]
  }
  const endpoints = new Set(links.flatMap((entry) => [entry.definition.link!.sourceSchemaId, entry.definition.link!.targetSchemaId]))
  const schemas = availableSchemas.filter((entry) => endpoints.has(entry.definition.id)
    || localSchemas.some((local) => local.definition.id === entry.definition.id) && matches(entry) && (!focus || entry.definition.id === focus))
  const selected = availableSchemas.find((entry) => entry.definition.id === selectedSchema)
  const boundLabel = (record: DefinitionRecord) => !record.definition.link?.storageBinding ? '仅定义' : record.definition.link.storageBinding.mode === 'managed' ? '平台托管' : '外部只读'
  // Resource names are rendered from this request's authorized catalog only.
  const schemaName = (id: string) => availableSchemas.find((entry) => entry.definition.id === id)?.definition.name ?? '受限对象'

  function selectSchema(record: DefinitionRecord) { setSelectedSchema(record.definition.id); setFocus(record.definition.id) }
  return <section className="relation-overview" aria-label="分类关系总览">
    <div className="relation-overview-heading"><div><h2><GitBranch size={18} />分类关系图</h2><p>对象类型与业务关系 · 选择连线配置，拖动连接点新建关系</p></div><button type="button" className="button primary" disabled={disabled || !categoryId} onClick={() => onCreateLink()}><Plus size={16} />新建关系</button></div>
    <div className="relation-overview-filters">
      <label className="relation-overview-search"><Search size={15} /><input aria-label="搜索对象或关系" placeholder="搜索对象或关系…" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      <label>分组<select className="field" aria-label="关系分组" value={groupId} onChange={(event) => setGroupId(event.target.value)}><option value="">全部分组</option>{groups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select></label>
      <label>映射<select className="field" aria-label="关系映射状态" value={binding} onChange={(event) => setBinding(event.target.value)}><option value="all">全部映射</option><option value="unbound">仅维护定义</option><option value="managed">平台关系表</option><option value="junction">外部关联表</option><option value="foreign_key">外部外键</option></select></label>
      <label className="relation-overview-check"><input type="checkbox" checked={showDrafts} onChange={(event) => setShowDrafts(event.target.checked)} />显示草稿</label>
      {focus && <button type="button" className="button subtle" onClick={() => { setFocus(''); setSelectedSchema('') }}><X size={14} />取消聚焦</button>}
    </div>
    <div className={`relation-overview-workspace ${editor ? 'has-editor' : ''}`}>
      <div className="relation-overview-explorer">
        <div className="relation-overview-canvas"><RelationGraph schemas={schemas} links={links} categories={categories} categoryId={categoryId} focusSchemaId={focus || undefined} selectedLinkId={selectedLinkId} onSelectLink={onSelectLink} onSelectSchema={selectSchema} onConnect={onCreateLink} layoutKey={layoutScope ? `${layoutScope}:${categoryId}:overview` : undefined} disabled={disabled} /></div>
        <details className="relation-overview-directory" open><summary>可见关系目录（{links.length}）</summary><div>{links.map((record) => <button key={record.definition.id} type="button" className={selectedLinkId === record.definition.id ? 'is-selected' : ''} aria-label={`编辑关系：${record.definition.name}`} disabled={disabled || record.definition.id.startsWith('unsaved:')} onClick={() => onSelectLink(record)}><GitBranch size={14} /><span><strong>{record.definition.name || '未命名关系'}</strong><small>{schemaName(record.definition.link!.sourceSchemaId)} → {schemaName(record.definition.link!.targetSchemaId)} · {record.definition.link!.cardinality}</small></span><span className="badge">{record.status === 'active' ? boundLabel(record) : '草稿'}</span></button>)}</div>{!links.length && <p className="muted">此范围没有可见关系，可调整筛选或新建关系。</p>}</details>
        <details className="relation-overview-schema-list"><summary>对象目录（{localSchemas.length}）</summary><div>{localSchemas.filter(matches).map((record) => <button className="button subtle" key={record.definition.id} type="button" aria-label={`聚焦对象：${record.definition.name}`} onClick={() => selectSchema(record)}><Box size={14} />{record.definition.name}{record.status !== 'active' && ' · 草稿'}</button>)}</div></details>
      </div>
      <aside className="relation-overview-inspector" aria-label="关系配置面板">{editor ?? <div className="relation-overview-selection">{selected ? <><Box size={24} /><h3>{selected.definition.name}</h3><p>{selected.definition.description || '已聚焦此对象的入站与出站关系。'}</p><button type="button" className="button" onClick={() => onSelectSchema(selected)}>打开对象定义<ArrowUpRight size={15} /></button><button type="button" className="button primary" disabled={selected.status !== 'active' || disabled} onClick={() => onCreateLink(selected.definition.id)}><Plus size={15} />从此对象新建关系</button></> : <><GitBranch size={28} /><h3>选择一条关系</h3><p>点击画布连线或下方关系目录，在这里配置业务定义、数据映射并预览真实关联。</p><p className="muted">支持同一对象之间的关系；新关系保存草稿并发布后生效。</p></>}</div>}</aside>
    </div>
  </section>
}
