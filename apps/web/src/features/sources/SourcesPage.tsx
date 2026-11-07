import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Check, Database, LoaderCircle, Plus, RefreshCw, Save, Unplug } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, DataSourceInput, SourceCompatibility, SourceTable, SourceTableInfo, SourceVersionOption } from '../../lib/types'
import './sources.css'

const newSource = (): DataSourceInput => ({ name: '', driver: 'mysql', host: '', port: 3306, database: '', username: '', password: '', tlsMode: 'require', enabled: true, tenantIsolation: 'column', versionPolicy: 'auto' })
const driverNames = { mysql: 'MySQL', postgresql: 'PostgreSQL' }
const statusNames = { supported: '兼容', unsupported: '暂不支持', mismatch: '版本不匹配', failed: '检查失败', pending: '待检测' }
const capabilityNames: Record<string, string> = { catalog: '目录读取', read: '实例读取', query: '数据查询', createTable: '建表', addColumn: '添加字段', createIndex: '创建索引' }

function CompatibilityDetails({ result, context }: { result?: SourceCompatibility; context: string }) {
  const checkedAt = result?.checkedAt ? new Date(result.checkedAt) : undefined
  const checkedLabel = checkedAt && Number.isFinite(checkedAt.getTime()) ? checkedAt.toLocaleString('zh-CN', { hour12: false }) : '尚未检查'
  return <section className="source-compatibility" aria-label="数据库版本检查">
    <div className="source-compatibility-heading"><h3>数据库版本检查</h3><span className={`badge ${result?.compatibilityStatus === 'supported' ? '' : 'muted'}`} aria-label="兼容状态">{result ? statusNames[result.compatibilityStatus] : '待检测'}</span></div>
    <p className="muted">{context}。检查结果仅适用于对应连接配置；具体操作仍需核验数据库账号权限和表结构。</p>
    <dl><div><dt>实际版本</dt><dd aria-label="实际数据库版本">{result?.serverVersion || '尚未识别'}</dd></div>
      <div><dt>连接状态</dt><dd>{result ? (result.connected ? '已连接' : '未连接') : '待检测'}</dd></div>
      <div><dt>最近检查</dt><dd>{checkedLabel}</dd></div>
      {result?.compatibilityStatus === 'supported' && <div className="source-compatibility-wide"><dt>版本适配范围</dt><dd>{result.capabilities.map((capability) => capabilityNames[capability]).filter(Boolean).join('、') || '未开放操作'}</dd></div>}
    </dl>
    {result?.message && <p className={result.compatibilityStatus === 'supported' ? 'muted' : 'source-compatibility-reason'}>{result.message}</p>}
  </section>
}

