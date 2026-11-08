import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowRight, Box, Braces, Check, ChevronDown, ChevronRight, CircleDashed, Database, FilePenLine, Folder, FolderCog, GitBranch, LayoutGrid, List, LoaderCircle, Plus, RefreshCw, Search, Workflow } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { Definition, DefinitionRecord, Kind, Organization } from '../../lib/types'
import { ModelEditor, type PendingPublication } from './ModelEditor'
import { kindDescriptions, kindLabels } from './model-helpers'
import { modelListRoute, modelRouteKind } from './model-routes'
import './model.css'

const RelationOverview = lazy(() => import('./RelationOverview').then((module) => ({ default: module.RelationOverview })))

const icons = { schema: Box, enum: List, link: GitBranch, action: Workflow, function: Braces }

function modelSummary(definition: Definition) {
  return definition.schema ? `${definition.schema.properties.length} 个属性 · ${definition.schema.indexes?.length ?? 0} 个索引` : definition.enum ? `${definition.enum.values.length} 个业务值` : definition.link ? `关系基数 ${definition.link.cardinality}` : `${(definition.action ?? definition.function)?.input?.length ?? 0} 个输入 · ${(definition.action ?? definition.function)?.output?.length ?? 0} 个输出`
}

function ModelStatus({ entry }: { entry: DefinitionRecord }) {
  const draft = entry.status === 'draft'
  return <span className={`model-status ${draft ? 'model-status-draft' : 'model-status-active'}`}>{draft ? <CircleDashed size={12} /> : <Check size={12} />}{draft ? entry.hasCurrent ? '待发布变更' : '草稿' : '已生效'}</span>
}

