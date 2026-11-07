import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import {
  ArrowRight,
  BookOpen,
  Box,
  Braces,
  Check,
  ChevronRight,
  Database,
  FileEdit,
  FolderTree,
  GitBranch,
  Layers3,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Search,
  ShieldCheck,
  Sparkles,
  Workflow,
  Users,
} from 'lucide-react'
import { ApiClient, ApiError } from './lib/api'
import type { DataCapabilities, DefinitionRecord, MetadataCapabilities, Organization, Session as AccountSession } from './lib/types'
import { ModelsPage } from './features/model/ModelsPage'
import { QueryPage } from './features/query/QueryPage'
import { DialogScope } from './components/Dialog'
import { Dialog } from './components/Dialog'
import { AdminPage } from './features/admin/AdminPage'
import { OrganizationsPage } from './features/organizations/OrganizationsPage'
import { SourcesPage } from './features/sources/SourcesPage'

type View = 'overview' | 'organizations' | 'sources' | 'models' | 'query' | 'admin'
type Identity = { tenantId: string; subject: string; expiresAt: number }
type Session = { api: ApiClient; identity: Identity; metadata: MetadataCapabilities; data: DataCapabilities; account: AccountSession }
const kindNames = { schema: '对象类型', enum: '枚举', link: '关系', action: '操作', function: '函数' }
const nav = [
  { id: 'overview' as const, name: '工作空间', subtitle: '概览', icon: LayoutDashboard },
  { id: 'organizations' as const, name: '分类与分组', subtitle: '维护业务目录', icon: FolderTree },
  { id: 'sources' as const, name: '数据源配置', subtitle: '管理数据库连接', icon: Database },
  { id: 'models' as const, name: '模型管理', subtitle: '定义业务世界', icon: Layers3 },
  { id: 'query' as const, name: '数据探索', subtitle: '让数据回答问题', icon: Search },
  { id: 'admin' as const, name: '用户与权限', subtitle: '管理成员和访问范围', icon: Users },
]

function currentView(route = window.location.hash.slice(1)): View {
  const hash = route.split(/[/?]/)[0]
  return hash === 'organizations' || hash === 'sources' || hash === 'models' || hash === 'query' || hash === 'admin' ? hash : 'overview'
}

function Brand({ light = false }: { light?: boolean }) {
  return (
    <div className={`brand ${light ? 'brand-light' : ''}`}>
      <span className="brand-symbol">
        <Box size={23} strokeWidth={1.7} />
      </span>
      <span>
        <strong>AgentForge</strong>
        <small>ONTOLOGY</small>
      </span>
    </div>
  )
}

function OntologyIllustration() {
  return (
    <div className="ontology-illustration" aria-hidden="true">
      <div className="graph-orbit orbit-one" />
      <div className="graph-orbit orbit-two" />
      <svg className="graph-lines" viewBox="0 0 460 285" fill="none">
        <path
          d="M230 143 95 73M230 143 355 60M230 143 95 223M230 143 365 218"
          stroke="currentColor"
          strokeDasharray="5 6"
        />
        <circle cx="230" cy="143" r="58" stroke="currentColor" strokeOpacity=".15" />
      </svg>
      <div className="graph-node graph-center">
        <Box size={30} />
        <strong>业务本体</strong>
        <small>Ontology</small>
      </div>
      <div className="graph-node graph-schema">
        <Layers3 size={19} />
        <span>对象类型</span>
        <small>Schema</small>
      </div>
      <div className="graph-node graph-link">
        <GitBranch size={19} />
        <span>关系</span>
        <small>Link</small>
      </div>
      <div className="graph-node graph-enum">
        <Braces size={19} />
        <span>枚举</span>
        <small>Enum</small>
      </div>
      <div className="graph-node graph-action">
        <Workflow size={19} />
        <span>操作与函数</span>
        <small>Action · Function</small>
      </div>
    </div>
  )
}

