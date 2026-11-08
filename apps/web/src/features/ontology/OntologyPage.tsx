import { useEffect, useMemo, useRef, useState } from 'react'
import { Box, Braces, ChevronRight, GitBranch, Layers3, List, LoaderCircle, Network, RefreshCw, Search, Workflow, X } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import type { Kind, Organization } from '../../lib/types'
import { Dialog } from '../../components/Dialog'
import { OntologyGraph } from './OntologyGraph'
import { OntologyInspector } from './OntologyInspector'
import { loadOntologyCatalog, type OntologyCatalog } from './ontology-catalog'
import './ontology-page.css'

const kinds: { kind: Kind; name: string; icon: typeof Box }[] = [
  { kind: 'schema', name: '对象模型', icon: Box },
  { kind: 'link', name: '关系', icon: GitBranch },
  { kind: 'enum', name: '枚举', icon: List },
  { kind: 'action', name: '动作', icon: Workflow },
  { kind: 'function', name: '函数', icon: Braces },
]
type Props = {
  api: ApiClient; categoryId: string; categories: Organization[]; layoutScope: string
  onOpenDefinition: (id: string, categoryId: string) => void
  onQuery: (schemaId: string, categoryId: string) => void
}

function useNarrow() {
  const [narrow, setNarrow] = useState(() => typeof window.matchMedia === 'function' ? window.matchMedia('(max-width: 720px)').matches : window.innerWidth <= 720)
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const media = window.matchMedia('(max-width: 720px)')
    const update = () => setNarrow(media.matches)
    update(); media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  return narrow
}

export function OntologyPage(props: Props) {
  // A different category or client owns different requests, selections and data.
  return <OntologyWorkspace key={`${props.layoutScope}:${props.categoryId}`} {...props} />
}

