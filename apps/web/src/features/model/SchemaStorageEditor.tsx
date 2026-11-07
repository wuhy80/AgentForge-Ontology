import { useEffect, useMemo, useRef, useState } from 'react'
import { ArrowRight, Database, LoaderCircle, RefreshCw } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, DataType, Property, PropertyMapping, Schema, SchemaStorageMapping, SourceColumn, SourceTable, SourceTableInfo, StorageBinding } from '../../lib/types'
import { typeLabels } from './PropertyEditor'

function sameTable(mapping: PropertyMapping | undefined, binding: StorageBinding) {
  return !mapping || mapping.dataSourceId === binding.dataSourceId
    && (!mapping.table || mapping.table === binding.table)
    && (!mapping.namespace || mapping.namespace === binding.namespace)
}

function SourceOptions({ sources, selected }: { sources: DataSource[]; selected: string }) {
  return <><option value="">选择数据源</option>{selected && !sources.some((source) => source.id === selected) && <option value={selected}>当前绑定数据源（目录不可见）</option>}{sources.map((source) => <option key={source.id} value={source.id} disabled={!source.enabled}>{source.name} · {source.driver === 'postgresql' ? 'PostgreSQL' : 'MySQL'}{!source.enabled ? '（已停用）' : ''}</option>)}</>
}

// Share in-flight discovery between properties bound to the same source/table.
function createCatalog(api: ApiClient) {
  const tables = new Map<string, Promise<SourceTableInfo[]>>()
  const columns = new Map<string, Promise<SourceTable>>()
  return {
    tables(id: string, namespace?: string) {
      const key = JSON.stringify([id, namespace || ''])
      if (!tables.has(key)) tables.set(key, api.listSourceTables(id, namespace).catch((cause) => { tables.delete(key); throw cause }))
      return tables.get(key)!
    },
    table(id: string, table: string, namespace?: string, refresh = false) {
      const key = JSON.stringify([id, namespace || '', table])
      if (refresh) columns.delete(key)
      if (!columns.has(key)) columns.set(key, api.inspectSourceTable(id, table, namespace).catch((cause) => { columns.delete(key); throw cause }))
      return columns.get(key)!
    },
  }
}
type Catalog = ReturnType<typeof createCatalog>

function TableOptions({ tables, namespace, table, inherit }: { tables: SourceTableInfo[]; namespace?: string; table?: string; inherit?: string }) {
  const selected = table ? JSON.stringify([namespace || '', table]) : ''
  return <><option value="">{inherit || '选择已有表'}</option>{selected && !tables.some((item) => item.name === table && item.namespace === (namespace || '')) && <option value={selected}>{[namespace, table].filter(Boolean).join('.')}（当前配置）</option>}{tables.map((item) => <option key={JSON.stringify([item.namespace, item.name])} value={JSON.stringify([item.namespace, item.name])}>{[item.namespace, item.name].filter(Boolean).join('.')}</option>)}</>
}

