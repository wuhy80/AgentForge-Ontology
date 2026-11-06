import { useEffect, useMemo, useRef, useState } from 'react'
import { ArrowRight, Box, Braces, Check, ChevronRight, CircleDashed, Database, FilePenLine, FolderCog, GitBranch, LayoutGrid, List, LoaderCircle, Plus, RefreshCw, Search, Shapes, Workflow } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Kind, Organization } from '../../lib/types'
import { ModelEditor, type PendingPublication } from './ModelEditor'
import { OrganizationsDialog } from './OrganizationsDialog'
import { kindDescriptions, kindLabels } from './model-helpers'
import './model.css'

const icons = { schema: Box, enum: List, link: GitBranch, action: Workflow, function: Braces }

export function ModelsPage({ api, categories, entries, onRefresh, onQuery, draftDirectoryDenied = false }: {
  api: ApiClient
  categories: Organization[]
  entries: DefinitionRecord[]
  onRefresh: () => Promise<void>
  onQuery: (schemaId: string) => void
  draftDirectoryDenied?: boolean
}) {
  const [kind, setKind] = useState<Kind | 'all'>('all')
  const [categoryId, setCategoryId] = useState('')
  const [search, setSearch] = useState('')
  const [layout, setLayout] = useState<'grid' | 'list'>('grid')
  const [organizationsOpen, setOrganizationsOpen] = useState(false)
  const [editor, setEditor] = useState<{ record?: DefinitionRecord; authorizationRecord?: DefinitionRecord; kind: Kind; token: number }>()
  const [localEntries, setLocalEntries] = useState<Record<string, DefinitionRecord>>({})
  const [pending, setPending] = useState<Record<string, PendingPublication>>({})
  const [error, setError] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [opening, setOpening] = useState('')
  const editorSequence = useRef(0)
  const openingSequence = useRef(0)
  const editorRef = useRef(editor)
  editorRef.current = editor

  function closeEditor() { editorSequence.current++; setEditor(undefined) }
  function launchEditor(value: { record?: DefinitionRecord; kind: Kind }) {
    openingSequence.current++
    setOpening('')
    setEditor({ ...value, token: ++editorSequence.current })
  }
  function clearPending(id: string) {
    setPending((value) => { const next = { ...value }; delete next[id]; return next })
    setLocalEntries((value) => { const next = { ...value }; delete next[id]; return next })
  }

  useEffect(() => {
    // Every fresh directory is an authorization boundary, including an empty
    // result after individual object permissions were revoked.
    const visible = new Map(entries.map((entry) => [entry.definition.id, entry.etag]))
    if (draftDirectoryDenied) {
      openingSequence.current++; setOpening(''); setPending({}); setLocalEntries({}); closeEditor()
      return
    }
    setPending((value) => Object.fromEntries(Object.entries(value).filter(([id, item]) => visible.get(id) === item.record.etag)))
    setLocalEntries({})
    const current = editorRef.current
    const authorizationRecord = current?.authorizationRecord ?? current?.record
    if (authorizationRecord?.status === 'draft' && visible.get(authorizationRecord.definition.id) !== authorizationRecord.etag) {
      openingSequence.current++; setOpening(''); closeEditor()
    }
  }, [entries, draftDirectoryDenied])

  const models = useMemo(() => {
    const merged = new Map(entries.map((entry) => [entry.definition.id, entry]))
    Object.values(localEntries).forEach((entry) => merged.set(entry.definition.id, entry))
    return [...merged.values()]
  }, [entries, localEntries])
  const visible = models.filter((entry) => {
    const definition = entry.definition
    const term = search.trim().toLocaleLowerCase()
    return (kind === 'all' || kind === definition.kind) && (!categoryId || categoryId === definition.categoryId) && (!term || `${definition.name} ${definition.description ?? ''}`.toLocaleLowerCase().includes(term))
  })

  async function refresh() {
    setRefreshing(true); setError('')
    try { await onRefresh(); setLocalEntries({}) } catch (cause) { setError(cause instanceof Error ? cause.message : '目录刷新失败。') } finally { setRefreshing(false) }
  }
  async function open(entry: DefinitionRecord) {
    const waiting = pending[entry.definition.id]
    const requestSequence = ++openingSequence.current
    setOpening(entry.definition.id); setError('')
    try {
      let latest: DefinitionRecord
      try { latest = await api.getDraft(entry.definition.id) } catch (cause) {
        if (!(cause instanceof ApiError) || cause.status !== 404) throw cause
        latest = await api.getCurrent(entry.definition.id)
      }
      if (requestSequence !== openingSequence.current) return
      if (waiting && latest.etag !== waiting.record.etag) {
        clearPending(entry.definition.id)
        setError('待恢复内容已改变，已清除旧草稿缓存。请重新读取当前定义核对发布状态。')
        return
      }
      latest.hasCurrent = entry.hasCurrent || entry.status === 'active' || latest.status === 'active'
      if (waiting) setPending((value) => ({ ...value, [entry.definition.id]: { ...waiting, record: latest } }))
      launchEditor({ record: latest, kind: latest.definition.kind })
    } catch (cause) {
      if (requestSequence !== openingSequence.current) return
      if (waiting && cause instanceof ApiError && (cause.status === 403 || cause.status === 404)) clearPending(entry.definition.id)
      setError(cause instanceof Error ? cause.message : '定义读取失败。')
      if (cause instanceof ApiError && cause.status === 403) {
        try { await onRefresh() } catch { /* The explicit read denial remains visible. */ }
      }
    } finally { if (requestSequence === openingSequence.current || editorRef.current?.record?.definition.id === entry.definition.id) setOpening('') }
  }
  async function saved(record: DefinitionRecord, token: number) {
    if (token !== editorSequence.current) return
    setEditor((value) => value?.token === token ? { ...value, authorizationRecord: record } : value)
    setLocalEntries((value) => ({ ...value, [record.definition.id]: record }))
    try { await onRefresh() } finally { clearLocal(record) }
  }
  async function published(record: DefinitionRecord, token: number) {
    if (token !== editorSequence.current) return
    setEditor((value) => value?.token === token ? { ...value, authorizationRecord: record } : value)
    setPending((value) => { const next = { ...value }; delete next[record.definition.id]; return next })
    setLocalEntries((value) => ({ ...value, [record.definition.id]: record }))
    try { await onRefresh() } finally { clearLocal(record) }
  }
  function clearLocal(record: DefinitionRecord) {
    setLocalEntries((value) => {
      if (value[record.definition.id]?.etag !== record.etag) return value
      const next = { ...value }
      delete next[record.definition.id]
      return next
    })
  }

  return <div className="model-page">
    <div className="model-page-heading"><div><div className="model-eyebrow">ONTOLOGY WORKSPACE</div><h1>业务模型</h1><p className="muted">从业务语言出发，把对象、关系与规则连接起来。</p></div><div className="model-inline-actions"><button type="button" className="button" onClick={() => setOrganizationsOpen(true)}><FolderCog size={17} />管理分类</button><button type="button" className="button primary" onClick={() => launchEditor({ kind: kind === 'all' ? 'schema' : kind })}><Plus size={18} />新建定义</button></div></div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {Object.values(pending).length > 0 && <section className="model-pending-panel" aria-label="待恢复的发布"><div className="model-pending-heading"><RefreshCw size={18} /><strong>有 {Object.keys(pending).length} 项发布等待恢复</strong><span>保留原始发布内容，可安全重试。</span></div>{Object.values(pending).map((item) => <button key={item.record.definition.id} type="button" className="model-pending-row" disabled={!!opening} onClick={() => void open(item.record)}><span>{item.record.definition.name}</span><span className="muted">{item.publication.status === 'activation_pending' ? '等待激活' : '等待确认'}</span><span>恢复发布<ArrowRight size={15} /></span></button>)}</section>}
    <div className="model-kind-tabs" role="group" aria-label="定义类型">
      <button type="button" aria-pressed={kind === 'all'} className={kind === 'all' ? 'is-active' : ''} onClick={() => setKind('all')}><Shapes size={17} />全部定义<span>{models.length}</span></button>
      {(Object.keys(kindLabels) as Kind[]).map((value) => { const Icon = icons[value]; return <button type="button" key={value} aria-pressed={kind === value} className={kind === value ? 'is-active' : ''} onClick={() => setKind(value)}><Icon size={17} />{kindLabels[value]}<span>{models.filter((entry) => entry.definition.kind === value).length}</span></button> })}
    </div>
    <div className="model-toolbar"><div className="model-filter-controls"><label className="model-search"><Search size={17} /><input aria-label="搜索业务模型" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索名称或描述…" /></label><select aria-label="按分类筛选" className="field model-category-filter" value={categoryId} onChange={(event) => setCategoryId(event.target.value)}><option value="">全部分类</option>{categories.map((category) => <option key={category.id} value={category.id}>{category.name}</option>)}</select></div><div className="model-inline-actions"><span className="model-loaded-count">{visible.length} 个已加载定义</span><button type="button" className="button subtle model-icon-button" title="刷新目录" aria-label="刷新目录" disabled={refreshing} onClick={() => void refresh()}><RefreshCw size={16} className={refreshing ? 'model-spin' : ''} /></button><div className="model-layout-toggle"><button type="button" aria-label="网格视图" aria-pressed={layout === 'grid'} className={layout === 'grid' ? 'is-active' : ''} onClick={() => setLayout('grid')}><LayoutGrid size={16} /></button><button type="button" aria-label="列表视图" aria-pressed={layout === 'list'} className={layout === 'list' ? 'is-active' : ''} onClick={() => setLayout('list')}><List size={17} /></button></div></div></div>
    {!categories.length && <div className="notice model-start-notice"><FolderCog size={20} /><div><strong>先建立你的业务分类</strong><p>创建分类和分组后，就可以开始定义业务模型。</p></div><button type="button" className="button" onClick={() => setOrganizationsOpen(true)}>创建分类<ChevronRight size={15} /></button></div>}
    {visible.length === 0 ? <div className="empty-state model-empty"><div className="model-empty-icon"><Box size={30} /></div><h2>{search || categoryId ? '没有匹配的业务定义' : models.length ? '此类型还没有定义' : '从第一个业务对象开始'}</h2><p className="muted">{search || categoryId ? '调整搜索词或分类筛选，查看其他业务定义。' : '将业务概念转化为可管理、可查询的模型。'}</p>{!search && !categoryId && <button type="button" className="button primary" onClick={() => launchEditor({ kind: kind === 'all' ? 'schema' : kind })}><Plus size={17} />新建{kind === 'all' ? '业务定义' : kindLabels[kind]}</button>}</div> : <div className={`model-catalog model-catalog-${layout}`}>{visible.map((entry) => {
      const definition = entry.definition
      const Icon = icons[definition.kind]
      const active = !!(entry.hasCurrent || entry.status === 'active')
      const draft = entry.status === 'draft'
      const details = definition.schema ? `${definition.schema.properties.length} 个属性 · ${definition.schema.indexes?.length ?? 0} 个索引` : definition.enum ? `${definition.enum.values.length} 个业务值` : definition.link ? `关系基数 ${definition.link.cardinality}` : `${(definition.action ?? definition.function)?.input?.length ?? 0} 个输入 · ${(definition.action ?? definition.function)?.output?.length ?? 0} 个输出`
      return <article className="model-card" key={definition.id}><div className="model-card-top"><div className={`model-type-icon model-kind-${definition.kind}`}><Icon size={21} /></div><span className={`model-status ${draft ? 'model-status-draft' : 'model-status-active'}`}>{draft ? <CircleDashed size={12} /> : <Check size={12} />}{draft ? active ? '待发布变更' : '草稿' : '已生效'}</span></div><div className="model-card-body"><span className="model-card-type">{kindLabels[definition.kind]}</span><h2><button type="button" disabled={!!opening} onClick={() => void open(entry)}>{definition.name}{opening === definition.id && <LoaderCircle size={16} className="model-spin" />}</button></h2><p>{definition.description || kindDescriptions[definition.kind]}</p><div className="model-card-path"><span>{categories.find((category) => category.id === definition.categoryId)?.name ?? '业务分类'}</span><ChevronRight size={12} /><span>{kindLabels[definition.kind]}</span></div></div><div className="model-card-footer"><span>{details}</span><div>{active && definition.kind === 'schema' && <button type="button" className="button subtle model-card-action" onClick={() => onQuery(definition.id)}><Database size={14} />查询</button>}<button type="button" className="button subtle model-card-action" disabled={!!opening} onClick={() => void open(entry)}><FilePenLine size={14} />编辑</button></div></div></article>
    })}</div>}
    <div className="model-catalog-note"><span className="model-note-dot" />首批能力 · MySQL 后端<span>草稿保存后发布生效，已生效定义支持展示信息修改。</span></div>
    {organizationsOpen && <OrganizationsDialog api={api} categories={categories} onClose={() => setOrganizationsOpen(false)} onRefresh={onRefresh} />}
    {editor && <ModelEditor key={editor.token} api={api} categories={categories} entries={models} initialKind={editor.kind} initialRecord={editor.record} pending={editor.record ? pending[editor.record.definition.id] : undefined} onClose={closeEditor} onSaved={(record) => saved(record, editor.token)} onPending={(value) => {
      if (editor.token !== editorSequence.current) return
      setPending((current) => {
      const next = { ...current }
      if (value.publication.status === 'rejected') delete next[value.record.definition.id]
      else next[value.record.definition.id] = value
      return next
      })
    }} onPublished={(record) => published(record, editor.token)} />}
  </div>
}