function Connect({ onConnect, reason }: { onConnect: (tenant: string, username: string, password: string) => Promise<void>; reason: string }) {
  const [tenant, setTenant] = useState('demo-tenant')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await onConnect(tenant.trim(), username.trim(), password)
      setPassword('')
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : '连接失败，请稍后重试')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="connect-page">
      <section className="connect-story">
        <Brand light />
        <div className="connect-story-content">
          <span className="eyebrow">让业务知识成为共同语言</span>
          <h1>
            理解业务。
            <br />
            连接数据。
            <br />
            <span>构建智能的基础。</span>
          </h1>
          <p>
            将对象、关系和业务规则汇聚在一个工作空间，
            <br />
            让每一份数据都有清晰的含义。
          </p>
          <OntologyIllustration />
        </div>
        <div className="connect-story-footer">
          <span className="live-dot" /> 模型驱动 · 中文定义 · 受控访问
        </div>
      </section>
      <main className="connect-form-section">
        <div className="connect-form">
          <span className="product-label">
            <Box size={17} /> ONTOLOGY WORKSPACE
          </span>
          <h2>登录你的工作空间</h2>
          <p className="connect-intro">从业务模型开始，探索数据的更多可能。</p>
          {reason && (
            <div className="notice" role="status">
              {reason}
            </div>
          )}
          {error && (
            <div className="notice notice-error" role="alert">
              {error}
            </div>
          )}
          <form onSubmit={submit}>
            <label className="field">租户<input name="tenant" autoComplete="organization" value={tenant} onChange={(event) => setTenant(event.target.value)} required disabled={busy} /></label>
            <label className="field">用户名<input name="username" autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} required disabled={busy} /></label>
            <label className="field">
              密码
              <input
                type="password"
                name="password"
                autoComplete="current-password"
                spellCheck={false}
                placeholder="请输入密码"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
                disabled={busy}
              />
            </label>
            <p className="field-hint">
              <ShieldCheck size={15} /> 账号与访问权限由你的租户管理员管理。
            </p>
            <button className="button primary connect-submit" disabled={busy || !tenant.trim() || !username.trim() || !password}>
              {busy ? <LoaderCircle className="spin" size={18} /> : null}
              {busy ? '正在登录…' : '登录'}
              {!busy && <ArrowRight size={18} />}
            </button>
          </form>
          <p className="field-hint">忘记密码或需要开通账号，请联系租户管理员。</p>
          <div className="connect-footer">
            AgentForge Ontology <span>首批工作台</span>
          </div>
        </div>
      </main>
    </div>
  )
}

