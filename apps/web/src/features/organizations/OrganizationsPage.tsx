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
  const [renaming, setRenaming] = useState<Organization>()
  const [deleting, setDeleting] = useState<Organization>()
  const [busy, setBusy] = useState<'create' | 'refresh' | 'delete'>()
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
    setName(''); setRenaming(undefined); setDeleting(undefined); setBusy(undefined); setError(''); setMessage(''); setUnavailable(false)
    operation.current = undefined
    return () => { scope.current.mounted = false }
  }, [api])

  function denied(cause: unknown) {
    setUnavailable(true); setError(messageOf(cause)); setMessage('')
  }
  async function run(kind: 'create' | 'refresh' | 'delete', event?: FormEvent) {
    event?.preventDefault()
    const trimmed = name.trim()
    if (operation.current || (kind === 'create' && (!trimmed || unavailable)) || (kind === 'delete' && (!deleting || deleting.id !== current?.id || unavailable))) return
    const job = Symbol(); operation.current = job
    const active = () => scope.current.mounted && scope.current.api === api && operation.current === job
    setBusy(kind); setError(''); setMessage('')
    try {
      const category = kind === 'create' ? (renaming ? await api.renameOrganization('categories', renaming, trimmed) : await api.createCategory(trimmed)) : undefined
      if (kind === 'delete' && deleting) {
        await api.deleteOrganization('categories', deleting)
        if (!active()) return
        setDeleting(undefined)
        setMessage(`分类“${deleting.name}”已删除。`)
      }
      if (!active()) return
      if (category) { setName(''); setRenaming(undefined); setMessage(`分类“${category.name}”已${renaming ? '重命名' : '创建'}。`) }
      try {
        await onRefresh()
      } catch (cause) {
        if (active()) {
          setUnavailable(true)
          setError(category ? `分类已${renaming ? '重命名' : '创建'}，但目录刷新失败：${messageOf(cause)}请刷新目录后继续。` : kind === 'delete' ? `分类已删除，目录刷新失败：${messageOf(cause)}` : messageOf(cause))
          if (category && !renaming) onSelectCategory(category.id)
        }
        return
      }
      if (!active()) return
      setUnavailable(false); setRevision((value) => value + 1)
      if (category && !renaming) onSelectCategory(category.id)
    } catch (cause) {
      if (!active()) return
      setError(messageOf(cause))
      if (forbidden(cause)) denied(cause)
    } finally {
      if (active()) { operation.current = undefined; setBusy(undefined) }
    }
  }

  const refreshDirectory = <button type="button" className="button" disabled={!!busy} onClick={() => { void run('refresh') }}><RefreshCw size={15} />刷新目录</button>
  return <div className="organizations-page">
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
    {unavailable ? <div className="card empty-state"><ShieldCheck size={30} /><h2>目录暂不可用</h2><p className="muted">刷新目录后，将重新确认当前可访问的分类与分组。</p>{refreshDirectory}</div> : <div className="organizations-layout">
      <section className="card organizations-categories" aria-labelledby="organizations-categories-title">
        <div className="organizations-section-heading"><div><Folder size={19} /><h2 id="organizations-categories-title">业务分类</h2></div><div className="organizations-category-actions"><span className="badge">{categories.length} 个</span>{refreshDirectory}</div></div>
        <p className="organizations-section-description muted">选择分类后，工作台会同步切换到该分类。</p>
        {categories.length ? <ul className="organizations-category-list">{categories.map((category) => <li key={category.id}><button type="button" aria-pressed={category.id === categoryId} disabled={!!busy} className={category.id === categoryId ? 'is-selected' : ''} onClick={() => { setDeleting(undefined); onSelectCategory(category.id) }}><span className="organizations-category-icon"><Folder size={18} /></span><span className="organizations-category-name">{category.name}</span>{category.id === categoryId ? <span className="organizations-current-label">当前分类</span> : <ChevronRight size={16} />}</button></li>)}</ul> : <div className="organizations-inline-empty"><FolderPlus size={27} /><strong>还没有业务分类</strong><p className="muted">先创建分类，再添加分组与业务定义。</p></div>}
        {current && !renaming && !deleting && <button type="button" className="button subtle" disabled={!!busy} onClick={() => { setRenaming(current); setName(current.name) }}>重命名当前分类</button>}
        {current && !renaming && deleting?.id !== current.id && <button type="button" className="button subtle" disabled={!!busy} onClick={() => setDeleting(current)}>删除当前分类</button>}
        {deleting && deleting.id === current?.id && <DirectoryDelete kind="分类" item={deleting} busy={!!busy} onCancel={() => setDeleting(undefined)} onConfirm={() => void run('delete')} />}
        <form className="organizations-create" onSubmit={(event) => { void run('create', event) }}><label>{renaming ? `分类“${renaming.name}”的新名称` : '新分类名称'}<input className="field" value={name} maxLength={128} disabled={!!busy} onChange={(event) => setName(event.target.value)} placeholder="例如：生产管理" /></label><button type="submit" className="button primary" disabled={!!busy || !name.trim()}>{busy === 'create' ? <LoaderCircle size={16} className="organizations-spin" /> : <Plus size={16} />}{renaming ? '保存分类名称' : '创建分类'}</button>{renaming && <button type="button" className="button subtle" disabled={!!busy} onClick={() => { setRenaming(undefined); setName('') }}>取消重命名</button>}</form>
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
  const [renaming, setRenaming] = useState<Organization>()
  const [deleting, setDeleting] = useState<Organization>()
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [deleteError, setDeleteError] = useState('')
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
    operation.current = undefined; setBusy(false); setName(''); setRenaming(undefined); setDeleting(undefined); setMessage(''); setDeleteError('')
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

  async function save(event?: FormEvent, remove?: Organization) {
    event?.preventDefault()
    const trimmed = name.trim()
    if ((!trimmed && !remove) || disabled || operation.current) return
    const job = Symbol(); operation.current = job
    setBusy(true); setMessage(''); setDeleteError('')
    const active = () => scope.current.mounted && scope.current.api === api && operation.current === job
    try {
      if (remove) await api.deleteOrganization('groups', remove)
      const group = remove ?? (renaming ? await api.renameOrganization('groups', renaming, trimmed) : await api.createGroup(category.id, trimmed))
      if (!active()) return
      request.current?.abort()
      setName(''); setRenaming(undefined); setDeleting(undefined); setState({ api, groups: [], loading: true, error: '' }); setReload((value) => value + 1)
      setMessage(`分组“${group.name}”已${remove ? '删除' : renaming ? '重命名' : '创建'}。`)
    } catch (cause) {
      if (!active()) return
      if (remove) setDeleteError(messageOf(cause))
      else {
        request.current?.abort()
        setState({ api, groups: [], loading: false, error: messageOf(cause) })
      }
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
    {visible.loading ? <div className="organizations-inline-empty" role="status"><LoaderCircle size={26} className="organizations-spin" /><p className="muted">正在读取当前分类的分组…</p></div> : visible.error ? <div className="organizations-inline-empty"><Layers2 size={27} /><p className="muted">当前分组未能读取，请重新读取后继续。</p></div> : visible.groups.length ? <ul className="organizations-group-list">{visible.groups.map((group) => <li key={group.id}><span className="organizations-group-icon"><Layers2 size={19} /></span><div><strong>{group.name}</strong><span className="muted">{category.name}</span></div><button type="button" className="button subtle" disabled={busy || disabled} aria-label={`重命名分组 ${group.name}`} onClick={() => { setDeleting(undefined); setDeleteError(''); setRenaming(group); setName(group.name) }}>重命名</button><button type="button" className="button subtle" disabled={busy || disabled || !!renaming} aria-label={`删除分组 ${group.name}`} onClick={() => { setDeleteError(''); setDeleting(group) }}>删除</button></li>)}</ul> : <div className="organizations-inline-empty"><Layers2 size={28} /><strong>当前分类还没有分组</strong><p className="muted">创建分组后，即可在其中维护业务定义。</p></div>}
    {deleteError && <div className="notice notice-error" role="alert">{deleteError}</div>}
    {deleting && <DirectoryDelete kind="分组" item={deleting} busy={busy || disabled} onCancel={() => { setDeleting(undefined); setDeleteError('') }} onConfirm={() => void save(undefined, deleting)} />}
    <form className="organizations-create" onSubmit={(event) => { void save(event) }}><label>{renaming ? `分组“${renaming.name}”的新名称` : '新分组名称'}<input className="field" value={name} maxLength={128} disabled={busy || disabled} onChange={(event) => setName(event.target.value)} placeholder="例如：基础资料" /></label><button type="submit" className="button primary" disabled={busy || disabled || !name.trim()}>{busy ? <LoaderCircle size={16} className="organizations-spin" /> : <Plus size={16} />}{renaming ? '保存分组名称' : '创建分组'}</button>{renaming && <button type="button" className="button subtle" disabled={busy || disabled} onClick={() => { setRenaming(undefined); setName('') }}>取消重命名</button>}</form>
  </section>
}

function DirectoryDelete({ kind, item, busy, onCancel, onConfirm }: {
  kind: '分类' | '分组'; item: Organization; busy: boolean; onCancel: () => void; onConfirm: () => void
}) {
  return <div className="notice notice-error" role="group" aria-label={`删除${kind}确认`}>
    <p>确认删除{kind}“{item.name}”？仅可删除空目录。</p>
    <button type="button" className="button subtle" disabled={busy} onClick={onCancel}>取消</button>
    <button type="button" className="button danger" disabled={busy} onClick={onConfirm}>确认删除</button>
  </div>
}