export function SchemaStorageEditor({ api, schema, onChange, locked, active, currentId, currentETag, draft = false, onSources, section = 'all' }: {
  api: ApiClient
  schema: Schema
  onChange: (schema: Schema) => void
  locked: boolean
  active: boolean
  currentId?: string
  currentETag?: string
  draft?: boolean
  onSources?: () => void
  section?: 'all' | 'storage' | 'mappings'
}) {
  const [sources, setSources] = useState<DataSource[]>([])
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [inspecting, setInspecting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [tableError, setTableError] = useState('')
  const catalog = useMemo(() => createCatalog(api), [api, active])
  const schemaRef = useRef(schema)
  schemaRef.current = schema
  const activeRef = useRef(active)
  activeRef.current = active
  const binding = schema.storageBinding
  const source = sources.find((item) => item.id === binding?.dataSourceId)

  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    setSources([]); setLoading(true); setError('')
    api.listDataSources(controller.signal, true).then((items) => {
      if (!controller.signal.aborted) setSources(items)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '数据源目录读取失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, active])

  useEffect(() => {
    setTables([]); setTableError('')
    if (!active || section === 'mappings' || !binding?.dataSourceId) return
    const controller = new AbortController()
    catalog.tables(binding.dataSourceId, binding.namespace).then((items) => {
      if (!controller.signal.aborted) setTables(items)
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setTableError(cause instanceof Error ? cause.message : '主表目录读取失败') })
    return () => controller.abort()
  }, [catalog, active, section, binding?.dataSourceId, binding?.namespace])

  function updateBinding(patch: Partial<StorageBinding>) {
    if (!binding) return
    setNotice(''); setError('')
    onChange({ ...schema, storageBinding: { ...binding, ...patch } })
  }

  function chooseSource(id: string) {
    setNotice(''); setError('')
    const nextSource = sources.find((item) => item.id === id)
    onChange({
      ...schema,
      mode: id ? schema.mode : 'schema_2_db',
      storageBinding: id ? { dataSourceId: id, namespace: nextSource?.driver === 'postgresql' ? 'public' : undefined, table: '', idColumn: 'id' } : undefined,
      properties: schema.properties.map((property) => ({ ...property, mapping: id ? { dataSourceId: id, column: '' } : undefined })),
    })
  }

  async function importColumns() {
    if (!binding?.dataSourceId || !binding.table.trim()) { setError('请先选择数据源并填写主表名称。'); return }
    setInspecting(true); setError(''); setNotice('')
    try {
      const table = await api.inspectSourceTable(binding.dataSourceId, binding.table, binding.namespace)
      if (schemaRef.current !== schema || !activeRef.current) return
      const primary = table.columns.filter((column) => column.primaryKey)
      const idColumn = table.columns.some((column) => column.name === binding.idColumn) ? binding.idColumn! : primary.length === 1 ? primary[0].name : ''
      if (!idColumn) { setError('源表需要明确的稳定主键列；请选择主键后重新读取。'); return }
      const columns = table.columns.filter((column) => column.name !== idColumn && column.name !== binding.tenantColumn)
      const unsupported = columns.filter((column) => column.generated || !typeLabels[column.dataType])
      if (unsupported.length) { setError(`源列尚未支持：${unsupported.map((column) => `${column.name}（${column.generated ? '生成列暂未支持' : column.databaseType}）`).join('、')}。请手工配置已支持的列。`); return }
      if (!columns.length) { setError('主表没有可导入的业务属性列。'); return }
      const mapped: Property[] = columns.map((column) => {
        const old = schema.properties.find((property) => sameTable(property.mapping, binding) && property.mapping?.column === column.name)
        return { ...old, id: old?.id ?? '', name: old?.name ?? column.name, dataType: column.dataType as DataType, required: !column.nullable, nullable: column.nullable, readOnly: column.generated || undefined, precision: column.dataType === 'decimal' ? column.precision : undefined, scale: column.dataType === 'decimal' ? column.scale : undefined, mapping: { dataSourceId: binding.dataSourceId, namespace: table.namespace, table: table.name, column: column.name } }
      })
      const others = schema.properties.filter((property) => property.mapping && !sameTable(property.mapping, binding))
      onChange({ ...schema, storageBinding: { ...binding, namespace: table.namespace, idColumn }, properties: [...mapped, ...others], indexes: [] })
      setNotice(`已载入 ${mapped.length} 个主表属性，保留按源列匹配的属性身份与其他来源映射。请核对中文业务名称，重新配置业务索引。`)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '源表读取失败') }
    finally { setInspecting(false) }
  }

  return <section className="model-storage-section" aria-label={section === 'mappings' ? '属性数据源与列映射' : '存储与映射'}>
    {section !== 'mappings' && <>
    <div className="model-section-heading"><div><h3><Database size={17} />存储方向与主表</h3><p className="muted">主表标识业务对象；每个属性可绑定同表、其他表或其他数据源的列。</p></div>{onSources && <button type="button" className="button subtle" onClick={onSources}>数据源配置<ArrowRight size={14} /></button>}</div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {notice && <div className="notice notice-success" role="status">{notice}</div>}
    <div className="form-grid">
      <label>建模方向<select className="field" aria-label="建模方向" disabled={locked} value={schema.mode} onChange={(event) => onChange({ ...schema, mode: event.target.value })}><option value="schema_2_db">Schema → 数据库（以模型为准）</option><option value="db_2_schema" disabled={!binding}>数据库 → Schema（以数据库为准）</option></select></label>
      <label>主数据源<select className="field" aria-label="主数据源" disabled={locked || loading} value={binding?.dataSourceId ?? ''} onChange={(event) => chooseSource(event.target.value)}><option value="">平台默认存储</option>{binding?.dataSourceId && !sources.some((item) => item.id === binding.dataSourceId) && <option value={binding.dataSourceId}>当前绑定数据源（目录不可见）</option>}{sources.map((item) => <option key={item.id} value={item.id} disabled={!item.enabled}>{item.name} · {item.driver === 'postgresql' ? 'PostgreSQL' : 'MySQL'}{!item.enabled ? '（已停用）' : ''}</option>)}</select></label>
      {binding && <>
        <label>数据库命名空间<input className="field" aria-label="数据库命名空间" disabled={locked} value={binding.namespace ?? ''} onChange={(event) => updateBinding({ namespace: event.target.value || undefined })} placeholder={source?.driver === 'postgresql' ? '例如：public' : '留空使用数据源数据库'} /></label>
        <label>选择已有主表<select className="field" aria-label="主表选择" disabled={locked} value={binding.table ? JSON.stringify([binding.namespace || '', binding.table]) : ''} onChange={(event) => { const [namespace, table] = event.target.value ? JSON.parse(event.target.value) as string[] : ['', '']; updateBinding({ namespace: namespace || undefined, table }) }}><TableOptions tables={tables} namespace={binding.namespace} table={binding.table} /></select></label>
        <label>{schema.mode === 'schema_2_db' ? '主表名称（可新建）' : '已选主表名称'}<input className="field" aria-label="主表名称" disabled={locked} readOnly={schema.mode === 'db_2_schema'} value={binding.table} onChange={(event) => { if (schema.mode !== 'db_2_schema') updateBinding({ table: event.target.value }) }} placeholder={schema.mode === 'db_2_schema' ? '请从目录选择已有表' : '选择已有表或输入新表名称'} /></label>
        <label>对象主键列<input className="field" aria-label="对象主键列" disabled={locked} value={binding.idColumn ?? 'id'} onChange={(event) => updateBinding({ idColumn: event.target.value })} placeholder="稳定、唯一且非空的主键列" /></label>
        <label>主表租户列<input className="field" aria-label="主表租户列" disabled={locked} value={binding.tenantColumn ?? ''} onChange={(event) => updateBinding({ tenantColumn: event.target.value || undefined })} placeholder={source?.tenantIsolation === 'column' ? '共享数据源必须指定租户列' : '专属数据源可留空'} /></label>
      </>}
    </div>
    {tableError && <div className="notice notice-error" role="alert">{tableError}</div>}
    <p className="muted model-storage-explanation">{!binding ? '平台自动维护表和属性列；选择已配置的数据源后，可显式设置表列映射。' : schema.mode === 'schema_2_db' ? '以模型为准：保存草稿不修改数据库，预览后显式发布，创建或安全增加目标表列；已有未纳管表不会被直接覆盖。' : '以数据库为准：读取并校验已有表结构，不创建、修改或删除源表。源列名称需人工补充中文业务名称。'}</p>
    {binding && schema.mode === 'db_2_schema' && !locked && <button type="button" className="button" disabled={inspecting || !binding.table} onClick={() => void importColumns()}>{inspecting ? <LoaderCircle size={15} className="model-spin" /> : <RefreshCw size={15} />}从主表读取属性</button>}
    </>}
    {section === 'mappings' && error && <div className="notice notice-error" role="alert">{error}</div>}
    {section !== 'storage' && !loading && !error && !sources.length && <p className="muted model-storage-explanation">还没有配置外部数据源。可先到“数据源配置”添加 MySQL 或 PostgreSQL，再在模型中选择对应的表和字段。</p>}
    {section !== 'storage' && <EffectiveStorageMapping api={api} schema={schema} currentId={currentId} currentETag={currentETag} draft={draft} active={active} onSources={onSources} />}
    {section !== 'storage' && binding && <>
      <div className="model-section-heading model-mapping-heading"><div><h3>属性数据源与列映射</h3><p className="muted">其他来源通过唯一关联键匹配主表对象主键，每个对象最多匹配一行。跨来源属性须允许空值；一对多使用关系或子对象。</p></div></div>
      <div className="model-property-mappings">{schema.properties.map((property, index) => <PropertyMappingEditor key={property.id || `mapping-${index}`} catalog={catalog} active={active} forward={schema.mode === 'schema_2_db'} index={index} property={property} binding={binding} sources={sources} locked={locked && !!property.id} onChange={(mapping) => onChange({ ...schema, properties: schema.properties.map((item, offset) => offset === index ? { ...item, mapping } : item) })} />)}</div>
      <p className="muted model-storage-explanation">当前跨来源属性支持投影读取，筛选与排序使用主表属性。绑定对象暂不提供实例写入；多来源结构同步可恢复，不承诺跨库原子提交。</p>
    </>}
  </section>
}

function EffectiveStorageMapping({ api, schema, currentId, currentETag, draft, active, onSources }: {
  api: ApiClient; schema: Schema; currentId?: string; currentETag?: string; draft: boolean; active: boolean; onSources?: () => void
}) {
  const [result, setResult] = useState<SchemaStorageMapping>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    setResult(undefined); setError(''); setBusy(false)
    if (!active || !currentId) return
    const controller = new AbortController()
    setBusy(true)
    api.getSchemaStorageMapping(currentId, controller.signal).then((mapping) => {
      if (controller.signal.aborted) return
      if (mapping.schemaId !== currentId || currentETag && mapping.definitionETag !== currentETag.replace(/^"|"$/g, '')) {
        setError('当前定义已更新，请载入最新内容后查看映射。'); return
      }
      setResult(mapping)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '数据库映射读取失败')
    }).finally(() => { if (!controller.signal.aborted) setBusy(false) })
    return () => controller.abort()
  }, [api, currentId, currentETag, active, refresh])
  if (!currentId && schema.storageBinding) return null
  return <section className="model-effective-mapping" aria-label="数据库映射概览">
    <div className="model-section-heading"><div><h3><Database size={17} />{currentId ? '当前生效的数据库映射' : '平台存储映射'}</h3><p className="muted">{currentId ? draft ? '草稿尚未发布；以下显示当前生效定义的实际数据源、表和字段。' : '从数据库目录读取，逐项核对属性对应的物理位置。' : '使用平台默认 MySQL；表与字段将在首次发布时生成。'}</p></div><div className="model-inline-actions">{currentId && <button className="button subtle" type="button" disabled={busy} onClick={() => setRefresh((value) => value + 1)}><RefreshCw size={14} className={busy ? 'model-spin' : undefined} />刷新映射</button>}{onSources && <button type="button" className="button subtle" onClick={onSources}>数据源配置<ArrowRight size={14} /></button>}</div></div>
    {busy && <p className="muted" role="status">正在读取数据库映射…</p>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {result && <>
      <p className="model-mapping-location"><strong>{result.primary.dataSourceName}</strong><span>{result.primary.driver === 'postgresql' ? 'PostgreSQL' : 'MySQL'}</span><code>{[result.primary.namespace, result.primary.table].filter(Boolean).join('.')}</code><span>对象主键：<code>{result.primary.keyColumn}</code></span>{result.primary.tenantColumn && <span>租户列：<code>{result.primary.tenantColumn}</code></span>}</p>
      <div className="table-wrap model-effective-mapping-table"><table aria-label="当前生效的数据库映射"><thead><tr><th>属性</th><th>数据源</th><th>数据库 / 命名空间</th><th>表</th><th>字段</th><th>数据库类型</th><th>状态</th></tr></thead><tbody>{result.properties.map((property) => <tr key={property.propertyId}><td>{property.propertyName}</td><td>{property.dataSourceName}</td><td><code>{property.namespace}</code></td><td><code>{property.table}</code></td><td><code>{property.column}</code></td><td><code>{property.databaseType || '—'}</code></td><td><span className={property.exists ? 'badge' : 'model-mapping-warning'}>{property.exists ? '已映射' : '字段缺失'}</span></td></tr>)}</tbody></table></div>
      {result.managed && <p className="muted model-storage-explanation">表名和字段名由稳定 ID 生成，修改中文名称不会改变映射。更换数据源、表或已有字段需要迁移。</p>}
    </>}
    {!currentId && <div className="table-wrap model-effective-mapping-table"><table aria-label="待发布的平台存储映射"><thead><tr><th>属性</th><th>数据源</th><th>表</th><th>字段</th><th>状态</th></tr></thead><tbody>{schema.properties.map((property, index) => <tr key={property.id || index}><td>{property.name || `未命名属性 ${index + 1}`}</td><td>平台默认存储 · MySQL</td><td>发布时生成</td><td>发布时生成</td><td>尚未生效</td></tr>)}</tbody></table></div>}
  </section>
}