export function App() {
  const [session, setSession] = useState<Session | null>(null)
  const [reason, setReason] = useState('')
  const connecting = useRef(0)
  const activeClient = useRef<ApiClient | null>(null)
  const [checking, setChecking] = useState(true)
  const [disconnecting, setDisconnecting] = useState(false)
  const establish = useCallback(async (credentials?: { tenant: string; username: string; password: string }) => {
    const request = ++connecting.current
    const api: ApiClient = new ApiClient(undefined, () => {
      if (activeClient.current !== api) return
      activeClient.current = null
      setSession(null)
      setReason('登录已失效，请重新登录。')
    })
    const account = credentials ? await api.login(credentials) : await api.getSession()
    const identity = { tenantId: account.user.tenantId, subject: account.user.displayName || account.user.username, expiresAt: account.expiresAt }
    const [metadata, data] = await Promise.all([api.metadataCapabilities(), api.dataCapabilities()])
    if (request !== connecting.current) return
    activeClient.current = api
    setReason('')
    setSession({ api, identity, metadata, data, account })
  }, [])
  const connect = (tenant: string, username: string, password: string) => establish({ tenant, username, password })
  useEffect(() => {
    let mounted = true
    void establish().catch((failure) => {
      if (mounted && !(failure instanceof ApiError && failure.status === 401)) setReason(failure instanceof Error ? failure.message : '无法检查登录状态')
    }).finally(() => { if (mounted) setChecking(false) })
    return () => { mounted = false; ++connecting.current }
  }, [establish])
  useEffect(() => {
    if (!session) return
    const expires = session.identity.expiresAt * 1000 - Date.now()
    const timer = window.setTimeout(
      () => {
        activeClient.current = null
        setReason('登录已过期，请重新登录。')
        setSession(null)
      },
      Math.max(0, Math.min(expires, 2147483647)),
    )
    return () => window.clearTimeout(timer)
  }, [session])
  if (checking) return <div className="empty-state"><LoaderCircle className="spin" size={25} /><p>正在检查登录状态…</p></div>
  if (!session) return <Connect onConnect={connect} reason={reason} />
  return (
    <Workspace
      key={`${session.identity.tenantId}:${session.account.user.id}`}
      session={session}
      onAccountUpdated={(account) => setSession((value) => value?.api === session.api ? { ...value, account, identity: { tenantId: account.user.tenantId, subject: account.user.displayName || account.user.username, expiresAt: account.expiresAt } } : value)}
      notice={reason}
      disconnecting={disconnecting}
      onDisconnect={async () => {
        if (disconnecting) return
        setDisconnecting(true)
        try {
          await session.api.logout()
          ++connecting.current
          activeClient.current = null
          setReason('')
          setSession(null)
        } catch (failure) { setReason(failure instanceof Error ? failure.message : '退出失败，请重试') }
        finally { setDisconnecting(false) }
      }}
    />
  )
}

