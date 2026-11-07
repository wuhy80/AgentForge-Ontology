import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Check, ChevronRight, Folder, FolderPlus, Layers2, LoaderCircle, Plus, RefreshCw, ShieldCheck } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import type { Organization } from '../../lib/types'
import './organizations.css'

type Props = {
  api: ApiClient
  categories: Organization[]
  categoryId: string
  onSelectCategory: (id: string) => void
  onRefresh: () => Promise<void>
}
const messageOf = (cause: unknown) => cause instanceof Error ? cause.message : '请求失败，请稍后重试。'
const forbidden = (cause: unknown) => cause instanceof ApiError && (cause.status === 401 || cause.status === 403)

export function OrganizationsPage({ api, categories, categoryId, onSelectCategory, onRefresh }: Props) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState<'create' | 'refresh'>()
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [unavailable, setUnavailable] = useState(false)
  const [revision, setRevision] = useState(0)
  const current = categories.find((category) => category.id === categoryId)
  const scope = useRef({ api, mounted: true })
  const operation = useRef<symbol | undefined>(undefined)
  scope.current.api = api
  useEffect(() => {
    scope.current.mounted = true
    setName(''); setBusy(undefined); setError(''); setMessage(''); setUnavailable(false)
    operation.current = undefined
    return () => { scope.current.mounted = false }
  }, [api])

  function denied(cause: unknown) {
    setUnavailable(true); setError(messageOf(cause)); setMessage('')
  }
  async function run(kind: 'create' | 'refresh', event?: FormEvent) {
    event?.preventDefault()
    const trimmed = name.trim()
    if (operation.current || (kind === 'create' && (!trimmed || unavailable))) return
    const job = Symbol(); operation.current = job
    const active = () => scope.current.mounted && scope.current.api === api && operation.current === job
    setBusy(kind); setError(''); setMessage('')
    try {
      const category = kind === 'create' ? await api.createCategory(trimmed) : undefined
      if (!active()) return
      if (category) { setName(''); setMessage(`分类“${category.name}”已创建。`) }
      try {
        await onRefresh()
      } catch (cause) {
        if (active()) {
          setUnavailable(true)
          setError(category ? `分类已创建，但目录刷新失败：${messageOf(cause)}请刷新目录后继续。` : messageOf(cause))
          if (category) onSelectCategory(category.id)
        }
        return
      }
      if (!active()) return
      setUnavailable(false); setRevision((value) => value + 1)
      if (category) onSelectCategory(category.id)
    } catch (cause) {
      if (!active()) return
      setError(messageOf(cause))
      if (forbidden(cause)) denied(cause)
    } finally {
      if (active()) { operation.current = undefined; setBusy(undefined) }
    }
  }

  return <div className="organizations-page">
    <header className="organizations-heading"><div><div className="organizations-eyebrow">WORKSPACE ORGANIZATION</div><h1>分类与分组</h1><p className="muted">按业务划分分类，通过分组整理对象、枚举、关系、操作和函数。</p></div><button type="button" className="button" disabled={!!busy} onClick={() => { void run('refresh') }}><RefreshCw size={15} />刷新目录</button></header>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
    {unavailable ? <div className="card empty-state"><ShieldCheck size={30} /><h2>目录暂不可用</h2><p className="muted">刷新目录后，将重新确认当前可访问的分类与分组。</p></div> : <div className="organizations-layout">
      <section className="card organizations-categories" aria-labelledby="organizations-categories-title">
        <div className="organizations-section-heading"><div><Folder size={19} /><h2 id="organizations-categories-title">业务分类</h2></div><span className="badge">{categories.length} 个</span></div>
        <p className="organizations-section-description muted">选择分类后，工作台会同步切换到该分类。</p>
        {categories.length ? <ul className="organizations-category-list">{categories.map((category) => <li key={category.id}><button type="button" aria-pressed={category.id === categoryId} disabled={!!busy} className={category.id === categoryId ? 'is-selected' : ''} onClick={() => onSelectCategory(category.id)}><span className="organizations-category-icon"><Folder size={18} /></span><span className="organizations-category-name">{category.name}</span>{category.id === categoryId ? <span className="organizations-current-label">当前分类</span> : <ChevronRight size={16} />}</button></li>)}</ul> : <div className="organizations-inline-empty"><FolderPlus size={27} /><strong>还没有业务分类</strong><p className="muted">先创建分类，再添加分组与业务定义。</p></div>}
        <form className="organizations-create" onSubmit={(event) => { void run('create', event) }}><label>新分类名称<input className="field" value={name} maxLength={128} disabled={!!busy} onChange={(event) => setName(event.target.value)} placeholder="例如：生产管理" /></label><button type="submit" className="button primary" disabled={!!busy || !name.trim()}>{busy === 'create' ? <LoaderCircle size={16} className="organizations-spin" /> : <Plus size={16} />}创建分类</button></form>
      </section>
      {current ? <GroupsForCategory key={current.id} api={api} category={current} revision={revision} disabled={!!busy} onForbidden={denied} /> : <section className="card organizations-no-category empty-state"><Layers2 size={32} /><h2>请选择一个分类</h2><p className="muted">分类下的分组将显示在这里。</p></section>}
    </div>}
  </div>
}