function OntologyWorkspace({ api, categoryId, categories, layoutScope, onOpenDefinition, onQuery }: Props) {
  const [catalog, setCatalog] = useState<OntologyCatalog>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const [search, setSearch] = useState('')
  const [kind, setKind] = useState<Kind | ''>('')
  const [group, setGroup] = useState('')
  const [mode, setMode] = useState<'relations' | 'dependencies'>('relations')
  const [selectedId, setSelectedId] = useState<string>()
  const [focusId, setFocusId] = useState<string>()
  const [detailOpen, setDetailOpen] = useState(false)
  const [mobilePane, setMobilePane] = useState<'directory' | 'canvas'>('directory')
  const generation = useRef(0)
  const narrow = useNarrow()
  const category = categories.find((item) => item.id === categoryId)

  useEffect(() => {
    const controller = new AbortController(), current = ++generation.current
    setLoading(true); setError(''); setCatalog(undefined); setDetailOpen(false)
    if (!categoryId) { setLoading(false); return () => controller.abort() }
    void loadOntologyCatalog(api, categoryId, categories, controller.signal).then((result) => {
      if (controller.signal.aborted || current !== generation.current) return
      setCatalog(result)
      setSelectedId((previous) => result.records.some((item) => item.definition.id === previous) ? previous : undefined)
      setFocusId((previous) => result.records.some((item) => item.definition.id === previous) ? previous : undefined)
    }).catch((cause: unknown) => {
      if (controller.signal.aborted || current !== generation.current) return
      setCatalog(undefined); setSelectedId(undefined); setFocusId(undefined)
      if (cause instanceof ApiError && [401, 403].includes(cause.status)) {
        try {
          const prefix = `agentforge:ontology-layout:${layoutScope}:${categoryId}:`
          for (const key of Object.keys(localStorage)) if (key.startsWith(prefix)) localStorage.removeItem(key)
        } catch { /* A disabled preference store never prevents clearing the view. */ }
      }
      setError(cause instanceof Error ? cause.message : '本体目录读取失败，请重试')
    }).finally(() => { if (!controller.signal.aborted && current === generation.current) setLoading(false) })
    return () => { controller.abort(); generation.current++ }
  }, [api, categoryId, categories, layoutScope, revision])

  const records = catalog?.records ?? []
  const local = records.filter((record) => record.definition.categoryId === categoryId)
  const term = search.trim().toLocaleLowerCase()
  const matching = local.filter(({ definition }) => (!kind || definition.kind === kind) && (!group || definition.groupId === group)
    && (!term || `${definition.name} ${definition.description ?? ''}`.toLocaleLowerCase().includes(term)))
  const selected = records.find((record) => record.definition.id === selectedId)
  const rootIds = useMemo(() => {
    if (!catalog) return []
    return catalog.records.filter(({ definition }) => definition.categoryId === categoryId && (!kind || definition.kind === kind) && (!group || definition.groupId === group)
      && (!term || `${definition.name} ${definition.description ?? ''}`.toLocaleLowerCase().includes(term))).map((record) => record.definition.id)
  }, [catalog, categoryId, kind, group, term])

  function select(id: string) {
    const record = records.find((item) => item.definition.id === id)
    if (!record) return
    setSelectedId(id); setDetailOpen(true); setFocusId(undefined)
    if (!['schema', 'link'].includes(record.definition.kind)) setMode('dependencies')
  }
  function changeFilter(update: () => void) { update(); setSelectedId(undefined); setFocusId(undefined); setDetailOpen(false) }
  function visit(id: string, callback: Props['onOpenDefinition']) {
    const record = records.find((item) => item.definition.id === id)
    if (record) callback(id, record.definition.categoryId)
  }
  const inspector = <OntologyInspector record={selected} records={records} categories={categories} groups={catalog?.groups ?? []} categoryId={categoryId} onSelect={select}
    onOpenDefinition={(id) => visit(id, onOpenDefinition)} onQuery={(id) => visit(id, onQuery)} />

  return <div className="ontology-explorer" aria-label="本体全景">
    <header className="ontology-page-heading">
      <div><span className="eyebrow">ONTOLOGY EXPLORER</span><h1>本体全景</h1><p>以对象为中心，发现业务关系与定义之间的联系。</p></div>
      <button type="button" className="button ontology-refresh" disabled={loading || !categoryId} onClick={() => setRevision((value) => value + 1)}><RefreshCw size={15} className={loading ? 'spin' : ''} />刷新本体</button>
    </header>
    <div className="ontology-scope"><span><Layers3 size={15} />{category?.name ?? '尚未选择分类'}<ChevronRight size={13} /><strong>{local.length} 项已加载定义</strong></span><span className="ontology-current-badge"><i />当前生效 · 只读浏览</span></div>
    {loading && <div className="ontology-loading" role="status"><LoaderCircle className="spin" size={24} /><p>正在读取已授权的本体定义…</p></div>}
    {error && <div className="notice notice-error" role="alert">{error}<span>已清除先前显示的目录与详情，请重新读取。</span></div>}
    {!categoryId && <div className="empty-state"><Layers3 size={30} /><h2>先选择当前分类</h2><p className="muted">本体全景跟随工作台顶部的分类。</p></div>}
    {catalog && <>
      {(catalog.warnings.length > 0 || catalog.unavailableReferences || catalog.referenceTruncated) && <div className="ontology-directory-notice" role="status">
        {catalog.warnings.map((warning) => <p key={warning}>{warning}</p>)}
        {catalog.unavailableReferences && <p>部分引用当前不可查看，仅显示已授权且已加载的定义。</p>}
        {catalog.referenceTruncated && <p>引用补齐已达到本次读取上限，当前视图不代表完整依赖范围。</p>}
      </div>}
      {!local.length ? <div className="empty-state ontology-empty"><Network size={35} /><h2>当前分类还没有已生效定义</h2><p className="muted">在本体建模中维护并生效定义后，可在这里查看它们的联系。</p></div> : <>
        <div className="ontology-type-summary" aria-label="本体定义数量">{kinds.map((item) => <button type="button" key={item.kind} className={`ontology-kind-summary ontology-kind-${item.kind} ${kind === item.kind ? 'is-selected' : ''}`} aria-pressed={kind === item.kind}
          onClick={() => changeFilter(() => { setKind(kind === item.kind ? '' : item.kind); if (!['schema', 'link'].includes(item.kind)) setMode('dependencies') })}><item.icon size={17} /><span>{item.name}</span><strong>{local.filter((record) => record.definition.kind === item.kind).length}</strong></button>)}</div>
        <div className="ontology-mobile-tabs" role="group" aria-label="浏览区域"><button type="button" aria-pressed={mobilePane === 'directory'} onClick={() => setMobilePane('directory')}><List size={15} />目录</button><button type="button" aria-pressed={mobilePane === 'canvas'} onClick={() => setMobilePane('canvas')}><Network size={15} />画布</button></div>
        <div className="ontology-panels" data-mobile-pane={mobilePane}>
          <aside className="ontology-directory" aria-label="本体目录">
            <div className="ontology-directory-heading"><h2>本体目录</h2><span>{matching.length}</span></div>
            <label className="ontology-search"><Search size={15} /><input type="search" aria-label="搜索本体定义" placeholder="搜索名称或说明" value={search} onChange={(event) => changeFilter(() => setSearch(event.target.value))} /></label>
            <div className="ontology-directory-filters"><label>类型<select aria-label="本体类型" value={kind} onChange={(event) => changeFilter(() => { const next = event.target.value as Kind | ''; setKind(next); if (next && !['schema', 'link'].includes(next)) setMode('dependencies') })}><option value="">全部类型</option>{kinds.map((item) => <option key={item.kind} value={item.kind}>{item.name}</option>)}</select></label>
              <label>分组<select aria-label="本体分组" value={group} onChange={(event) => changeFilter(() => setGroup(event.target.value))}><option value="">全部分组</option>{catalog.groups.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label></div>
            <div className="ontology-directory-list">{kinds.map((item) => {
              const entries = matching.filter((record) => record.definition.kind === item.kind)
              return !!entries.length && <section key={item.kind} className={`ontology-directory-kind ontology-kind-${item.kind}`}><h3><item.icon size={13} />{item.name}<span>{entries.length}</span></h3>{entries.map((record) => <button type="button" key={record.definition.id} className={selectedId === record.definition.id ? 'is-selected' : ''} aria-label={`查看${item.name}：${record.definition.name}`} aria-pressed={selectedId === record.definition.id} onClick={() => select(record.definition.id)}><item.icon size={15} /><span><strong>{record.definition.name}</strong>{record.definition.description && <small>{record.definition.description}</small>}</span><ChevronRight size={13} /></button>)}</section>
            })}{!matching.length && <p className="ontology-no-match">没有匹配的定义，请调整搜索或筛选。</p>}</div>
            <p className="ontology-directory-footnote">只展示当前生效定义；查看定义后可继续维护草稿。</p>
          </aside>
          <section className="ontology-canvas-panel" aria-label="本体画布">
            <div className="ontology-canvas-heading"><div role="tablist" aria-label="本体画布视图"><button type="button" role="tab" aria-selected={mode === 'relations'} onClick={() => { setMode('relations'); setFocusId(undefined) }}><GitBranch size={15} />对象关系图</button><button type="button" role="tab" aria-selected={mode === 'dependencies'} onClick={() => { setMode('dependencies'); setFocusId(undefined) }}><Network size={15} />定义依赖图</button></div>
              {focusId && <button className="button subtle" type="button" onClick={() => setFocusId(undefined)}><X size={13} />取消聚焦</button>}</div>
            <p className="ontology-canvas-description">{mode === 'relations' ? '对象通过 Link 连接，方向和基数说明业务关系。' : '沿已声明的引用，查看枚举、作用对象及函数绑定。'}</p>
            <OntologyGraph records={records} rootIds={rootIds} categories={categories} categoryId={categoryId} mode={mode} selectedId={selectedId} focusId={focusId} onSelect={select} onFocus={setFocusId} layoutKey={layoutScope} />
            <div className="ontology-canvas-caption"><span>画布展示匹配资源及其已加载关联</span>{selected && <button type="button" className="button subtle" onClick={() => { setDetailOpen(true); setFocusId(selectedId) }}>聚焦所选定义</button>}</div>
          </section>
          {!narrow && <aside className="ontology-detail-panel" aria-label="本体详情">{inspector}</aside>}
        </div>
        {narrow && detailOpen && selected && <Dialog title="本体详情" onClose={() => setDetailOpen(false)}><div className="ontology-mobile-inspector">{inspector}<button type="button" className="button" onClick={() => { setFocusId(selectedId); setDetailOpen(false); setMobilePane('canvas') }}>在画布中聚焦</button></div></Dialog>}
      </>}
    </>}
  </div>
}
