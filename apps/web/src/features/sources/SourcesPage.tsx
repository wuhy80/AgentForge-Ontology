import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Check, Database, LoaderCircle, Plus, RefreshCw, Save, Unplug } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, DataSourceInput, SourceTable, SourceTableInfo } from '../../lib/types'
import './sources.css'

const newSource = (): DataSourceInput => ({ name: '', driver: 'mysql', host: '', port: 3306, database: '', username: '', password: '', tlsMode: 'require', enabled: true, tenantIsolation: 'column' })
const driverNames = { mysql: 'MySQL', postgresql: 'PostgreSQL' }

export function SourcesPage({ api, administrator, active = true }: { api: ApiClient; administrator: boolean; active?: boolean }) {
  const [sources, setSources] = useState<DataSource[]>([])
  const [editing, setEditing] = useState<DataSource | 'new'>()
  const [form, setForm] = useState<DataSourceInput>(newSource)
  const [clearPassword, setClearPassword] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [revision, setRevision] = useState(0)
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [table, setTable] = useState<SourceTable>()
  const sequence = useRef(0)

  function report(cause: unknown) {
    setError(cause instanceof Error ? cause.message : '数据源操作失败。')
    if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
      setSources([]); setEditing(undefined); setForm(newSource()); setTables([]); setTable(undefined)
    }
  }
  useEffect(() => {
    const controller = new AbortController()
    if (!active || !administrator) {
      sequence.current++
      setForm((value) => ({ ...value, password: '' }))
      return
    }
    setBusy('load'); setError('')
    api.listDataSources(controller.signal).then((items) => {
      if (!controller.signal.aborted) setSources(items)
    }).catch((cause: unknown) => { if (!controller.signal.aborted) report(cause) })
      .finally(() => { if (!controller.signal.aborted) setBusy('') })
    return () => { controller.abort(); sequence.current++ }
  }, [api, active, administrator, revision])

  function edit(source: DataSource | 'new') {
    sequence.current++
    setEditing(source); setTables([]); setTable(undefined); setClearPassword(false); setError(''); setMessage('')
    setForm(source === 'new' ? newSource() : {
      name: source.name, driver: source.driver, host: source.host ?? '', port: source.port ?? (source.driver === 'mysql' ? 3306 : 5432),
      database: source.database, username: source.username ?? '', password: '', tlsMode: source.tlsMode ?? 'require', enabled: source.enabled, tenantIsolation: source.tenantIsolation,
    })
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
      setMessage('数据源已保存。可测试连接并查看表结构。')
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function testConnection() {
    if (!editing || busy) return
    const request = ++sequence.current
    setBusy('test'); setError(''); setMessage('')
    try {
      // Existing passwords remain on the server. Test the saved record after an
      // edit; a new record can be checked before it is persisted.
      const result = editing === 'new' ? await api.testDataSourceInput(input()) : await api.testDataSource(editing.id)
      if (request === sequence.current) setMessage(`连接成功 · ${driverNames[form.driver]} ${result.serverVersion}${editing === 'new' ? '' : '（已保存配置）'}`)
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
          {!sources.length ? <p className="muted">{busy === 'load' ? '正在读取…' : '尚未配置数据源。'}</p> : <ul>{sources.map((source) => <li key={source.id}><button type="button" disabled={!!busy} className={editing !== 'new' && editing?.id === source.id ? 'is-active' : ''} onClick={() => edit(source)}><Database size={17} /><span><strong>{source.name}</strong><small>{driverNames[source.driver]} · {source.database}</small></span><span className={`badge ${source.enabled ? '' : 'muted'}`}>{source.enabled ? '启用' : '停用'}</span></button></li>)}</ul>}
        </section>
        {editing ? <section className="card sources-editor" aria-label="数据源连接配置"><h2>{editing === 'new' ? '添加数据源' : editing.name}</h2><form onSubmit={(event) => void save(event)}>
          <fieldset disabled={!!busy} className="sources-fields"><label>数据源名称<input className="field" aria-label="数据源名称" required maxLength={128} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></label>
            <label>数据库类型<select className="field" aria-label="数据库类型" value={form.driver} onChange={(event) => { const driver = event.target.value as DataSourceInput['driver']; setForm({ ...form, driver, port: driver === 'mysql' ? 3306 : 5432 }) }}><option value="mysql">MySQL</option><option value="postgresql">PostgreSQL</option></select></label>
            <label>主机<input className="field" aria-label="数据库主机" required value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} placeholder="数据库服务器地址" /></label>
            <label>端口<input className="field" type="number" aria-label="数据库端口" required min={1} max={65535} value={form.port} onChange={(event) => setForm({ ...form, port: Number(event.target.value) })} /></label>
            <label>数据库<input className="field" aria-label="数据库名称" required value={form.database} onChange={(event) => setForm({ ...form, database: event.target.value })} /></label>
            <label>用户名<input className="field" aria-label="数据库用户名" autoComplete="off" required value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} /></label>
            <label>密码<input className="field" aria-label="数据库密码" type="password" autoComplete="new-password" value={form.password ?? ''} disabled={!!busy || clearPassword} placeholder={editing !== 'new' && editing.passwordSet ? '已保存；留空保留原密码' : '连接密码'} onChange={(event) => setForm({ ...form, password: event.target.value })} /></label>
            <label>TLS<select className="field" aria-label="连接加密" value={form.tlsMode} onChange={(event) => setForm({ ...form, tlsMode: event.target.value as DataSourceInput['tlsMode'] })}><option value="require">加密连接</option><option value="verify-full">加密并校验证书与主机</option><option value="disable">不使用 TLS（本地开发）</option></select></label>
            <label className="sources-wide">租户数据隔离<select className="field" aria-label="租户数据隔离" value={form.tenantIsolation} onChange={(event) => setForm({ ...form, tenantIsolation: event.target.value as DataSourceInput['tenantIsolation'] })}><option value="column">共享数据库，模型指定租户列</option><option value="dedicated">此数据库专属于当前租户</option></select><small className="muted">{form.tenantIsolation === 'dedicated' ? '确认该数据库中的业务表仅存放当前租户数据。' : '主表和每个属性来源表都必须配置租户列，查询始终带当前租户条件。'}</small></label>
            <label className="sources-checkbox"><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />启用数据源</label>
            {editing !== 'new' && editing.passwordSet && <label className="sources-checkbox"><input type="checkbox" checked={clearPassword} onChange={(event) => setClearPassword(event.target.checked)} />清空已保存密码</label>}
          </fieldset>
          <div className="sources-editor-actions"><button className="button primary" disabled={!!busy}><Save size={16} />{busy === 'save' ? '正在保存…' : '保存数据源'}</button><button type="button" className="button" disabled={!!busy || !form.host || !form.database || !form.username} onClick={() => void testConnection()}>{busy === 'test' ? <LoaderCircle size={16} className="spin" /> : <Unplug size={16} />}测试{editing === 'new' ? '连接' : '已保存连接'}</button>{editing !== 'new' && <button type="button" className="button subtle" disabled={!!busy || !editing.enabled} onClick={() => void discover()}>查看数据库表</button>}</div>
        </form>
          {tables.length > 0 && <section className="sources-tables" aria-label="数据源表目录"><h3>数据库表</h3><div className="sources-table-buttons">{tables.map((item) => <button type="button" className="button subtle" disabled={!!busy} key={`${item.namespace}:${item.name}`} onClick={() => void inspect(item)}>{item.namespace}.{item.name}</button>)}</div></section>}
          {table && <section className="sources-tables" aria-label="数据源表结构"><h3>{table.namespace}.{table.name}</h3><div className="table-wrap"><table><thead><tr><th>列</th><th>数据库类型</th><th>模型类型</th><th>约束</th></tr></thead><tbody>{table.columns.map((column) => <tr key={column.name}><td>{column.name}</td><td>{column.databaseType}</td><td>{column.dataType || '暂不支持'}</td><td>{[column.primaryKey ? '主键' : '', column.nullable ? '可空' : '非空', column.generated ? '生成列' : ''].filter(Boolean).join(' · ')}</td></tr>)}</tbody></table></div></section>}
        </section> : <section className="card empty-state"><Database size={32} /><h2>选择或添加数据源</h2><p className="muted">支持 MySQL 与 PostgreSQL 的连接配置及表结构发现。</p></section>}
      </div>
    </>}
  </div>
}