function GroupsForCategory({ api, category, revision, disabled, onForbidden }: {
  api: ApiClient; category: Organization; revision: number; disabled: boolean; onForbidden: (cause: unknown) => void
}) {
  const [state, setState] = useState<{ api: ApiClient; groups: Organization[]; loading: boolean; error: string }>({ api, groups: [], loading: true, error: '' })
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [reload, setReload] = useState(0)
  const request = useRef<AbortController | undefined>(undefined)
  const scope = useRef({ api, mounted: true })
  const operation = useRef<symbol | undefined>(undefined)
  scope.current.api = api
  useEffect(() => {
    scope.current.mounted = true
    return () => { scope.current.mounted = false }
  }, [])
  useEffect(() => {
    operation.current = undefined; setBusy(false); setName(''); setMessage('')
  }, [api])
  useEffect(() => {
    const controller = new AbortController(); request.current = controller
    setState({ api, groups: [], loading: true, error: '' })
    api.listGroups(category.id, controller.signal).then((groups) => {
      if (!controller.signal.aborted) setState({ api, groups, loading: false, error: '' })
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return
      setState({ api, groups: [], loading: false, error: messageOf(cause) })
      if (forbidden(cause)) onForbidden(cause)
    })
    return () => controller.abort()
    // Callbacks and display-name changes do not restart the category request.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, category.id, revision, reload])
  const visible = state.api === api ? state : { groups: [], loading: true, error: '' }

  async function create(event: FormEvent) {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || disabled || operation.current) return
    const job = Symbol(); operation.current = job
    setBusy(true); setMessage('')
    const active = () => scope.current.mounted && scope.current.api === api && operation.current === job
    try {
      const group = await api.createGroup(category.id, trimmed)
      if (!active()) return
      request.current?.abort()
      setName(''); setState({ api, groups: [], loading: true, error: '' }); setReload((value) => value + 1)
      setMessage(`分组“${group.name}”已创建。`)
    } catch (cause) {
      if (!active()) return
      request.current?.abort()
      setState({ api, groups: [], loading: false, error: messageOf(cause) })
      if (forbidden(cause)) onForbidden(cause)
    } finally {
      if (active()) { operation.current = undefined; setBusy(false) }
    }
  }
  return <section className="card organizations-groups" aria-labelledby="organizations-groups-title">
    <div className="organizations-section-heading"><div><Layers2 size={19} /><h2 id="organizations-groups-title">{category.name}的分组</h2></div><span className="badge">{visible.loading ? '读取中' : `${visible.groups.length} 个`}</span></div>
    <p className="organizations-section-description muted">分组用于归置业务定义，分类内五类定义共享业务名称。</p>
    {visible.error && <div className="notice notice-error organizations-notice" role="alert">{visible.error}<button type="button" className="button subtle" disabled={busy || disabled} onClick={() => setReload((value) => value + 1)}>重新读取分组</button></div>}
    {message && <div className="notice notice-success organizations-notice" role="status"><Check size={16} />{message}</div>}
    {visible.loading ? <div className="organizations-inline-empty" role="status"><LoaderCircle size={26} className="organizations-spin" /><p className="muted">正在读取当前分类的分组…</p></div> : visible.error ? <div className="organizations-inline-empty"><Layers2 size={27} /><p className="muted">当前分组未能读取，请重新读取后继续。</p></div> : visible.groups.length ? <ul className="organizations-group-list">{visible.groups.map((group) => <li key={group.id}><span className="organizations-group-icon"><Layers2 size={19} /></span><div><strong>{group.name}</strong><span className="muted">{category.name}</span></div></li>)}</ul> : <div className="organizations-inline-empty"><Layers2 size={28} /><strong>当前分类还没有分组</strong><p className="muted">创建分组后，即可在其中维护业务定义。</p></div>}
    <form className="organizations-create" onSubmit={(event) => { void create(event) }}><label>新分组名称<input className="field" value={name} maxLength={128} disabled={busy || disabled} onChange={(event) => setName(event.target.value)} placeholder="例如：基础资料" /></label><button type="submit" className="button primary" disabled={busy || disabled || !name.trim()}>{busy ? <LoaderCircle size={16} className="organizations-spin" /> : <Plus size={16} />}创建分组</button></form>
  </section>
}
