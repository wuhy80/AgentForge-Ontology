import { useEffect, useId, useRef, useState } from 'react'
import { ArrowLeft, ChevronLeft, ChevronRight, Database, LoaderCircle, Play, RefreshCw, Search, Table2 } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceDatabaseMetric, SourceIndex, SourceOverview, SourceQueryResult, SourceTable, SourceTableInfo } from '../../lib/types'

export function sourcePreviewSQL(driver: DataSource['driver'], table: SourceTableInfo): string {
  const quote = (name: string) => driver === 'mysql' ? `\`${name.replaceAll('`', '``')}\`` : `"${name.replaceAll('"', '""')}"`
  return `SELECT * FROM ${quote(table.namespace)}.${quote(table.name)} LIMIT 100;`
}

const views = [{ id: 'fields', label: '字段详情' }, { id: 'preview', label: '表数据预览' }, { id: 'sql', label: 'SQL控制台' }] as const
type View = typeof views[number]['id']
const pageSize = 20
const sameTable = (a?: SourceTableInfo, b?: SourceTableInfo) => !!a && !!b && a.namespace === b.namespace && a.name === b.name
const indexType = (index: SourceIndex) => index.primary ? '主键' : index.unique ? `${index.predicate ? '条件' : index.columns.length > 1 ? '联合' : ''}唯一索引` : '普通索引'

function Metric({ label, metric, error }: { label: string; metric?: SourceDatabaseMetric; error?: string }) {
  return <div className="source-metric"><dt>{label}<span>{metric ? metric.scope === 'instance' ? '实例' : '当前数据库' : '—'}</span></dt>
    <dd>{metric?.available && metric.value !== undefined ? <><strong>{metric.value}</strong><small>{metric.unit === 'bytes' ? '字节' : metric.unit === 'queries/s' ? '次/秒' : '个'}</small></> : <strong className="source-metric-unavailable">暂不可用</strong>}</dd>
    <p>{metric?.available ? metric.basis : metric?.reason || error || '尚未取得实时指标'}{metric?.sampleWindowMs ? ` · 采样 ${metric.sampleWindowMs} ms` : ''}</p>
  </div>
}

function QueryResult({ value, label }: { value: SourceQueryResult; label: string }) {
  return <section className="source-sql-results" aria-label={label}><p className="muted" role="status">返回 {value.rows.length} 行 · 耗时 {value.elapsedMs} ms{value.hasMore ? ` · 还有更多数据，当前仅展示前 ${value.limit} 行` : ''}</p>
    <div className="table-wrap"><table><thead><tr>{value.columns.map((column, i) => <th key={i}>{column}</th>)}</tr></thead><tbody>
      {value.rows.map((row, i) => <tr key={i}>{row.map((cell, j) => <td key={j}><div className="source-result-cell">{cell === null ? <span className="muted">NULL</span> : cell === '' ? <span className="muted">空字符串</span> : cell}</div></td>)}</tr>)}
      {!value.rows.length && <tr><td colSpan={Math.max(1, value.columns.length)}>查询成功，没有符合条件的数据。</td></tr>}
    </tbody></table></div>
  </section>
}

export function SourceManager({ api, source, active, onBack, onDenied }: { api: ApiClient; source: DataSource; active: boolean; onBack: () => void; onDenied: (cause: unknown) => void }) {
  const [current, setCurrent] = useState<DataSource>()
  const [overview, setOverview] = useState<SourceOverview>()
  const [overviewError, setOverviewError] = useState('')
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [selected, setSelected] = useState<SourceTableInfo>()
  const [table, setTable] = useState<SourceTable>()
  const [namespace, setNamespace] = useState(source.driver === 'mysql' ? source.database : 'public')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [fieldSearch, setFieldSearch] = useState('')
  const [view, setView] = useState<View>('fields')
  const [sql, setSQL] = useState('')
  const [result, setResult] = useState<SourceQueryResult>()
  const [preview, setPreview] = useState<SourceQueryResult>()
  const [loading, setLoading] = useState(false)
  const [inspecting, setInspecting] = useState(false)
  const [querying, setQuerying] = useState(false)
  const [stale, setStale] = useState(false)
  const [error, setError] = useState('')
  const generation = useRef(0), detailSequence = useRef(0), querySequence = useRef(0)
  const catalogRequest = useRef<AbortController | undefined>(undefined), detailRequest = useRef<AbortController | undefined>(undefined), queryRequest = useRef<AbortController | undefined>(undefined)
  const selectedRef = useRef<SourceTableInfo | undefined>(undefined)
  const namespaceRef = useRef(namespace)
  const tabId = useId()
  const allowed = !!current?.enabled && current.tenantIsolation === 'dedicated' && !stale

  function cancelQueries() {
    queryRequest.current?.abort(); querySequence.current++
    setResult(undefined); setPreview(undefined); setQuerying(false)
  }
  function abortAll() {
    catalogRequest.current?.abort(); detailRequest.current?.abort(); queryRequest.current?.abort()
    generation.current++; detailSequence.current++; querySequence.current++
  }
  function report(cause: unknown) {
    const message = cause instanceof Error ? cause.message : '读取数据源失败。'
    if (cause instanceof ApiError && (cause.status === 401 || cause.status === 403)) {
      abortAll(); setCurrent(undefined); setOverview(undefined); setTables([]); setTable(undefined); setSelected(undefined); selectedRef.current = undefined
      setSQL(''); setResult(undefined); setPreview(undefined); setLoading(false); setInspecting(false); setQuerying(false); setOverviewError(''); setError(message); onDenied(cause)
    } else if (cause instanceof ApiError && cause.status === 412) {
      cancelQueries(); setStale(true); setOverview(undefined); setError(`${message}，请点击“刷新结构”重新读取配置后再查询。`)
    } else setError(message)
  }

  async function inspect(item: SourceTableInfo, saved = current, epoch = generation.current) {
    if (!saved || epoch !== generation.current) return
    detailRequest.current?.abort(); cancelQueries()
    const request = ++detailSequence.current, controller = new AbortController()
    detailRequest.current = controller; selectedRef.current = item
    setSelected(item); setTable(undefined); setFieldSearch(''); setInspecting(true); setError('')
    setSQL(sourcePreviewSQL(saved.driver, item))
    try {
      const value = await api.inspectSourceTable(saved.id, item.name, item.namespace, controller.signal)
      if (controller.signal.aborted || epoch !== generation.current || request !== detailSequence.current) return
      if (value.name !== item.name || value.namespace !== item.namespace) throw new Error('返回的表结构与当前选择不一致，请刷新结构。')
      setTable(value)
    } catch (cause) { if (!controller.signal.aborted && epoch === generation.current && request === detailSequence.current) report(cause) }
    finally { if (!controller.signal.aborted && epoch === generation.current && request === detailSequence.current) setInspecting(false) }
  }

  async function reload(reset = false) {
    const previous = reset ? undefined : selectedRef.current
    abortAll(); cancelQueries()
    const epoch = generation.current, controller = new AbortController()
    catalogRequest.current = controller
    setLoading(true); setInspecting(false); setCurrent(undefined); setOverview(undefined); setOverviewError(''); setTable(undefined); setTables([]); setSelected(undefined); setError(''); setStale(false)
    if (reset) { setView('fields'); setSearch(''); setPage(1); setSQL(''); selectedRef.current = undefined }
    try {
      const saved = await api.getDataSource(source.id, controller.signal)
      if (controller.signal.aborted || epoch !== generation.current) return
      if (saved.id !== source.id || saved.tenantId !== source.tenantId) throw new Error('返回的数据源身份不一致，请返回列表重新打开。')
      setCurrent(saved)
      if (!saved.enabled) { setError('数据源已停用，请返回列表编辑并启用。'); return }
      const targetNamespace = saved.driver === 'mysql' ? saved.database : reset ? 'public' : namespaceRef.current.trim() || 'public'
      namespaceRef.current = targetNamespace; setNamespace(targetNamespace)
      const valid = () => !controller.signal.aborted && epoch === generation.current
      const guard = <T,>(request: Promise<T>) => request.catch((cause: unknown) => {
        if (valid() && cause instanceof ApiError && [401, 403, 412].includes(cause.status)) report(cause)
        throw cause
      })
      const responses = await Promise.allSettled([
        guard(api.getSourceOverview(saved.id, controller.signal)),
        guard(api.listSourceTables(saved.id, targetNamespace, controller.signal)),
      ])
      if (!valid()) return
      // Permission/configuration errors take precedence over partial catalog data.
      for (const response of responses) if (response.status === 'rejected' && response.reason instanceof ApiError && [401, 403, 412].includes(response.reason.status)) { report(response.reason); return }
      const [summary, catalog] = responses
      if (summary.status === 'fulfilled') {
        if (summary.value.sourceId !== saved.id || summary.value.etag !== saved.etag || summary.value.database !== saved.database) { report(new ApiError(412, 'precondition_failed', '数据源配置已更新')); return }
        setOverview(summary.value)
      } else setOverviewError(summary.reason instanceof Error ? summary.reason.message : '无法读取数据库实时概览')
      if (catalog.status === 'rejected') { report(catalog.reason); return }
      setTables(catalog.value); setPage(1)
      const next = catalog.value.find((item) => sameTable(item, previous)) || catalog.value[0]
      if (next) await inspect(next, saved, epoch)
      else { selectedRef.current = undefined; setSQL('') }
    } catch (cause) { if (!controller.signal.aborted && epoch === generation.current) report(cause) }
    finally { if (!controller.signal.aborted && epoch === generation.current) setLoading(false) }
  }

  useEffect(() => {
    if (active) void reload(true)
    else {
      abortAll(); setCurrent(undefined); setOverview(undefined); setTables([]); setTable(undefined); setSelected(undefined); selectedRef.current = undefined
      setResult(undefined); setPreview(undefined); setSQL(''); setError(''); setLoading(false); setInspecting(false); setQuerying(false)
    }
    return abortAll
  }, [api, source.id, active])

  async function execute(mode: 'preview' | 'sql') {
    if (!current || !allowed || loading || inspecting || querying || (mode === 'preview' && !selected)) return
    const statement = mode === 'preview' && selected ? sourcePreviewSQL(current.driver, selected) : sql
    if (!statement.trim()) return
    cancelQueries()
    const epoch = generation.current, request = ++querySequence.current, controller = new AbortController()
    queryRequest.current = controller; setQuerying(true); setError('')
    try {
      const value = await api.queryDataSource(current.id, statement, current.etag, controller.signal)
      if (!controller.signal.aborted && epoch === generation.current && request === querySequence.current) {
        if (mode === 'preview') setPreview(value); else setResult(value)
      }
    } catch (cause) { if (!controller.signal.aborted && epoch === generation.current && request === querySequence.current) report(cause) }
    finally { if (!controller.signal.aborted && epoch === generation.current && request === querySequence.current) setQuerying(false) }
  }
  function changeView(next: View) {
    if (next === view) return
    cancelQueries(); setView(next)
    if (!stale) setError('')
  }
  const filtered = tables.filter((item) => `${item.namespace}.${item.name}`.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize)), currentPage = Math.min(page, pages)
  const rows = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize)
  const count = (key: 'columnCount' | 'primaryKeyCount') => tables.every((item) => item[key] !== undefined) ? tables.reduce((total, item) => total + item[key]!, 0) : '—'
  const fields = table?.columns.filter((column) => `${column.name} ${column.databaseType} ${column.comment || ''}`.toLocaleLowerCase().includes(fieldSearch.trim().toLocaleLowerCase())) || []
  const blocked = !current?.enabled ? '数据源未启用，启用后可查看数据。' : current.tenantIsolation !== 'dedicated' ? '共享数据库请通过“数据探索”按模型查询；表数据预览和直接 SQL 仅开放给当前租户专属数据源。' : stale ? '配置已更新，请刷新结构后再查询。' : ''
  if (!active) return null

  return <div className="sources-page source-manager">
    <nav className="source-breadcrumb" aria-label="数据库管理导航"><button type="button" className="button subtle source-back" aria-label="返回数据源列表" onClick={() => { abortAll(); cancelQueries(); onBack() }}><ArrowLeft size={15} />返回列表</button><span>/</span><span>数据库管理</span></nav>
    <div className="sources-heading"><div><h1>数据库管理</h1><p className="muted">{current?.name || '正在读取数据源'} · 浏览数据库结构、预览表数据与执行查询</p></div>{current && <span className="badge">{current.driver === 'mysql' ? 'MySQL' : 'PostgreSQL'}</span>}</div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    <section className="card source-database-info" aria-label="数据库基础信息" aria-busy={loading}>
      <div className="sources-panel-heading"><h2><Database size={18} />数据库基础信息</h2><button type="button" className="button subtle" disabled={loading} onClick={() => void reload()}>{loading ? <LoaderCircle size={15} className="spin" /> : <RefreshCw size={15} />}刷新结构</button></div>
      {current ? <><div className="source-info-grid">
        <div><h3>连接信息</h3><dl>{[['主机 IP', current.host || '未返回'], ['端口', String(current.port || (current.driver === 'mysql' ? 3306 : 5432))], ['数据库名', current.database], ['用户名', current.username || '未返回']].map(([name, value]) => <div key={name}><dt>{name}</dt><dd>{value}</dd></div>)}</dl></div>
        <div><h3>详细配置</h3><dl>{[['数据库类型', current.driver === 'mysql' ? 'MySQL' : 'PostgreSQL'], ['数据库版本', overview?.serverVersion || '暂不可用'], ['字符集', overview?.charset || '暂不可用'], ['数据隔离', current.tenantIsolation === 'dedicated' ? '当前租户专属' : '共享数据库']].map(([name, value]) => <div key={name}><dt>{name}</dt><dd>{value}</dd></div>)}</dl></div>
      </div><dl className="source-metrics"><Metric label="当前连接数" metric={overview?.metrics.connections} error={overviewError} /><Metric label="存储大小" metric={overview?.metrics.storageBytes} error={overviewError} /><Metric label="QPS" metric={overview?.metrics.qps} error={overviewError} /></dl>
      {overviewError && <p className="muted">实时概览暂不可用：{overviewError}。表目录与结构可独立查看。</p>}
      {overview && <p className="source-checked-at muted">采集时间：{overview.checkedAt}</p>}</> : <p className="muted">{loading ? '正在读取数据库配置与实时指标…' : '暂无可显示的数据库信息。'}</p>}
    </section>
    <div className="source-management-layout">
      <section className="card source-table-catalog" aria-label="数据库表目录" aria-busy={loading}>
        <h2><Table2 size={17} />数据表<span className="badge">{tables.length}</span></h2>
        <p className="source-catalog-summary">{tables.length} 张表 <span>·</span> {count('columnCount')} 个字段 <span>·</span> {count('primaryKeyCount')} 个主键列</p>
        {tables.some((item) => item.columnCount === undefined || item.primaryKeyCount === undefined) && <p className="muted">部分目录统计暂不可用，以实际表结构为准。</p>}
        {current?.driver === 'postgresql' && <form className="source-namespace" onSubmit={(event) => { event.preventDefault(); void reload() }}><label>命名空间<input className="field" aria-label="目录命名空间" disabled={loading} value={namespace} onChange={(event) => { setNamespace(event.target.value); namespaceRef.current = event.target.value }} /></label><button type="submit" className="button subtle" disabled={loading || !namespace.trim()}>加载表目录</button></form>}
        <div className="source-search"><Search size={15} /><input className="field" aria-label="搜索数据库表" placeholder="搜索表名…" value={search} onChange={(event) => { setSearch(event.target.value); setPage(1) }} /></div>
        <div className="source-table-list table-wrap"><table><thead><tr><th>表名</th><th>字段数</th><th title="主键包含的字段数量">主键数</th></tr></thead><tbody>{rows.map((item) => <tr key={`${item.namespace}:${item.name}`} className={sameTable(item, selected) ? 'is-active' : ''}><td><button type="button" aria-label={`${item.namespace}.${item.name}`} aria-pressed={sameTable(item, selected)} disabled={loading || stale || !current} onClick={() => { setView('fields'); void inspect(item) }} title={`${item.namespace}.${item.name}`}><Table2 size={14} /><span>{item.name}</span></button></td><td>{item.columnCount ?? '—'}</td><td>{item.primaryKeyCount ?? '—'}</td></tr>)}
          {!rows.length && <tr><td colSpan={3} className="sources-list-empty">{loading ? '正在读取数据库表…' : search ? '没有匹配的数据表。' : '当前命名空间没有可查看的表。'}</td></tr>}
        </tbody></table></div>
        <nav className="source-pagination" aria-label="数据库表分页"><span>共 {filtered.length} 张 · 第 {currentPage} / {pages} 页</span><div><button type="button" className="button subtle" aria-label="上一页" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}><ChevronLeft size={15} /></button><button type="button" className="button subtle" aria-label="下一页" disabled={currentPage >= pages} onClick={() => setPage(currentPage + 1)}><ChevronRight size={15} /></button></div></nav>
      </section>
      <section className="card source-management-content" aria-label="数据库控制台">
        <div className="source-console-heading"><h2>数据库控制台{selected ? `：${selected.name}` : ''}</h2><p className="muted">{selected ? `${selected.namespace}.${selected.name}` : '从左侧选择数据表查看详情'}</p></div>
        <div className="source-view-tabs" role="tablist" aria-label="数据库管理视图">{views.map((item, index) => <button key={item.id} type="button" id={`${tabId}-${item.id}-tab`} role="tab" aria-selected={view === item.id} aria-controls={`${tabId}-${item.id}-panel`} tabIndex={view === item.id ? 0 : -1} onClick={() => changeView(item.id)} onKeyDown={(event) => {
          const next = event.key === 'ArrowRight' ? (index + 1) % views.length : event.key === 'ArrowLeft' ? (index + views.length - 1) % views.length : event.key === 'Home' ? 0 : event.key === 'End' ? views.length - 1 : -1
          if (next >= 0) { event.preventDefault(); changeView(views[next].id); document.getElementById(`${tabId}-${views[next].id}-tab`)?.focus() }
        }}>{item.label}</button>)}</div>
        {view === 'fields' && <div role="tabpanel" id={`${tabId}-fields-panel`} aria-labelledby={`${tabId}-fields-tab`} tabIndex={0}><section className="source-table-details" aria-label="数据源表结构">
          <div className="source-details-toolbar"><p className="muted">{table ? `${table.columns.length} 个字段 · ${table.indexes.length} 个索引` : '字段与索引信息'}</p><div className="source-search"><Search size={15} /><input className="field" aria-label="搜索字段" placeholder="搜索字段、类型或注释…" value={fieldSearch} onChange={(event) => setFieldSearch(event.target.value)} /></div></div>
          {inspecting ? <p className="source-empty muted" role="status">正在读取表结构…</p> : !table ? <p className="source-empty muted">{loading ? '正在加载表目录…' : '选择数据表后查看字段详情。'}</p> : <><div className="table-wrap source-fields-table"><table><thead><tr><th>字段名</th><th>类型</th><th>可空</th><th>默认值</th><th>索引</th><th>注释</th></tr></thead><tbody>{fields.map((column) => {
            const indexes = table.indexes.filter((index) => index.columns.includes(column.name))
            return <tr key={column.name}><td><code>{column.name}</code>{column.generated && <small className="muted"> · 生成列</small>}</td><td><code>{column.databaseType}</code></td><td>{column.nullable ? '是' : '否'}</td><td>{column.defaultValue === undefined ? <span className="muted">未返回</span> : column.defaultValue === null ? <span className="muted">NULL</span> : column.defaultValue === '' ? <span className="muted">空字符串</span> : <code>{column.defaultValue}</code>}</td><td>{indexes.length ? indexes.map((index) => <span className="source-index-label" key={index.name}>{indexType(index)}<small>{index.name}</small></span>) : column.primaryKey ? '主键' : '—'}</td><td>{column.comment || '—'}</td></tr>
          })}{!fields.length && <tr><td colSpan={6} className="sources-list-empty">没有匹配的字段。</td></tr>}</tbody></table></div>
          <h3>索引定义 <span className="muted">{table.indexes.length}</span></h3>{table.indexes.length ? <div className="table-wrap source-indexes-table"><table><thead><tr><th>索引名称</th><th>索引类型</th><th>包含字段</th><th>条件</th></tr></thead><tbody>{table.indexes.map((index) => <tr key={index.name}><td>{index.name}</td><td>{indexType(index)}</td><td>{index.columns.join(', ')}</td><td>{index.predicate || '—'}</td></tr>)}</tbody></table></div> : <p className="muted">未发现索引。</p>}</>}
        </section></div>}
        {view === 'preview' && <div role="tabpanel" id={`${tabId}-preview-panel`} aria-labelledby={`${tabId}-preview-tab`} tabIndex={0}><section className="source-preview" aria-label="表数据预览"><div className="sources-panel-heading"><p className="muted">显式读取当前表的前 100 行数据。</p><button type="button" className="button primary" disabled={loading || inspecting || querying || !allowed || !table} onClick={() => void execute('preview')}>{querying ? <LoaderCircle size={15} className="spin" /> : <Play size={15} />}预览表数据</button></div>{blocked && <p className="notice">{blocked}</p>}{preview ? <QueryResult value={preview} label="表数据预览结果" /> : <p className="source-empty muted">{querying ? '正在读取表数据…' : '点击“预览表数据”加载当前表的数据。'}</p>}</section></div>}
        {view === 'sql' && <div role="tabpanel" id={`${tabId}-sql-panel`} aria-labelledby={`${tabId}-sql-tab`} tabIndex={0}><section className="source-sql" aria-label="SQL 查询"><div className="sources-panel-heading"><h3>SQL 查询</h3><button type="button" className="button primary" disabled={loading || inspecting || querying || !allowed || !sql.trim()} onClick={() => void execute('sql')}>{querying ? <LoaderCircle size={15} className="spin" /> : <Play size={15} />}执行 SQL</button></div><p className="muted">支持单表 SELECT、WHERE（比较、LIKE、IS NULL、AND/OR）、ORDER BY 和 LIMIT，最多返回 100 行。</p>{blocked && <p className="notice">{blocked}</p>}<textarea className="field source-sql-input" aria-label="SQL 语句" spellCheck={false} disabled={loading || inspecting || !allowed} value={sql} placeholder="SELECT * FROM 表名 LIMIT 100;" onChange={(event) => { cancelQueries(); setSQL(event.target.value); if (!stale) setError('') }} />{result && <QueryResult value={result} label="SQL 查询结果" />}</section></div>}
      </section>
    </div>
  </div>
}