function Workspace({ session, onDisconnect, onAccountUpdated, notice, disconnecting }: { session: Session; onDisconnect: () => Promise<void>; onAccountUpdated: (account: AccountSession) => void; notice: string; disconnecting: boolean }) {
  const [route, setRoute] = useState(() => window.location.hash.slice(1))
  const view = currentView(route)
  const routeRef = useRef(route)
  routeRef.current = route
  const navigationGuard = useRef<(() => boolean) | undefined>(undefined)
  const registerNavigationGuard = useCallback((guard?: () => boolean) => { navigationGuard.current = guard }, [])
  const categoryStorageKey = `ontology.category.${encodeURIComponent(session.identity.tenantId)}.${encodeURIComponent(session.account.user.id)}`
  const [selectedCategoryId, setSelectedCategoryId] = useState(() => {
    try { return sessionStorage.getItem(categoryStorageKey) ?? '' } catch { return '' }
  })
  const [categories, setCategories] = useState<Organization[]>([])
  const [current, setCurrent] = useState<DefinitionRecord[]>([])
  const [entries, setEntries] = useState<DefinitionRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const [initialSchemaId, setInitialSchemaId] = useState<string>()
  const [selectionRequest, setSelectionRequest] = useState(0)
  const [draftDirectoryDenied, setDraftDirectoryDenied] = useState(false)
  const [help, setHelp] = useState(false)
  const [passwordOpen, setPasswordOpen] = useState(false)
  const requests = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    const sequence = ++requests.current
    controller.current?.abort()
    const active = new AbortController()
    controller.current = active
    setLoading(true)
    setError('')
    try {
      const [categoryResult, currentResult, draftResult] = await Promise.allSettled([
        session.api.listCategories(active.signal),
        session.api.listDefinitions({ view: 'current' }, active.signal),
        session.api.listDefinitions({ view: 'draft' }, active.signal),
      ])
      if (sequence !== requests.current || active.signal.aborted) return
      if (categoryResult.status === 'rejected') throw categoryResult.reason
      const nextCategories = categoryResult.value
      const nextCurrent = currentResult.status === 'fulfilled' ? currentResult.value : []
      const drafts = draftResult.status === 'fulfilled' ? draftResult.value : []
      const directoryFailure = currentResult.status === 'rejected' ? currentResult.reason
        : draftResult.status === 'rejected' && !(draftResult.reason instanceof ApiError && draftResult.reason.status === 403) ? draftResult.reason : undefined
      if (directoryFailure) setError(directoryFailure instanceof Error ? directoryFailure.message : '模型目录读取失败，请重试')
      const merged = new Map(
        nextCurrent.map((record) => [record.definition.id, { ...record, hasCurrent: true }]),
      )
      for (const record of drafts)
        merged.set(record.definition.id, { ...record, hasCurrent: merged.has(record.definition.id) })
      setDraftDirectoryDenied(draftResult.status === 'rejected')
      setCategories(nextCategories)
      setSelectedCategoryId((previous) => nextCategories.some((category) => category.id === previous)
        ? previous : nextCategories[0]?.id ?? '')
      setCurrent(nextCurrent)
      setEntries(
        Array.from(merged.values()).sort((left, right) =>
          left.definition.name.localeCompare(right.definition.name, 'zh-CN'),
        ),
      )
    } catch (failure) {
      if (sequence !== requests.current || active.signal.aborted) return
      setCategories([])
      setCurrent([])
      setEntries([])
      setDraftDirectoryDenied(true)
      setError(failure instanceof Error ? failure.message : '无法加载工作空间，请重试')
      throw failure
    } finally {
      if (sequence === requests.current && !active.signal.aborted) {
        setLoading(false)
        setLoaded(true)
      }
    }
  }, [session.api])
  // A stored preference is usable only after the current authorized directory
  // confirms it. This is a UI context, never an authorization claim.
  const categoryId = categories.some((category) => category.id === selectedCategoryId) ? selectedCategoryId : ''
  useEffect(() => {
    if (!categoryId) return
    try { sessionStorage.setItem(categoryStorageKey, categoryId) } catch { /* Selection still works when browser storage is unavailable. */ }
  }, [categoryId, categoryStorageKey])
  useEffect(() => {
    void refresh().catch(() => {})
    return () => {
      ++requests.current
      controller.current?.abort()
    }
  }, [refresh])
  useEffect(() => {
    const update = () => {
      const next = window.location.hash.slice(1)
      if (next === routeRef.current) return
      if (navigationGuard.current && !navigationGuard.current()) {
        window.history.replaceState(null, '', `#${routeRef.current}`)
        return
      }
      setRoute(next)
    }
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])
  const navigateModel = (path: string, replace = false) => {
    routeRef.current = path
    setRoute(path)
    if (replace) window.history.replaceState(null, '', `#${path}`)
    else window.location.hash = path
  }
  const navigate = (next: View, preserveEditor = false) => {
    if (!preserveEditor && navigationGuard.current && !navigationGuard.current()) return
    navigateModel(next)
  }
  const selectCategory = (id: string) => {
    if (id === selectedCategoryId) return
    if (navigationGuard.current && !navigationGuard.current()) return
    setSelectedCategoryId(id)
    setInitialSchemaId(undefined)
    setSelectionRequest((value) => value + 1)
    if (view === 'models' && route.startsWith('models/')) navigateModel('models', true)
  }
  useEffect(() => {
    if (!loaded || !route.startsWith('models/') || route.startsWith('models/new')) return
    let id: string
    try { id = decodeURIComponent(route.slice(7).split('?')[0]) } catch { return }
    const definition = entries.find((entry) => entry.definition.id === id)?.definition
    if (definition && categories.some((category) => category.id === definition.categoryId)) setSelectedCategoryId(definition.categoryId)
  }, [loaded, route, entries, categories])
  const query = (schemaId: string) => {
    setInitialSchemaId(schemaId)
    setSelectionRequest((value) => value + 1)
    navigate('query')
  }
  const scopedCurrent = current.filter((record) => record.definition.categoryId === categoryId)
  const scopedEntries = entries.filter((record) => record.definition.categoryId === categoryId)
  const schemas = scopedCurrent.filter((record) => record.definition.kind === 'schema')
  const title = nav.find((item) => item.id === view)!.name
  return (
    <div className="app-layout">
      <a
        className="skip-link"
        href="#main-content"
        onClick={(event) => {
          event.preventDefault()
          document.getElementById('main-content')?.focus()
        }}
      >
        跳转到主要内容
      </a>
      <aside className="sidebar">
        <Brand light />
        <div className="workspace-switch">
          <span className="workspace-avatar">O</span>
          <span>
            <strong>Ontology 工作空间</strong>
            <small title={session.identity.tenantId}>{session.identity.tenantId}</small>
          </span>
          <ShieldCheck size={16} />
        </div>
        <div className="nav-label">工作台</div>
        <nav aria-label="工作台导航">
          {nav.filter((item) => !['admin', 'sources'].includes(item.id) || session.account.user.administrator).map((item) => (
            <a
              href={`#${item.id}`}
              onClick={(event) => { event.preventDefault(); navigate(item.id) }}
              key={item.id}
              className={`nav-item ${view === item.id ? 'active' : ''}`}
              aria-label={item.name}
              aria-current={view === item.id ? 'page' : undefined}
            >
              <item.icon size={19} strokeWidth={1.7} />
              <span>{item.name}</span>
              {view === item.id && <ChevronRight size={15} />}
            </a>
          ))}
        </nav>
        <div className="sidebar-guide">
          <span className="guide-icon">
            <Sparkles size={18} />
          </span>
          <strong>从模型到数据</strong>
          <p>
            用中文定义业务对象，
            <br />
            让数据建立共同的语义。
          </p>
          <button type="button" onClick={() => navigate('models')}>
            开始建模 <ArrowRight size={14} />
          </button>
        </div>
        <div className="sidebar-bottom">
          <button
            type="button"
            className="nav-item"
            aria-label="使用指引"
            onClick={() => setHelp((previous) => !previous)}
            aria-expanded={help}
          >
            <BookOpen size={18} />
            <span>使用指引</span>
          </button>
          <div className="sidebar-version">
            <span className="live-dot" /> 本地开发环境<span>0.1</span>
          </div>
        </div>
      </aside>
      <div className="workspace-main">
        <header className="topbar">
          <div className="breadcrumbs">
            Ontology <ChevronRight size={14} />
            <span>{title}</span>
            {loading && loaded && <LoaderCircle size={13} className="spin" aria-label="正在刷新" />}
          </div>
          <div className="topbar-right">
            <label className="workspace-category">
              <span>当前分类</span>
              <select aria-label="当前分类" value={categoryId} onChange={(event) => selectCategory(event.target.value)} disabled={!categories.length || loading}>
                {!categoryId && <option value="">{loading ? '正在读取分类…' : '暂无可用分类'}</option>}
                {categories.map((category) => <option key={category.id} value={category.id}>{category.name}</option>)}
              </select>
            </label>
            <span className="connection-status">
              <span className="live-dot" /> 已连接
            </span>
            <span className="topbar-separator" />
            <span className="user-avatar">{session.identity.subject.slice(0, 1).toUpperCase()}</span>
            <span className="subject-name">{session.identity.subject}</span>
            <button className="button subtle" type="button" onClick={() => setPasswordOpen(true)}>修改密码</button>
            <button
              type="button"
              className="icon-button"
              aria-label="退出工作空间"
              title="退出工作空间"
              onClick={() => { if (!navigationGuard.current || navigationGuard.current()) void onDisconnect() }}
              disabled={disconnecting}
            >
              <LogOut size={17} />
            </button>
          </div>
        </header>
        <main id="main-content" className="main-content" tabIndex={-1}>
          {notice && <div className="notice notice-error" role="alert">{notice}</div>}
          {passwordOpen && <ChangePassword api={session.api} onUpdated={onAccountUpdated} onClose={() => setPasswordOpen(false)} />}
          {help && (
            <div className="help-panel card">
              <div>
                <span className="eyebrow">QUICK START</span>
                <h2>三步，开始使用你的业务本体</h2>
              </div>
              <ol>
                <li>
                  <strong>组织模型</strong>在“分类与分组”维护目录，顶部选择当前分类后定义对象。
                </li>
                <li>
                  <strong>检查并生效</strong>保存草稿，预览结构后应用定义。
                </li>
                <li>
                  <strong>探索数据</strong>创建实例，选择字段与条件，显式运行查询。
                </li>
              </ol>
              <p className="muted">操作与函数目前支持定义维护；关系事实、多跳、分表和任务执行尚未开放。</p>
              <button className="button subtle" type="button" onClick={() => setHelp(false)}>
                收起指引
              </button>
            </div>
          )}
          {error && (
            <div className="notice notice-error workspace-error" role="alert">
              <span>{error}</span>
              <button type="button" className="button subtle" onClick={() => void refresh().catch(() => {})}>
                重新加载
              </button>
            </div>
          )}
          {!loaded ? (
            <div className="loading-state" role="status">
              <LoaderCircle className="spin" size={26} />
              <p>正在加载工作空间…</p>
            </div>
          ) : (
            <>
              <section hidden={view !== 'overview'}>
                <Overview
                  categories={categories}
                  categoryId={categoryId}
                  current={scopedCurrent}
                  entries={scopedEntries}
                  session={session}
                  onModels={() => navigate('models')}
                  onModel={(id) => navigateModel(`models/${encodeURIComponent(id)}`)}
                  onOrganizations={() => navigate('organizations')}
                  onQuery={query}
                />
              </section>
              <section hidden={view !== 'organizations'}>
                <DialogScope active={view === 'organizations'}>
                  <OrganizationsPage api={session.api} categories={categories} categoryId={categoryId} onSelectCategory={selectCategory} onRefresh={refresh} />
                </DialogScope>
              </section>
              <section hidden={view !== 'models'}>
                <DialogScope active={view === 'models'}>
                  <ModelsPage
                    api={session.api}
                    active={view === 'models'}
                    route={route}
                    onNavigate={navigateModel}
                    onNavigationGuard={registerNavigationGuard}
                    categories={categories}
                    categoryId={categoryId}
                    entries={entries}
                    draftDirectoryDenied={draftDirectoryDenied}
                    onRefresh={refresh}
                    onQuery={query}
                    onOrganizations={() => navigate('organizations', true)}
                    onSources={session.account.user.administrator ? () => navigate('sources', true) : undefined}
                  />
                </DialogScope>
              </section>
              <section hidden={view !== 'sources'}>
                <SourcesPage api={session.api} administrator={session.account.user.administrator} active={view === 'sources'} />
              </section>
              <section hidden={view !== 'query'}>
                <DialogScope active={view === 'query'}>
                  <QueryPage
                    api={session.api}
                    schemas={schemas}
                    categories={categories}
                    categoryId={categoryId}
                    initialSchemaId={initialSchemaId}
                    selectionRequest={selectionRequest}
                    onModel={() => navigate('models')}
                  />
                </DialogScope>
              </section>
              {view === 'admin' && (session.account.user.administrator
                ? <AdminPage api={session.api} session={session.account} categories={categories} definitions={current} onSessionChanged={() => { void session.api.getSession().then(onAccountUpdated).catch(() => {}) }} />
                : <div className="notice notice-error" role="alert">你没有用户与权限管理权限。</div>)}
            </>
          )}
        </main>
        <footer className="workspace-footer">
          <span>AgentForge Ontology</span>
          <span>让业务与数据，说同一种语言。</span>
        </footer>
      </div>
    </div>
  )
}

