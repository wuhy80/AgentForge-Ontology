import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import {
  ArrowRight,
  BookOpen,
  Box,
  Braces,
  Check,
  ChevronRight,
  CircleHelp,
  Database,
  FileEdit,
  GitBranch,
  Layers3,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Search,
  ShieldCheck,
  Sparkles,
  Workflow,
} from 'lucide-react'
import { ApiClient, ApiError } from './lib/api'
import type { DataCapabilities, DefinitionRecord, MetadataCapabilities, Organization } from './lib/types'
import { ModelsPage } from './features/model/ModelsPage'
import { QueryPage } from './features/query/QueryPage'
import { DialogScope } from './components/Dialog'

type View = 'overview' | 'models' | 'query'
type Identity = { tenantId: string; subject: string; expiresAt: number }
type Session = { api: ApiClient; identity: Identity; metadata: MetadataCapabilities; data: DataCapabilities }
const kindNames = { schema: '对象类型', enum: '枚举', link: '关系', action: '操作', function: '函数' }
const nav = [
  { id: 'overview' as const, name: '工作空间', subtitle: '概览', icon: LayoutDashboard },
  { id: 'models' as const, name: '模型管理', subtitle: '定义业务世界', icon: Layers3 },
  { id: 'query' as const, name: '数据探索', subtitle: '让数据回答问题', icon: Search },
]

function currentView(): View {
  const hash = window.location.hash.slice(1)
  return hash === 'models' || hash === 'query' ? hash : 'overview'
}

