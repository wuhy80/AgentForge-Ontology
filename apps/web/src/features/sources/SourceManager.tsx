import { useEffect, useRef, useState } from 'react'
import { ArrowLeft, Database, LoaderCircle, Play, RefreshCw, Table2 } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceQueryResult, SourceTable, SourceTableInfo } from '../../lib/types'

export function sourcePreviewSQL(driver: DataSource['driver'], table: SourceTableInfo): string {
  const quote = (name: string) => driver === 'mysql' ? `\`${name.replaceAll('`', '``')}\`` : `"${name.replaceAll('"', '""')}"`
  return `SELECT * FROM ${quote(table.namespace)}.${quote(table.name)} LIMIT 100;`
}

export function SourceManager({ api, source, active, onBack, onDenied }: { api: ApiClient; source: DataSource; active: boolean; onBack: () => void; onDenied: (cause: unknown) => void }) {
  const [current, setCurrent] = useState(source)
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [table, setTable] = useState<SourceTable>()
  const [namespace, setNamespace] = useState(source.driver === 'mysql' ? source.database : 'public')
  const [search, setSearch] = useState('')
  const [sql, setSQL] = useState('')
  const [result, setResult] = useState<SourceQueryResult>()
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const sequence = useRef(0)
  const pending = useRef<AbortController | undefined>(undefined)
  const allowed = current.enabled && current.tenantIsolation === 'dedicated'

  function report(cause: unknown) {
    setError(cause instanceof Error ? cause.message : '读取数据源失败。')
    if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
      setTables([]); setTable(undefined); setSQL(''); setResult(undefined); onDenied(cause)
    }
  }
  useEffect(() => {
    const controller = new AbortController()
    pending.current = controller
    const request = ++sequence.current
    setResult(undefined); setTable(undefined); setTables([]); setSQL(''); setBusy(''); setError('')
    if (!active) return
    setBusy('load')
    api.getDataSource(source.id, controller.signal).then(async (saved) => {
      if (controller.signal.aborted || request !== sequence.current) return
      setCurrent(saved)
      const defaultNamespace = saved.driver === 'mysql' ? saved.database : 'public'
      setNamespace(defaultNamespace); setSearch('')
      if (!saved.enabled) { setError('数据源已停用，请返回列表编辑并启用。'); return }
      const items = await api.listSourceTables(saved.id, defaultNamespace, controller.signal)
      if (!controller.signal.aborted && request === sequence.current) setTables(items)
    }).catch((cause: unknown) => { if (!controller.signal.aborted && request === sequence.current) report(cause) })
      .finally(() => { if (!controller.signal.aborted && request === sequence.current) setBusy('') })
    return () => { controller.abort(); pending.current?.abort(); sequence.current++ }
  }, [api, source.id, active])

  async function loadTables() {
    if (busy) return
    const request = ++sequence.current
    const controller = new AbortController(); pending.current = controller
    setBusy('tables'); setError(''); setTable(undefined); setTables([]); setResult(undefined)
    try {
      const items = await api.listSourceTables(current.id, namespace, controller.signal)
      if (request === sequence.current) setTables(items)
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function inspect(item: SourceTableInfo) {
    if (busy) return
    const request = ++sequence.current
    const controller = new AbortController(); pending.current = controller
    setBusy('inspect'); setError(''); setTable(undefined); setResult(undefined)
    try {
      const value = await api.inspectSourceTable(current.id, item.name, item.namespace, controller.signal)
      if (request === sequence.current) { setTable(value); setSQL(sourcePreviewSQL(current.driver, item)) }
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  async function execute() {
    if (busy || !allowed || !sql.trim()) return
    const request = ++sequence.current
    const controller = new AbortController(); pending.current = controller
    setBusy('query'); setError(''); setResult(undefined)
    try {
      const value = await api.queryDataSource(current.id, sql, current.etag, controller.signal)
      if (request === sequence.current) setResult(value)
    } catch (cause) { if (request === sequence.current) report(cause) }
    finally { if (request === sequence.current) setBusy('') }
  }
  return <div className="sources-page source-manager">
    <button type="button" className="button subtle source-back" onClick={onBack}><ArrowLeft size={16} />返回数据源列表</button>
    <div className="sources-heading"><div><h1>{current.name}</h1><p className="muted">数据库管理 · 浏览表结构与索引，执行 SQL 查看数据。</p></div><span className="badge">{current.driver === 'mysql' ? 'MySQL' : 'PostgreSQL'}</span></div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    <section className="card source-database-info" aria-label="数据库基础信息"><h2><Database size={17} />数据库基础信息</h2><dl>
      {[['数据库', current.database], ['主机 / 端口', `${current.host || '—'}:${current.port || (current.driver === 'mysql' ? 3306 : 5432)}`], ['数据库版本', current.compatibility?.serverVersion || '待检测'], ['连接账号', current.username || '—'], ['连接加密', current.tlsMode === 'disable' ? '未启用 TLS' : current.tlsMode === 'verify-full' ? 'TLS 与证书校验' : 'TLS'], ['数据隔离', current.tenantIsolation === 'dedicated' ? '当前租户专属数据库' : '共享数据库，按模型租户列隔离']].map(([name, value]) => <div key={name}><dt>{name}</dt><dd>{value}</dd></div>)}
    </dl></section>
    <div className="source-management-layout">
      <section className="card source-table-catalog" aria-label="数据库表目录"><h2><Table2 size={17} />数据库表 <span className="muted">{tables.length}</span></h2>
        <form className="source-namespace" onSubmit={(event) => { event.preventDefault(); void loadTables() }}><label>命名空间<input className="field" aria-label="目录命名空间" disabled={!!busy || current.driver === 'mysql'} value={namespace} onChange={(event) => setNamespace(event.target.value)} /></label><button type="submit" className="button subtle" disabled={!!busy || !namespace.trim()} aria-label="刷新数据库表"><RefreshCw size={16} /></button></form>
        <input className="field" aria-label="搜索数据库表" placeholder="搜索表名…" value={search} onChange={(event) => setSearch(event.target.value)} />
        <div className="source-table-list">{tables.filter((item) => item.name.toLocaleLowerCase().includes(search.toLocaleLowerCase())).map((item) => <button type="button" className={table?.name === item.name && table.namespace === item.namespace ? 'is-active' : ''} disabled={!!busy} key={`${item.namespace}:${item.name}`} onClick={() => void inspect(item)}><Table2 size={15} /><span>{item.namespace}.{item.name}</span></button>)}</div>
        {!tables.length && <p className="muted">{busy === 'load' || busy === 'tables' ? '正在读取数据库表…' : '当前命名空间没有可查看的表。'}</p>}
      </section>
      <div className="source-management-content">
        <section className="card source-table-details" aria-label="数据源表结构"><h2>{table ? `${table.namespace}.${table.name}` : '表结构与索引'}</h2>
          {busy === 'inspect' ? <p className="muted">正在读取表结构…</p> : !table ? <p className="muted">从左侧选择数据库表，查看字段和索引并生成预览 SQL。</p> : <>
            <h3>字段 <span className="muted">{table.columns.length}</span></h3><div className="table-wrap"><table><thead><tr><th>字段</th><th>数据库类型</th><th>约束</th></tr></thead><tbody>{table.columns.map((column) => <tr key={column.name}><td>{column.name}</td><td>{column.databaseType}</td><td>{[column.primaryKey ? '主键' : '', column.nullable ? '可空' : '非空', column.generated ? '生成列' : ''].filter(Boolean).join(' · ')}</td></tr>)}</tbody></table></div>
            <h3>索引 <span className="muted">{table.indexes.length}</span></h3>{table.indexes.length ? <div className="table-wrap"><table><thead><tr><th>索引名称</th><th>类型</th><th>字段</th><th>条件</th></tr></thead><tbody>{table.indexes.map((index) => <tr key={index.name}><td>{index.name}</td><td>{index.primary ? '主键' : index.unique ? '唯一索引' : '普通索引'}</td><td>{index.columns.join(', ')}</td><td>{index.predicate || '—'}</td></tr>)}</tbody></table></div> : <p className="muted">未发现索引。</p>}
          </>}
        </section>
        <section className="card source-sql" aria-label="SQL 查询"><div className="sources-panel-heading"><h2>SQL 查询</h2><button type="button" className="button primary" disabled={!!busy || !allowed || !sql.trim()} onClick={() => void execute()}>{busy === 'query' ? <LoaderCircle size={16} className="spin" /> : <Play size={16} />}执行 SQL</button></div>
          <p className="muted">支持单表 SELECT、WHERE（比较、LIKE、IS NULL、AND/OR）、ORDER BY 和 LIMIT，最多返回100行。选择表后可直接执行预览。</p>
          {!allowed && <p className="notice">{current.enabled ? '共享数据库请通过“数据探索”按模型查询；直接 SQL 仅开放给当前租户专属数据源。' : '数据源已停用，启用后可查看数据。'}</p>}
          <textarea className="field source-sql-input" aria-label="SQL 语句" spellCheck={false} disabled={!!busy || !allowed} value={sql} placeholder="从表目录选择表，或输入 SELECT * FROM 表名 LIMIT 100;" onChange={(event) => { setSQL(event.target.value); setResult(undefined); setError('') }} />
          {result && <section className="source-sql-results" aria-label="SQL 查询结果"><p className="muted" role="status">返回 {result.rows.length} 行 · 耗时 {result.elapsedMs} ms{result.hasMore ? ` · 还有更多数据，当前仅展示前 ${result.limit} 行` : ''}</p><div className="table-wrap"><table><thead><tr>{result.columns.map((column, i) => <th key={i}>{column}</th>)}</tr></thead><tbody>{result.rows.map((row, i) => <tr key={i}>{row.map((value, j) => <td key={j}><div className="source-result-cell">{value === null ? <span className="muted">NULL</span> : value === '' ? <span className="muted">空字符串</span> : value}</div></td>)}</tr>)}{!result.rows.length && <tr><td colSpan={result.columns.length}>查询成功，没有符合条件的数据。</td></tr>}</tbody></table></div></section>}
        </section>
      </div>
    </div>
  </div>
}