function ChangePassword({ api, onClose, onUpdated }: { api: ApiClient; onClose: () => void; onUpdated: (account: AccountSession) => void }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [saved, setSaved] = useState(false)
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if (password !== confirm) { setError('两次输入的新密码不一致'); return }
    setBusy(true); setError('')
    try { const account = await api.changePassword(currentPassword, password); onUpdated(account); setCurrentPassword(''); setPassword(''); setConfirm(''); setSaved(true) }
    catch (failure) { setError(failure instanceof Error ? failure.message : '密码修改失败') }
    finally { setBusy(false) }
  }
  return <Dialog title="修改密码" description="修改后，其他设备上的登录将失效。" onClose={onClose}>
    {saved ? <><div className="notice" role="status">密码已修改，请妥善保管新密码。</div><button className="button primary" onClick={onClose}>完成</button></>
      : <form onSubmit={submit}>
        {error && <div className="notice notice-error" role="alert">{error}</div>}
        <label className="field">当前密码<input type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} required /></label>
        <label className="field">新密码<input type="password" autoComplete="new-password" minLength={12} maxLength={72} value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
        <label className="field">确认新密码<input type="password" autoComplete="new-password" value={confirm} onChange={(event) => setConfirm(event.target.value)} required /></label>
        <button className="button primary" disabled={busy}>{busy ? '正在保存…' : '保存密码'}</button>
      </form>}
  </Dialog>
}