export function ModelsPage({ api, categoryId, categories, entries, currentEntries, onRefresh, onQuery, onOrganizations, onSources, route, onNavigate, onNavigationGuard, layoutScope, draftDirectoryDenied = false, active = true }: {
  api: ApiClient
  categoryId: string
  categories: Organization[]
  entries: DefinitionRecord[]
  currentEntries?: DefinitionRecord[]
  onRefresh: () => Promise<void>
  onQuery: (schemaId: string) => void
  onOrganizations: () => void
  onSources?: () => void
  route?: string
  onNavigate?: (path: string, replace?: boolean) => void
  onNavigationGuard?: (guard?: () => boolean) => void
  layoutScope?: string
  draftDirectoryDenied?: boolean
  active?: boolean
}) {
  const kind = modelRouteKind(route, entries)
  const [search, setSearch] = useState('')
  const [layout, setLayout] = useState<'grid' | 'list' | 'graph'>(() => new URLSearchParams(route?.split('?')[1]).get('view') === 'graph' ? 'graph' : 'list')
  const [groupDirectory, setGroupDirectory] = useState<{ api: ApiClient; categoryId: string; items: Organization[]; error: string; loading: boolean }>()
  const [groupRevision, setGroupRevision] = useState(0)
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({})
  const [editor, setEditor] = useState<{ record?: DefinitionRecord; authorizationRecord?: DefinitionRecord; kind: Kind; token: number; categoryId: string; initialLink?: { sourceSchemaId: string; targetSchemaId: string } }>()
  const [graphPreview, setGraphPreview] = useState<{ definition: Definition; dirty: boolean }>()
  const editorGuard = useRef<(() => boolean) | undefined>(undefined)
  const registerGuard = useCallback((guard?: () => boolean) => { editorGuard.current = guard; onNavigationGuard?.(guard) }, [onNavigationGuard])
  const definitionChanged = useCallback((definition: Definition, dirty: boolean, token: number) => { if (token === editorSequence.current) setGraphPreview({ definition, dirty }) }, [])
  const [localEntries, setLocalEntries] = useState<Record<string, DefinitionRecord>>({})
  const [pending, setPending] = useState<Record<string, PendingPublication>>({})
  const [error, setError] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [opening, setOpening] = useState('')
  const editorSequence = useRef(0)
  const openingSequence = useRef(0)
  const previousRoute = useRef(route)
  const editorRef = useRef(editor)
  editorRef.current = editor
  const categoryRef = useRef(categoryId)
  categoryRef.current = categoryId
  const directoryRef = useRef({ entries, draftDirectoryDenied })
  directoryRef.current = { entries, draftDirectoryDenied }
  const category = categories.find((item) => item.id === categoryId)
  const listRoute = (value = kind) => layout === 'graph' ? 'models?view=graph' : modelListRoute(value)

  function closeEditor(navigate = true) {
    const closingKind = editorRef.current?.kind ?? kind
    editorSequence.current++; setEditor(undefined); setGraphPreview(undefined)
    if (navigate) onNavigate?.(listRoute(closingKind))
  }
  function launchEditor(value: { record?: DefinitionRecord; kind: Kind; initialLink?: { sourceSchemaId: string; targetSchemaId: string } }, navigate = true) {
    openingSequence.current++
    setOpening('')
    if (!category) return
    setGraphPreview(undefined)
    setEditor({ ...value, categoryId, token: ++editorSequence.current })
    if (navigate) onNavigate?.(value.record ? `models/${encodeURIComponent(value.record.definition.id)}${layout === 'graph' && value.kind === 'link' ? '?view=graph' : ''}` : `models/new?kind=${value.kind}${layout === 'graph' ? '&view=graph' : ''}${value.initialLink ? `&source=${encodeURIComponent(value.initialLink.sourceSchemaId)}&target=${encodeURIComponent(value.initialLink.targetSchemaId)}` : ''}`)
  }

  function createRelation(sourceSchemaId = '', targetSchemaId = '') {
    if (editorGuard.current && !editorGuard.current()) return
    launchEditor({ kind: 'link', initialLink: { sourceSchemaId, targetSchemaId } })
  }

  function switchLayout(value: 'grid' | 'list' | 'graph') {
    if (layout === value) return
    if (editor && editorGuard.current && !editorGuard.current()) return
    setLayout(value)
    if (editor) closeEditor(false)
    onNavigate?.(value === 'graph' ? 'models?view=graph' : modelListRoute(kind))
  }

  useEffect(() => {
    openingSequence.current++
    setOpening(''); setRefreshing(false); setError(''); setSearch(''); closeEditor(false)
  }, [categoryId])

  useEffect(() => { setSearch('') }, [kind])

  useEffect(() => () => { openingSequence.current++; editorSequence.current++ }, [])
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
    if (authorizationRecord?.status === 'active' && ![...entries, ...(currentEntries ?? [])].some((entry) => entry.definition.id === authorizationRecord.definition.id && entry.definition.tenantId === authorizationRecord.definition.tenantId)) {
      openingSequence.current++; setOpening(''); closeEditor()
      return
    }
    if (authorizationRecord?.status === 'draft' && visible.get(authorizationRecord.definition.id) !== authorizationRecord.etag) {
      openingSequence.current++; setOpening(''); closeEditor()
    }
  }, [entries, currentEntries, draftDirectoryDenied])

  const models = useMemo(() => {
    const merged = new Map(entries.map((entry) => [entry.definition.id, entry]))
    Object.values(localEntries).forEach((entry) => merged.set(entry.definition.id, entry))
    return [...merged.values()]
  }, [entries, localEntries])
  const scopedModels = models.filter((entry) => entry.definition.categoryId === categoryId)
  const scopedPending = Object.values(pending).filter((item) => item.record.definition.categoryId === categoryId && (layout === 'graph' || item.record.definition.kind === kind))
  const visible = scopedModels.filter((entry) => {
    const definition = entry.definition
    const term = search.trim().toLocaleLowerCase()
    return kind === definition.kind && (!term || `${definition.name} ${definition.description ?? ''}`.toLocaleLowerCase().includes(term))
  })
  const hasModels = scopedModels.length > 0
  useEffect(() => {
    if (!active || !categoryId || !hasModels) return
    const controller = new AbortController()
    setGroupDirectory({ api, categoryId, items: [], error: '', loading: true })
    api.listGroups(categoryId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setGroupDirectory({ api, categoryId, items: items.filter((group) => group.categoryId === categoryId), error: '', loading: false })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setGroupDirectory({ api, categoryId, items: [], error: cause instanceof Error ? cause.message : '分组读取失败', loading: false })
    })
    return () => controller.abort()
  }, [api, categoryId, active, hasModels, entries, groupRevision])
  useEffect(() => { setCollapsedGroups({}) }, [categoryId, search, kind])
  const currentGroups = groupDirectory?.api === api && groupDirectory.categoryId === categoryId ? groupDirectory : undefined
  const knownGroups = new Map(currentGroups?.items.map((group) => [group.id, group]) ?? [])
  const grouped = new Map<string, { id: string; name: string; entries: DefinitionRecord[] }>()
  for (const entry of visible) {
    const group = knownGroups.get(entry.definition.groupId)
    const id = entry.definition.groupId
    if (!grouped.has(id)) grouped.set(id, { id, name: group?.name ?? (!currentGroups || currentGroups.loading ? '正在加载分组' : '其他分组'), entries: [] })
    grouped.get(id)!.entries.push(entry)
  }
  const visibleGroups = [...grouped.values()].sort((left, right) => Number(knownGroups.has(right.id)) - Number(knownGroups.has(left.id)) || left.name.localeCompare(right.name, 'zh-CN'))
  visibleGroups.forEach((group) => group.entries.sort((left, right) => left.definition.name.localeCompare(right.definition.name, 'zh-CN')))

  function groupHeading(group: { id: string; name: string; entries: DefinitionRecord[] }) {
    return <button type="button" className="model-group-toggle" aria-expanded={!collapsedGroups[group.id]} aria-label={`${collapsedGroups[group.id] ? '展开' : '收起'}分组：${group.name}`} onClick={() => setCollapsedGroups((value) => ({ ...value, [group.id]: !value[group.id] }))}>
      {collapsedGroups[group.id] ? <ChevronRight size={14} /> : <ChevronDown size={14} />}<Folder size={15} /><span>{group.name}</span><span className="model-group-count">{group.entries.length}</span>
    </button>
  }

  async function refresh() {
    setRefreshing(true); setError('')
    setGroupRevision((value) => value + 1)
    try {
      await onRefresh()
      if (categoryRef.current === categoryId) setLocalEntries({})
    } catch (cause) {
      if (categoryRef.current === categoryId) setError(cause instanceof Error ? cause.message : '目录刷新失败。')
    } finally { if (categoryRef.current === categoryId) setRefreshing(false) }
  }
  async function open(entry: DefinitionRecord, navigate = true) {
    if (entry.definition.categoryId !== categoryId && onNavigate) {
      if (editorGuard.current && !editorGuard.current()) return
      closeEditor(false)
      onNavigate(`models/${encodeURIComponent(entry.definition.id)}`)
      return
    }
    await openID(entry.definition.id, entry, navigate)
  }
  async function openID(id: string, entry?: DefinitionRecord, navigate = false) {
    if (editorRef.current?.record?.definition.id === id) return
    if (navigate && editorGuard.current && !editorGuard.current()) return
    const waiting = pending[id]
    const requestSequence = ++openingSequence.current
    setOpening(id); setError('')
    try {
      let latest: DefinitionRecord
      try { latest = await api.getDraft(id) } catch (cause) {
        if (!(cause instanceof ApiError) || cause.status !== 404) throw cause
        latest = await api.getCurrent(id)
      }
      if (requestSequence !== openingSequence.current || categoryRef.current !== categoryId) return
      if (latest.definition.categoryId !== categoryId) {
        if (waiting) clearPending(id)
        setError('定义不属于当前分类，请切换顶部分类后重新打开。')
        return
      }
      if (waiting && latest.etag !== waiting.record.etag) {
        clearPending(id)
        setError('待恢复内容已改变，已清除旧草稿缓存。请重新读取当前定义核对发布状态。')
        return
      }
      latest.hasCurrent = !!(entry?.hasCurrent || entry?.status === 'active' || latest.status === 'active')
      if (waiting) setPending((value) => ({ ...value, [id]: { ...waiting, record: latest } }))
      launchEditor({ record: latest, kind: latest.definition.kind }, navigate)
    } catch (cause) {
      if (requestSequence !== openingSequence.current || categoryRef.current !== categoryId) return
      if (waiting && cause instanceof ApiError && (cause.status === 403 || cause.status === 404)) clearPending(id)
      setError(cause instanceof Error ? cause.message : '定义读取失败。')
      if (cause instanceof ApiError && cause.status === 403) {
        try { await onRefresh() } catch { /* The explicit read denial remains visible. */ }
      }
    } finally { if (requestSequence === openingSequence.current || editorRef.current?.record?.definition.id === id) setOpening('') }
  }
  useEffect(() => {
    const previous = previousRoute.current
    previousRoute.current = route
    if (route === undefined || !active) return
    const [path, query] = route.split('?')
    const graph = new URLSearchParams(query).get('view') === 'graph'
    if (graph) setLayout('graph')
    else setLayout((value) => value === 'graph' ? 'list' : value)
    const target = path.startsWith('models/') ? path.slice(7) : ''
    const current = editorRef.current
    if (!target) {
      if (current && current.kind === kind && previous && !previous.startsWith('models')) {
        onNavigate?.(current.record ? `models/${encodeURIComponent(current.record.definition.id)}` : `models/new?kind=${current.kind}`, true)
      } else if (current) closeEditor(false)
      return
    }
    if (!category) return
    if (target === 'new') {
      if (current && !current.record && current.categoryId === categoryId && current.kind === kind) return
      const requested = new URLSearchParams(query).get('kind') as Kind
      const params = new URLSearchParams(query)
      launchEditor({ kind: requested in kindLabels ? requested : 'schema', initialLink: requested === 'link' ? { sourceSchemaId: params.get('source') ?? '', targetSchemaId: params.get('target') ?? '' } : undefined }, false)
      return
    }
    let id: string
    try { id = decodeURIComponent(target) } catch { setError('模型地址无效。'); return }
    if (current?.record?.definition.id === id && current.categoryId === categoryId) return
    closeEditor(false)
    void openID(id, models.find((entry) => entry.definition.id === id))
  }, [route, active, categoryId])
  async function saved(record: DefinitionRecord, token: number) {
    if (token !== editorSequence.current) return
    setEditor((value) => value?.token === token ? { ...value, record, authorizationRecord: record } : value)
    onNavigate?.(`models/${encodeURIComponent(record.definition.id)}${layout === 'graph' && record.definition.link ? '?view=graph' : ''}`, true)
    setLocalEntries((value) => ({ ...value, [record.definition.id]: record }))
    try { await onRefresh() } finally { clearLocal(record) }
  }
  async function published(record: DefinitionRecord, token: number) {
    if (token !== editorSequence.current) return
    setEditor((value) => value?.token === token ? { ...value, record, authorizationRecord: record } : value)
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

  const editing = editor && editor.categoryId === categoryId ? <ModelEditor key={`${categoryId}:${editor.token}`} api={api} active={active} categoryId={categoryId} categories={categories} entries={models} currentEntries={currentEntries} initialKind={editor.kind} initialRecord={editor.record} initialLink={editor.initialLink} embedded={layout === 'graph' && editor.kind === 'link'} layoutScope={layoutScope} onDefinitionChange={(definition, dirty) => definitionChanged(definition, dirty, editor.token)} onGraph={() => { setLayout('graph'); onNavigate?.(editor.record ? `models/${encodeURIComponent(editor.record.definition.id)}?view=graph` : 'models/new?kind=link&view=graph', true) }} pending={editor.record ? pending[editor.record.definition.id] : undefined} onClose={() => closeEditor()} onOrganizations={onOrganizations} onSources={onSources} onNavigationGuard={registerGuard} onSaved={(record) => saved(record, editor.token)} onPending={(value) => {
    const directory = directoryRef.current
    // Late publication results may retain their original recovery identity only
    // while that exact draft remains authorized in the current directory.
    if (directory.draftDirectoryDenied || editor.token !== editorSequence.current && !directory.entries.some((entry) => entry.definition.id === value.record.definition.id && entry.etag === value.record.etag)) return
    setPending((current) => {
      const next = { ...current }
      if (value.publication.status === 'rejected') delete next[value.record.definition.id]
      else next[value.record.definition.id] = value
      return next
    })
  }} onPublished={(record) => published(record, editor.token)} /> : undefined

  if (editing && (layout !== 'graph' || editor?.kind !== 'link')) return editing

  if (route?.startsWith('models/') && !editing && layout !== 'graph') return <section className="model-editor-page" aria-label="模型编辑状态"><button type="button" className="button subtle" onClick={() => closeEditor()}>返回模型列表</button>{error ? <div className="notice notice-error" role="alert">{error}</div> : <div className="loading-state" role="status"><LoaderCircle className="model-spin" size={24} /><p>正在读取模型…</p></div>}</section>

  return <div className={`model-page model-page-${layout}`}>
    <div className="model-page-heading"><div><div className="model-eyebrow">ONTOLOGY WORKSPACE</div><h1>{layout === 'graph' ? '关系' : kindLabels[kind]}</h1><p className="muted">{kindDescriptions[layout === 'graph' ? 'link' : kind]}</p></div><div className="model-inline-actions">{layout !== 'graph' && <button type="button" className="button primary" disabled={!category} onClick={() => launchEditor({ kind })}><Plus size={18} />新建定义</button>}</div></div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {scopedPending.length > 0 && <section className="model-pending-panel" aria-label="待恢复的发布"><div className="model-pending-heading"><RefreshCw size={18} /><strong>有 {scopedPending.length} 项发布等待恢复</strong><span>保留原始发布内容，可安全重试；切换分类后仍可返回恢复。</span></div>{scopedPending.map((item) => <button key={item.record.definition.id} type="button" className="model-pending-row" disabled={!!opening} onClick={() => void open(item.record)}><span>{item.record.definition.name}</span><span className="muted">{item.publication.status === 'activation_pending' ? '等待激活' : '等待确认'}</span><span>恢复发布<ArrowRight size={15} /></span></button>)}</section>}
    <div className="model-toolbar"><div className="model-filter-controls">{layout !== 'graph' && <label className="model-search"><Search size={17} /><input aria-label="搜索业务模型" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索名称或描述…" /></label>}</div><div className="model-inline-actions"><span className="model-loaded-count">{scopedModels.filter((entry) => entry.definition.kind === (layout === 'graph' ? 'link' : kind)).length} 个已加载定义</span><button type="button" className="button subtle model-icon-button" title="刷新目录" aria-label="刷新目录" disabled={refreshing} onClick={() => void refresh()}><RefreshCw size={16} className={refreshing ? 'model-spin' : ''} /></button><div className="model-layout-toggle"><button type="button" aria-label="关系图" title="关系图" aria-pressed={layout === 'graph'} className={layout === 'graph' ? 'is-active' : ''} onClick={() => switchLayout('graph')}><GitBranch size={17} /></button><button type="button" aria-label="网格视图" aria-pressed={layout === 'grid'} className={layout === 'grid' ? 'is-active' : ''} onClick={() => switchLayout('grid')}><LayoutGrid size={16} /></button><button type="button" aria-label="列表视图" aria-pressed={layout === 'list'} className={layout === 'list' ? 'is-active' : ''} onClick={() => switchLayout('list')}><List size={17} /></button></div></div></div>
    {!category && <div className="notice model-start-notice"><FolderCog size={20} /><div><strong>{categories.length ? '先选择当前业务分类' : '先建立你的业务分类'}</strong><p>{categories.length ? '请在顶部选择当前分类，或前往「分类与分组」维护业务目录。' : '请前往「分类与分组」创建分类和分组，再开始定义业务模型。'}</p></div><button type="button" className="button" onClick={onOrganizations}>前往分类与分组<ChevronRight size={15} /></button></div>}
    {currentGroups?.error && <div className="notice model-group-error" role="alert">分组名称暂不可用：{currentGroups.error}。可刷新目录重试，模型暂列于“其他分组”。</div>}
    {layout === 'graph' ? <Suspense fallback={<p className="muted" role="status">正在加载关系画布…</p>}><RelationOverview key={categoryId} entries={models} currentEntries={currentEntries} categories={categories} categoryId={categoryId} groups={currentGroups?.items ?? []} selectedLinkId={editor?.record?.definition.id} preview={editor?.kind === 'link' && graphPreview ? { definition: { ...graphPreview.definition, id: graphPreview.definition.id || `unsaved:${editor.token}`, tenantId: category?.tenantId ?? '' }, etag: editor.record?.etag ?? 'unsaved', status: graphPreview.dirty || !editor.record ? 'draft' : editor.record.status } : undefined} onSelectLink={(record) => void open(record)} onSelectSchema={(record) => void open(record)} onCreateLink={createRelation} editor={editing} layoutScope={layoutScope} disabled={!!opening || refreshing} /></Suspense> : visible.length === 0 ? <div className="empty-state model-empty"><div className="model-empty-icon"><Box size={30} /></div><h2>{search ? '没有匹配的业务定义' : scopedModels.length ? '此类型还没有定义' : '从第一个业务对象开始'}</h2><p className="muted">{search ? '调整搜索词，查看当前分类下的其他业务定义。' : '将业务概念转化为可管理、可查询的模型。'}</p>{!search && category && <button type="button" className="button primary" onClick={() => launchEditor({ kind })}><Plus size={17} />新建{kindLabels[kind]}</button>}</div> : layout === 'list' ? <div className="model-list-wrap"><table className="model-list-table" aria-label="按分组排列的业务模型">
      <thead><tr><th className="model-list-name">模型名称</th><th className="model-list-type">类型</th><th className="model-list-status">状态</th><th className="model-list-description">描述</th><th className="model-list-details">概要</th><th className="model-list-actions">操作</th></tr></thead>
      {visibleGroups.map((group) => <tbody key={group.id} aria-label={group.name}>
        <tr className="model-group-heading-row"><th colSpan={6} scope="rowgroup">{groupHeading(group)}</th></tr>
        {!collapsedGroups[group.id] && group.entries.map((entry) => {
          const definition = entry.definition
          const Icon = icons[definition.kind]
          const description = definition.description || kindDescriptions[definition.kind]
          return <tr key={definition.id} className="model-list-row">
            <td className="model-list-name"><button type="button" className="model-list-name-button" title={definition.name} disabled={!!opening} onClick={() => void open(entry)}><Icon size={15} className={`model-list-kind-${definition.kind}`} /><span>{definition.name}</span>{opening === definition.id && <LoaderCircle size={14} className="model-spin" />}</button></td>
            <td className="model-list-type">{kindLabels[definition.kind]}</td>
            <td className="model-list-status"><ModelStatus entry={entry} /></td>
            <td className="model-list-description"><span title={description}>{description}</span></td>
            <td className="model-list-details">{modelSummary(definition)}</td>
            <td className="model-list-actions"><div>{(entry.hasCurrent || entry.status === 'active') && definition.kind === 'schema' && <button type="button" className="button subtle model-list-action" onClick={() => onQuery(definition.id)}><Database size={13} />查询</button>}<button type="button" className="button subtle model-list-action" disabled={!!opening} onClick={() => void open(entry)}><FilePenLine size={13} />编辑</button></div></td>
          </tr>
        })}
      </tbody>)}
    </table></div> : <div className="model-grouped-catalog">{visibleGroups.map((group) => <section className="model-catalog-group" key={group.id} aria-label={group.name}><h2 className="model-grid-group-heading">{groupHeading(group)}</h2>{!collapsedGroups[group.id] && <div className="model-catalog model-catalog-grid">{group.entries.map((entry) => {
      const definition = entry.definition
      const Icon = icons[definition.kind]
      const active = !!(entry.hasCurrent || entry.status === 'active')
      return <article className="model-card" key={definition.id}><div className="model-card-top"><div className={`model-type-icon model-kind-${definition.kind}`}><Icon size={21} /></div><ModelStatus entry={entry} /></div><div className="model-card-body"><span className="model-card-type">{kindLabels[definition.kind]}</span><h3><button type="button" disabled={!!opening} onClick={() => void open(entry)}>{definition.name}{opening === definition.id && <LoaderCircle size={16} className="model-spin" />}</button></h3><p>{definition.description || kindDescriptions[definition.kind]}</p><div className="model-card-path"><span>{category?.name ?? '业务分类'}</span><ChevronRight size={12} /><span>{group.name}</span></div></div><div className="model-card-footer"><span>{modelSummary(definition)}</span><div>{active && definition.kind === 'schema' && <button type="button" className="button subtle model-card-action" onClick={() => onQuery(definition.id)}><Database size={14} />查询</button>}<button type="button" className="button subtle model-card-action" disabled={!!opening} onClick={() => void open(entry)}><FilePenLine size={14} />编辑</button></div></div></article>
    })}</div>}</section>)}</div>}
    <div className="model-catalog-note"><span className="model-note-dot" />首批能力 · MySQL 后端<span>草稿保存后发布生效，已生效定义支持展示信息修改。</span></div>
  </div>
}