// The claims are only displayed after both services accept the signed identity.
// They never select a tenant header or substitute for backend authentication.
function identityLabel(token: string): Identity {
  try {
    const part = token.split('.')[0].replace(/-/g, '+').replace(/_/g, '/')
    const bytes = Uint8Array.from(atob(part), (char) => char.charCodeAt(0))
    const value: unknown = JSON.parse(new TextDecoder().decode(bytes))
    if (
      typeof value === 'object' &&
      value !== null &&
      'tenantId' in value &&
      'subject' in value &&
      'expiresAt' in value &&
      typeof value.tenantId === 'string' &&
      typeof value.subject === 'string' &&
      typeof value.expiresAt === 'number'
    ) {
      return { tenantId: value.tenantId, subject: value.subject, expiresAt: value.expiresAt }
    }
  } catch {
    /* A malformed bootstrap credential is rejected before connecting. */
  }
  throw new Error('请输入当前服务签发的有效访问凭证')
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

function Connect({ onConnect, reason }: { onConnect: (token: string) => Promise<void>; reason: string }) {
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await onConnect(token.trim())
      setToken('')
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
          <h2>连接你的工作空间</h2>
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
            <label className="field">
              访问凭证
              <input
                type="password"
                name="credential"
                autoComplete="off"
                spellCheck={false}
                placeholder="粘贴你的访问凭证"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                required
                disabled={busy}
              />
            </label>
            <p className="field-hint">
              <ShieldCheck size={15} /> 凭证仅在当前页面会话中使用，退出后即清除。
            </p>
            <button className="button primary connect-submit" disabled={busy || !token.trim()}>
              {busy ? <LoaderCircle className="spin" size={18} /> : null}
              {busy ? '正在连接…' : '进入工作空间'}
              {!busy && <ArrowRight size={18} />}
            </button>
          </form>
          <details className="connect-help">
            <summary>
              <CircleHelp size={16} /> 如何获取本地开发凭证
            </summary>
            <p>先启动本地后端，在仓库根目录运行以下命令，将输出粘贴到上方。</p>
            <code>powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/dev-token.ps1</code>
            <p>刷新页面后需要重新连接。该凭证用于当前本地开发环境。</p>
          </details>
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
  const connect = useCallback(async (token: string) => {
    const request = ++connecting.current
    const identity = identityLabel(token)
    const api: ApiClient = new ApiClient(token, () => {
      if (activeClient.current !== api) return
      activeClient.current = null
      setSession(null)
      setReason('访问凭证已失效，请重新连接。')
    })
    const [metadata, data] = await Promise.all([api.metadataCapabilities(), api.dataCapabilities()])
    if (request !== connecting.current) return
    activeClient.current = api
    setReason('')
    setSession({ api, identity, metadata, data })
  }, [])
  useEffect(() => {
    if (!session) return
    const expires = session.identity.expiresAt * 1000 - Date.now()
    const timer = window.setTimeout(
      () => {
        activeClient.current = null
        setReason('访问凭证已过期，请重新连接。')
        setSession(null)
      },
      Math.max(0, Math.min(expires, 2147483647)),
    )
    return () => window.clearTimeout(timer)
  }, [session])
  if (!session) return <Connect onConnect={connect} reason={reason} />
  return (
    <Workspace
      key={`${session.identity.tenantId}:${session.identity.subject}`}
      session={session}
      onDisconnect={() => {
        ++connecting.current
        activeClient.current = null
        setReason('')
        setSession(null)
      }}
    />
  )
}

function Workspace({ session, onDisconnect }: { session: Session; onDisconnect: () => void }) {
  const [view, setView] = useState<View>(currentView)
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
      const [nextCategories, nextCurrent, draftDirectory] = await Promise.all([
        session.api.listCategories(active.signal),
        session.api.listDefinitions({ view: 'current' }, active.signal),
        session.api
          .listDefinitions({ view: 'draft' }, active.signal)
          .then((records) => ({ records, denied: false }))
          .catch((failure) => {
            if (failure instanceof ApiError && failure.status === 403) return { records: [], denied: true }
            throw failure
          }),
      ])
      if (sequence !== requests.current || active.signal.aborted) return
      const merged = new Map(
        nextCurrent.map((record) => [record.definition.id, { ...record, hasCurrent: true }]),
      )
      for (const record of draftDirectory.records)
        merged.set(record.definition.id, { ...record, hasCurrent: merged.has(record.definition.id) })
      setDraftDirectoryDenied(draftDirectory.denied)
      setCategories(nextCategories)
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
    } finally {
      if (sequence === requests.current && !active.signal.aborted) {
        setLoading(false)
        setLoaded(true)
      }
    }
  }, [session.api])
  useEffect(() => {
    void refresh()
    return () => {
      ++requests.current
      controller.current?.abort()
    }
  }, [refresh])
  useEffect(() => {
    const update = () => setView(currentView())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])
  const navigate = (next: View) => {
    setView(next)
    window.location.hash = next
  }
  const query = (schemaId: string) => {
    setInitialSchemaId(schemaId)
    setSelectionRequest((value) => value + 1)
    navigate('query')
  }
  const schemas = current.filter((record) => record.definition.kind === 'schema')
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
          {nav.map((item) => (
            <a
              href={`#${item.id}`}
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
            <span className="connection-status">
              <span className="live-dot" /> 已连接
            </span>
            <span className="topbar-separator" />
            <span className="user-avatar">{session.identity.subject.slice(0, 1).toUpperCase()}</span>
            <span className="subject-name">{session.identity.subject}</span>
            <button
              type="button"
              className="icon-button"
              aria-label="退出工作空间"
              title="退出工作空间"
              onClick={onDisconnect}
            >
              <LogOut size={17} />
            </button>
          </div>
        </header>
        <main id="main-content" className="main-content" tabIndex={-1}>
          {help && (
            <div className="help-panel card">
              <div>
                <span className="eyebrow">QUICK START</span>
                <h2>三步，开始使用你的业务本体</h2>
              </div>
              <ol>
                <li>
                  <strong>组织模型</strong>创建分类与分组，再定义对象的属性。
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
              <button type="button" className="button subtle" onClick={() => void refresh()}>
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
                  current={current}
                  entries={entries}
                  session={session}
                  onModels={() => navigate('models')}
                  onQuery={query}
                />
              </section>
              <section hidden={view !== 'models'}>
                <DialogScope active={view === 'models'}>
                  <ModelsPage
                    api={session.api}
                    categories={categories}
                    entries={entries}
                    draftDirectoryDenied={draftDirectoryDenied}
                    onRefresh={refresh}
                    onQuery={query}
                  />
                </DialogScope>
              </section>
              <section hidden={view !== 'query'}>
                <DialogScope active={view === 'query'}>
                  <QueryPage
                    api={session.api}
                    schemas={schemas}
                    categories={categories}
                    initialSchemaId={initialSchemaId}
                    selectionRequest={selectionRequest}
                    onModel={() => navigate('models')}
                  />
                </DialogScope>
              </section>
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

function Overview({
  categories,
  current,
  entries,
  session,
  onModels,
  onQuery,
}: {
  categories: Organization[]
  current: DefinitionRecord[]
  entries: DefinitionRecord[]
  session: Session
  onModels: () => void
  onQuery: (id: string) => void
}) {
  const schemas = current.filter((entry) => entry.definition.kind === 'schema')
  const drafts = entries.filter((entry) => entry.status === 'draft')
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
      name: '业务分类',
      value: categories.length,
      hint: '按业务领域组织模型',
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
          <p>从清晰的业务定义，到可以探索的数据。</p>
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
              <h3>从第一个业务对象开始</h3>
              <p>创建分类、分组和对象类型，为数据赋予含义。</p>
              <button type="button" className="button primary" onClick={onModels}>
                开始建模 <ArrowRight size={15} />
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
                        <button type="button" className="table-name" onClick={onModels}>
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
              <ShieldCheck size={14} /> 当前租户内的可访问定义
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