export function SourcesPage({ api, administrator, active = true }: { api: ApiClient; administrator: boolean; active?: boolean }) {
  const [sources, setSources] = useState<DataSource[]>([])
  const [versions, setVersions] = useState<SourceVersionOption[]>([])
  const [editing, setEditing] = useState<DataSource | 'new'>()
  const [form, setForm] = useState<DataSourceInput>(newSource)
  const [clearPassword, setClearPassword] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [revision, setRevision] = useState(0)
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [table, setTable] = useState<SourceTable>()
  const [compatibility, setCompatibility] = useState<SourceCompatibility>()
  const [checkContext, setCheckContext] = useState('已保存配置')
  const sequence = useRef(0)

  function report(cause: unknown) {
    setError(cause instanceof Error ? cause.message : '数据源操作失败。')
    if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
      setSources([]); setVersions([]); setEditing(undefined); setForm(newSource()); setTables([]); setTable(undefined); setCompatibility(undefined)
    }
  }
  useEffect(() => {
    const controller = new AbortController()
    if (!active || !administrator) {
      sequence.current++
      setForm((value) => ({ ...value, password: '' }))
      setCompatibility(undefined); setMessage('')
      return
    }
    setBusy('load'); setError('')
    Promise.all([api.listDataSources(controller.signal), api.listSourceVersions(controller.signal)]).then(([items, options]) => {
      if (!controller.signal.aborted) { setSources(items); setVersions(options) }
    }).catch((cause: unknown) => { if (!controller.signal.aborted) report(cause) })
      .finally(() => { if (!controller.signal.aborted) setBusy('') })
    return () => { controller.abort(); sequence.current++ }
  }, [api, active, administrator, revision])

  function edit(source: DataSource | 'new') {
    sequence.current++
    setEditing(source); setTables([]); setTable(undefined); setClearPassword(false); setError(''); setMessage('')
    setCompatibility(source === 'new' ? undefined : source.compatibility); setCheckContext(source === 'new' ? '当前编辑配置' : '已保存配置')
    setForm(source === 'new' ? newSource() : {
      name: source.name, driver: source.driver, host: source.host ?? '', port: source.port ?? (source.driver === 'mysql' ? 3306 : 5432),
      database: source.database, username: source.username ?? '', password: '', tlsMode: source.tlsMode ?? 'require', enabled: source.enabled, tenantIsolation: source.tenantIsolation,
      versionPolicy: source.versionPolicy ?? 'auto', expectedVersionLine: source.expectedVersionLine,
    })
  }
  function changeConnection(patch: Partial<DataSourceInput>) {
    setForm((value) => ({ ...value, ...patch })); setCompatibility(undefined); setCheckContext('当前编辑配置'); setMessage(''); setError(''); setTables([]); setTable(undefined)
  }
  function input(): DataSourceInput {
    return { ...form, password: clearPassword ? '' : form.password || (editing === 'new' ? '' : undefined) }
  }
  async function save(event: FormEvent) {
    event.preventDefault()
    if (!editing || busy) return
    const request = ++sequence.current
    setBusy('save'); setError(''); setMessage('')
    try {
      const source = editing === 'new' ? await api.createDataSource(input()) : await api.updateDataSource(editing.id, input(), editing.etag)
      if (request !== sequence.current) return
      setSources((items) => [...items.filter((item) => item.id !== source.id), source].sort((a, b) => a.name.localeCompare(b.name, 'zh-CN')))
      setEditing(source); setForm((value) => ({ ...value, password: '' })); setClearPassword(false)
      setCompatibility(source.compatibility); setCheckContext('已保存配置')
      setMessage('数据源已保存。可测试连接并查看表结构。')
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function testConnection() {
    if (!editing || busy) return
    const request = ++sequence.current
    setBusy('test'); setError(''); setMessage('')
    setCompatibility(undefined)
    try {
      const result = editing === 'new' ? await api.testDataSourceInput(input()) : await api.testDataSource(editing.id, input(), editing.etag)
      if (request === sequence.current) {
        setCompatibility(result.compatibility); setCheckContext('当前编辑配置；测试不会保存表单')
        if (result.success && result.compatibility.compatibilityStatus === 'supported') setMessage(`当前配置检查通过 · ${driverNames[form.driver]} ${result.serverVersion}`)
        else setError(result.compatibility.message || statusNames[result.compatibility.compatibilityStatus])
      }
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function discover() {
    if (!editing || editing === 'new' || busy) return
    const request = ++sequence.current
    setBusy('tables'); setError(''); setTable(undefined)
    try { const items = await api.listSourceTables(editing.id); if (request === sequence.current) setTables(items) }
    catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function inspect(item: SourceTableInfo) {
    if (!editing || editing === 'new' || busy) return
    const request = ++sequence.current
    setBusy('inspect'); setError(''); setTable(undefined)
    try { const result = await api.inspectSourceTable(editing.id, item.name, item.namespace); if (request === sequence.current) setTable(result) }
    catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  return <div className="sources-page">
    <div className="sources-heading"><div><h1>数据源配置</h1><p className="muted">管理数据库连接，供对象模型选择主表及属性来源。</p></div>{administrator && <div className="sources-actions"><button type="button" className="button subtle" disabled={!!busy} aria-label="刷新数据源" onClick={() => setRevision((value) => value + 1)}><RefreshCw size={16} /></button><button type="button" className="button primary" disabled={!!busy} onClick={() => edit('new')}><Plus size={16} />添加数据源</button></div>}</div>
    {!administrator ? <div className="card empty-state"><Database size={30} /><h2>数据源由租户管理员配置</h2><p className="muted">模型编辑可选择已获授权的数据源与表列。</p></div> : <>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      <div className="sources-layout">
        <section className="card sources-directory" aria-label="已配置的数据源"><h2>已配置的数据源 <span className="muted">{sources.length}</span></h2>
          {!sources.length ? <p className="muted">{busy === 'load' ? '正在读取…' : '尚未配置数据源。'}</p> : <ul>{sources.map((source) => <li key={source.id}><button type="button" disabled={!!busy} className={editing !== 'new' && editing?.id === source.id ? 'is-active' : ''} onClick={() => edit(source)}><Database size={17} /><span><strong>{source.name}</strong><small>{driverNames[source.driver]} · {source.database}</small><small>{source.compatibility?.serverVersion || '版本待识别'} · {source.compatibility ? statusNames[source.compatibility.compatibilityStatus] : '待检测'}</small></span><span className={`badge ${source.enabled ? '' : 'muted'}`}>{source.enabled ? '启用' : '停用'}</span></button></li>)}</ul>}
        </section>
        {editing ? <section className="card sources-editor" aria-label="数据源连接配置"><h2>{editing === 'new' ? '添加数据源' : editing.name}</h2><form onSubmit={(event) => void save(event)}>
          <fieldset disabled={!!busy} className="sources-fields"><label>数据源名称<input className="field" aria-label="数据源名称" required maxLength={128} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></label>
            <label>数据库类型<select className="field" aria-label="数据库类型" value={form.driver} onChange={(event) => { const driver = event.target.value as DataSourceInput['driver']; changeConnection({ driver, port: driver === 'mysql' ? 3306 : 5432, versionPolicy: 'auto', expectedVersionLine: undefined }) }}><option value="mysql">MySQL</option><option value="postgresql">PostgreSQL</option></select></label>
            <label>数据库版本<select className="field" aria-label="数据库版本" value={form.versionPolicy === 'pinned' ? form.expectedVersionLine ?? '' : 'auto'} onChange={(event) => changeConnection({ versionPolicy: event.target.value === 'auto' ? 'auto' : 'pinned', expectedVersionLine: event.target.value === 'auto' ? undefined : event.target.value })}><option value="auto">自动识别</option>{form.versionPolicy === 'pinned' && !versions.some((option) => option.driver === form.driver && option.versionLine === form.expectedVersionLine) && <option value={form.expectedVersionLine ?? ''} disabled>{form.expectedVersionLine || '未指定版本'}（未登记）</option>}{versions.filter((option) => option.driver === form.driver).map((option) => <option key={option.versionLine} value={option.versionLine} disabled={option.status !== 'supported'}>{option.label}{option.status === 'pending' ? '（待适配）' : ''}</option>)}</select><small className="muted">指定版本会核对真实服务器；自动识别也仅开放已适配范围。</small></label>
            <label>主机<input className="field" aria-label="数据库主机" required value={form.host} onChange={(event) => changeConnection({ host: event.target.value })} placeholder="数据库服务器地址" /></label>
            <label>端口<input className="field" type="number" aria-label="数据库端口" required min={1} max={65535} value={form.port} onChange={(event) => changeConnection({ port: Number(event.target.value) })} /></label>
            <label>数据库<input className="field" aria-label="数据库名称" required value={form.database} onChange={(event) => changeConnection({ database: event.target.value })} /></label>
            <label>用户名<input className="field" aria-label="数据库用户名" autoComplete="off" required value={form.username} onChange={(event) => changeConnection({ username: event.target.value })} /></label>
            <label>密码<input className="field" aria-label="数据库密码" type="password" autoComplete="new-password" value={form.password ?? ''} disabled={!!busy || clearPassword} placeholder={editing !== 'new' && editing.passwordSet ? '已保存；留空保留原密码' : '连接密码'} onChange={(event) => changeConnection({ password: event.target.value })} /></label>
            <label>TLS<select className="field" aria-label="连接加密" value={form.tlsMode} onChange={(event) => changeConnection({ tlsMode: event.target.value as DataSourceInput['tlsMode'] })}><option value="require">加密连接</option><option value="verify-full">加密并校验证书与主机</option><option value="disable">不使用 TLS（本地开发）</option></select></label>
            <label className="sources-wide">租户数据隔离<select className="field" aria-label="租户数据隔离" value={form.tenantIsolation} onChange={(event) => changeConnection({ tenantIsolation: event.target.value as DataSourceInput['tenantIsolation'] })}><option value="column">共享数据库，模型指定租户列</option><option value="dedicated">此数据库专属于当前租户</option></select><small className="muted">{form.tenantIsolation === 'dedicated' ? '确认该数据库中的业务表仅存放当前租户数据。' : '主表和每个属性来源表都必须配置租户列，查询始终带当前租户条件。'}</small></label>
            <label className="sources-checkbox"><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />启用数据源</label>
            {editing !== 'new' && editing.passwordSet && <label className="sources-checkbox"><input type="checkbox" checked={clearPassword} onChange={(event) => { setClearPassword(event.target.checked); changeConnection({}) }} />清空已保存密码</label>}
          </fieldset>
          <CompatibilityDetails result={compatibility} context={checkContext} />
          <div className="sources-editor-actions"><button className="button primary" disabled={!!busy}><Save size={16} />{busy === 'save' ? '正在保存…' : '保存数据源'}</button><button type="button" className="button" disabled={!!busy || !form.host || !form.database || !form.username} onClick={() => void testConnection()}>{busy === 'test' ? <LoaderCircle size={16} className="spin" /> : <Unplug size={16} />}测试当前配置</button>{editing !== 'new' && <button type="button" className="button subtle" disabled={!!busy || !editing.enabled} onClick={() => void discover()}>查看数据库表</button>}</div>
        </form>
          {tables.length > 0 && <section className="sources-tables" aria-label="数据源表目录"><h3>数据库表</h3><div className="sources-table-buttons">{tables.map((item) => <button type="button" className="button subtle" disabled={!!busy} key={`${item.namespace}:${item.name}`} onClick={() => void inspect(item)}>{item.namespace}.{item.name}</button>)}</div></section>}
          {table && <section className="sources-tables" aria-label="数据源表结构"><h3>{table.namespace}.{table.name}</h3><div className="table-wrap"><table><thead><tr><th>列</th><th>数据库类型</th><th>模型类型</th><th>约束</th></tr></thead><tbody>{table.columns.map((column) => <tr key={column.name}><td>{column.name}</td><td>{column.databaseType}</td><td>{column.dataType || '暂不支持'}</td><td>{[column.primaryKey ? '主键' : '', column.nullable ? '可空' : '非空', column.generated ? '生成列' : ''].filter(Boolean).join(' · ')}</td></tr>)}</tbody></table></div></section>}
        </section> : <section className="card empty-state"><Database size={32} /><h2>选择或添加数据源</h2><p className="muted">支持 MySQL 与 PostgreSQL 的连接配置及表结构发现。</p></section>}
      </div>
    </>}
  </div>
}