function PropertyMappingEditor({ catalog, active, forward, index, property, binding, sources, locked, onChange }: {
  catalog: Catalog
  active: boolean
  forward: boolean
  index: number
  property: Property
  binding: StorageBinding
  sources: DataSource[]
  locked: boolean
  onChange: (mapping: PropertyMapping) => void
}) {
  const mapping = property.mapping ?? { dataSourceId: binding.dataSourceId, column: '' }
  const primary = sameTable(mapping, binding)
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [columns, setColumns] = useState<SourceColumn[]>([])
  const [busy, setBusy] = useState(false)
  const [tableError, setTableError] = useState('')
  const [error, setError] = useState('')
  const [newTable, setNewTable] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const label = `属性${index + 1}`
  const source = sources.find((item) => item.id === mapping.dataSourceId)
  const namespace = mapping.namespace ?? (mapping.dataSourceId === binding.dataSourceId ? binding.namespace : source?.driver === 'postgresql' ? 'public' : undefined)
  const tableName = mapping.table || (mapping.dataSourceId === binding.dataSourceId ? binding.table : '')
  useEffect(() => {
    let stale = false
    setTables([]); setTableError('')
    if (active && mapping.dataSourceId) catalog.tables(mapping.dataSourceId, namespace).then((items) => { if (!stale) setTables(items) })
      .catch((cause: unknown) => { if (!stale) setTableError(cause instanceof Error ? cause.message : '表目录读取失败') })
    return () => { stale = true }
  }, [catalog, active, mapping.dataSourceId, namespace])
  useEffect(() => {
    let stale = false
    setColumns([]); setError(''); setNewTable(false); setBusy(false)
    if (!active || !mapping.dataSourceId || !tableName) return
    setBusy(true)
    catalog.table(mapping.dataSourceId, tableName, namespace, refresh > 0).then((table) => {
      if (!stale) setColumns(table.columns)
    }).catch((cause: unknown) => {
      if (stale) return
      if (forward && cause instanceof ApiError && cause.status === 404) setNewTable(true)
      else setError(cause instanceof Error ? cause.message : '列目录读取失败')
    }).finally(() => { if (!stale) setBusy(false) })
    return () => { stale = true }
  }, [catalog, active, mapping.dataSourceId, namespace, tableName, forward, refresh])
  function update(patch: Partial<PropertyMapping>) { onChange({ ...mapping, ...patch }) }
  function chooseTable(value: string) {
    const [selectedNamespace, table] = value ? JSON.parse(value) as string[] : ['', '']
    update({ namespace: selectedNamespace || undefined, table: table || undefined, column: '', keyColumn: undefined, tenantColumn: undefined })
  }
  function incompatible(column: SourceColumn) {
    if (column.generated && column.name !== (primary ? binding.idColumn ?? 'id' : mapping.keyColumn)) return true
    if (property.dataType === 'uuid') return column.dataType !== 'uuid' && !(column.dataType === 'string' && column.maxLength === 36)
    if (property.dataType === 'longtext' && source?.driver === 'postgresql' && column.dataType === 'text') return false
    return column.dataType !== property.dataType || property.dataType === 'decimal' && (column.precision !== property.precision || column.scale !== property.scale)
  }
  const selectedColumn = columns.find((column) => column.name === mapping.column)
  return <div className="model-property-mapping">
    <div className="model-mapping-title"><strong>{property.name || `未命名属性 ${index + 1}`}</strong><span className="badge">{primary ? '主表属性' : '关联来源属性'}</span><button type="button" className="button subtle" disabled={busy || !mapping.dataSourceId || !tableName} onClick={() => setRefresh((value) => value + 1)}>{busy ? <LoaderCircle size={14} className="model-spin" /> : <RefreshCw size={14} />}读取列目录</button></div>
    <div className="model-mapping-grid">
      <label>数据源<select className="field" aria-label={`${label}数据源`} disabled={locked} value={mapping.dataSourceId} onChange={(event) => onChange({ dataSourceId: event.target.value, namespace: sources.find((item) => item.id === event.target.value)?.driver === 'postgresql' ? 'public' : undefined, column: '' })}><SourceOptions sources={sources} selected={mapping.dataSourceId} /></select></label>
      <label>命名空间<input className="field" aria-label={`${label}命名空间`} disabled={locked} value={mapping.namespace ?? ''} onChange={(event) => update({ namespace: event.target.value || undefined, table: undefined, column: '', keyColumn: undefined, tenantColumn: undefined })} placeholder={mapping.dataSourceId === binding.dataSourceId ? binding.namespace || '使用主表命名空间' : source?.driver === 'postgresql' ? '例如：public' : '使用数据源数据库'} /></label>
      <label>选择已有来源表<select className="field" aria-label={`${label}来源表选择`} disabled={locked || !mapping.dataSourceId} value={mapping.table ? JSON.stringify([mapping.namespace || '', mapping.table]) : ''} onChange={(event) => chooseTable(event.target.value)}><TableOptions tables={tables} namespace={mapping.namespace} table={mapping.table} inherit={mapping.dataSourceId === binding.dataSourceId ? `继承主表：${binding.table || '未选择'}` : undefined} /></select></label>
      <label>{forward ? '来源表（可新建）' : '已选来源表'}<input className="field" aria-label={`${label}来源表`} disabled={locked} readOnly={!forward} value={mapping.table ?? ''} onChange={(event) => { if (forward) update({ table: event.target.value || undefined, column: '', keyColumn: undefined, tenantColumn: undefined }) }} placeholder={mapping.dataSourceId === binding.dataSourceId ? binding.table || '使用主表' : forward ? '选择已有表或输入新表名称' : '请从目录选择已有表'} /></label>
      <label>选择数据库字段<select className="field" aria-label={`${label}字段选择`} disabled={locked || busy || !tableName} value={mapping.column} onChange={(event) => update({ column: event.target.value })}><option value="">选择物理字段</option>{mapping.column && !columns.some((column) => column.name === mapping.column) && <option value={mapping.column}>{mapping.column}（当前配置）</option>}{columns.map((column) => <option key={column.name} value={column.name} disabled={incompatible(column)}>{column.name} · {column.databaseType}{column.generated && incompatible(column) ? '（生成列暂未支持）' : incompatible(column) ? '（与属性类型不兼容）' : ''}</option>)}</select></label>
      <label>{forward ? '映射列（可新建）' : '已选映射列'}<input className="field" aria-label={`${label}映射列`} disabled={locked} readOnly={!forward} value={mapping.column} onChange={(event) => { if (forward) update({ column: event.target.value }) }} placeholder={forward ? '选择字段或输入新字段名' : '请从目录选择已有字段'} /></label>
      <label>{primary ? '主键列（继承）' : '唯一关联键列'}<input className="field" aria-label={`${label}关联键列`} list={`model-key-columns-${index}`} disabled={locked || primary} value={primary ? binding.idColumn ?? 'id' : mapping.keyColumn ?? ''} onChange={(event) => update({ keyColumn: event.target.value || undefined })} placeholder="唯一、非空，匹配对象主键" /><datalist id={`model-key-columns-${index}`}>{columns.map((column) => <option key={column.name} value={column.name}>{column.databaseType}</option>)}</datalist></label>
      <label>租户列<input className="field" aria-label={`${label}租户列`} list={`model-key-columns-${index}`} disabled={locked || primary} value={primary ? binding.tenantColumn ?? '' : mapping.tenantColumn ?? ''} onChange={(event) => update({ tenantColumn: event.target.value || undefined })} placeholder={source?.tenantIsolation === 'column' ? '共享来源须填写租户列' : '专属来源可留空'} /></label>
    </div>
    {tableError && <div className="notice notice-error" role="alert">{tableError}</div>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {newTable && <p className="muted model-storage-explanation">当前表尚不存在；请填写目标字段名，预览并发布后按模型创建。</p>}
    {selectedColumn && incompatible(selectedColumn) && <p className="model-mapping-warning" role="alert">当前字段 {selectedColumn.name} 与属性类型不兼容，或属于尚未支持的生成列；请重新选择并核对模型类型。</p>}
    {!primary && !property.nullable && <p className="model-mapping-warning">关联来源可能没有匹配行，请在对象属性中启用“允许空值”。</p>}
  </div>
}