function Overview({
  categories,
  categoryId,
  current,
  entries,
  session,
  onModels,
  onModel,
  onOrganizations,
  onQuery,
}: {
  categories: Organization[]
  categoryId: string
  current: DefinitionRecord[]
  entries: DefinitionRecord[]
  session: Session
  onModels: () => void
  onModel: (id: string) => void
  onOrganizations: () => void
  onQuery: (id: string) => void
}) {
  const schemas = current.filter((entry) => entry.definition.kind === 'schema')
  const drafts = entries.filter((entry) => entry.status === 'draft')
  const categoryName = categories.find((category) => category.id === categoryId)?.name
  const stats = [
    { name: '已生效定义', value: current.length, hint: '当前可用的业务模型', icon: Layers3, color: 'green' },
    { name: '对象类型', value: schemas.length, hint: '连接模型与业务实例', icon: Box, color: 'blue' },
    {
      name: '编辑中草稿',
      value: drafts.length,
      hint: '保存意图，检查后生效',
      icon: FileEdit,
      color: 'amber',
    },
    {
      name: '当前分类定义',
      value: entries.length,
      hint: categoryName ?? '请先创建业务分类',
      icon: Workflow,
      color: 'purple',
    },
  ]
  return (
    <>
      <div className="page-heading">
        <div>
          <span className="eyebrow">YOUR ONTOLOGY, CONNECTED</span>
          <h1>工作空间概览</h1>
          <p>{categoryName ? `${categoryName} · 从清晰的业务定义，到可以探索的数据。` : '在“分类与分组”创建目录，开始组织业务模型。'}</p>
        </div>
        <button type="button" className="button primary" onClick={onModels}>
          <Layers3 size={16} /> 管理模型 <ArrowRight size={16} />
        </button>
      </div>
      <div className="overview-hero">
        <div className="overview-hero-copy">
          <span className="hero-tag">
            <span className="live-dot" /> 一个工作空间，统一业务语义
          </span>
          <h2>
            把业务知识，
            <br />
            变成可用的数据。
          </h2>
          <p>
            定义对象、连接关系、探索数据。
            <br />
            为你的业务建立可持续演进的语义基础。
          </p>
          <button type="button" className="hero-link" onClick={onModels}>
            探索模型目录 <ArrowRight size={17} />
          </button>
        </div>
        <OntologyIllustration />
      </div>
      <div className="stats-grid">
        {stats.map((stat) => (
          <div className="stat-card card" key={stat.name}>
            <div className="stat-top">
              <span>{stat.name}</span>
              <span className={`stat-icon ${stat.color}`}>
                <stat.icon size={19} strokeWidth={1.7} />
              </span>
            </div>
            <strong className="stat-number">{stat.value}</strong>
            <p>{stat.hint}</p>
          </div>
        ))}
      </div>
      <div className="overview-columns">
        <section className="card model-directory">
          <div className="card-heading">
            <div>
              <h2>
                模型目录 <span className="count-pill">{entries.length}</span>
              </h2>
              <p className="muted">你的业务世界，由这些定义组成</p>
            </div>
            <button type="button" className="text-button" onClick={onModels}>
              查看全部 <ArrowRight size={15} />
            </button>
          </div>
          {entries.length === 0 ? (
            <div className="empty-state">
              <span className="empty-icon">
                <Layers3 size={28} />
              </span>
              <h3>{categoryId ? '从第一个业务对象开始' : '先建立业务目录'}</h3>
              <p>{categoryId ? '在当前分类下选择分组并创建对象类型，为数据赋予含义。' : '在统一管理页创建分类与分组，再开始建模。'}</p>
              <button type="button" className="button primary" onClick={categoryId ? onModels : onOrganizations}>
                {categoryId ? '开始建模' : '管理分类与分组'} <ArrowRight size={15} />
              </button>
            </div>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>定义名称</th>
                    <th>业务分类</th>
                    <th>类型</th>
                    <th>状态</th>
                    <th>
                      <span className="sr-only">操作</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {entries.slice(0, 6).map((entry) => (
                    <tr key={entry.definition.id}>
                      <td>
                        <span className={`definition-glyph kind-${entry.definition.kind}`}>
                          <Box size={16} />
                        </span>
                        <button type="button" className="table-name" onClick={() => onModel(entry.definition.id)}>
                          {entry.definition.name}
                        </button>
                      </td>
                      <td className="muted">
                        {categories.find((category) => category.id === entry.definition.categoryId)?.name ??
                          '—'}
                      </td>
                      <td>
                        <span className="type-label">{kindNames[entry.definition.kind]}</span>
                      </td>
                      <td>
                        <span className={`badge ${entry.status === 'draft' ? 'badge-amber' : 'badge-green'}`}>
                          {entry.status === 'draft' ? '草稿' : '已生效'}
                        </span>
                      </td>
                      <td>
                        {entry.definition.kind === 'schema' &&
                          (entry.hasCurrent || entry.status === 'active') && (
                            <button
                              type="button"
                              className="icon-button"
                              aria-label={`查询${entry.definition.name}`}
                              onClick={() => onQuery(entry.definition.id)}
                            >
                              <ArrowRight size={16} />
                            </button>
                          )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="directory-footer">
            <span>
              <ShieldCheck size={14} /> 当前分类内的可访问定义
            </span>
            <span>稳定身份 · 单份当前态</span>
          </div>
        </section>
        <section className="card capability-card">
          <div className="capability-icon">
            <Database size={24} strokeWidth={1.5} />
          </div>
          <span className="eyebrow">CONNECTED FOUNDATION</span>
          <h2>准备好探索数据</h2>
          <p className="muted">模型生效后，即可通过中文字段构建查询，结果遵循当前访问权限。</p>
          <ul>
            <li>
              <Check size={15} />
              <span>{session.data.backend.toUpperCase()} 数据后端</span>
              <span className="tiny-badge">已连接</span>
            </li>
            <li>
              <Check size={15} />
              <span>单对象筛选与投影</span>
            </li>
            <li>
              <Check size={15} />
              <span>每页最多 {session.data.maxPageSize} 条</span>
            </li>
            <li>
              <ShieldCheck size={15} />
              <span>服务端行列授权</span>
            </li>
          </ul>
          <button
            type="button"
            className="button"
            onClick={() => (schemas.length ? onQuery(schemas[0].definition.id) : onModels())}
          >
            {schemas.length ? '打开数据探索' : '先创建对象类型'}
            <ArrowRight size={16} />
          </button>
        </section>
      </div>
    </>
  )
}
