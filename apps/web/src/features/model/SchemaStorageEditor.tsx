import { useEffect, useRef, useState } from 'react'
import { ArrowRight, Database, LoaderCircle, RefreshCw } from 'lucide-react'
import type { ApiClient } from '../../lib/api'
import type { DataSource, DataType, Property, PropertyMapping, Schema, SourceColumn, SourceTableInfo, StorageBinding } from '../../lib/types'
import { typeLabels } from './PropertyEditor'

function sameTable(mapping: PropertyMapping | undefined, binding: StorageBinding) {
  return !mapping || mapping.dataSourceId === binding.dataSourceId
    && (!mapping.table || mapping.table === binding.table)
    && (!mapping.namespace || mapping.namespace === binding.namespace)
}

function SourceOptions({ sources, selected }: { sources: DataSource[]; selected: string }) {
  return <><option value="">选择数据源</option>{selected && !sources.some((source) => source.id === selected) && <option value={selected}>当前绑定数据源（目录不可见）</option>}{sources.map((source) => <option key={source.id} value={source.id} disabled={!source.enabled}>{source.name} · {source.driver === 'postgresql' ? 'PostgreSQL' : 'MySQL'}{!source.enabled ? '（已停用）' : ''}</option>)}</>
}

export function SchemaStorageEditor({ api, schema, onChange, locked, active, onSources, section = 'all' }: {
  api: ApiClient
  schema: Schema
  onChange: (schema: Schema) => void
  locked: boolean
  active: boolean
  onSources?: () => void
  section?: 'all' | 'storage' | 'mappings'
}) {
  const [sources, setSources] = useState<DataSource[]>([])
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [inspecting, setInspecting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
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
    setTables([])
    if (!active || section === 'mappings' || !binding?.dataSourceId) return
    const controller = new AbortController()
    api.listSourceTables(binding.dataSourceId, binding.namespace, controller.signal).then((items) => {
      if (!controller.signal.aborted) setTables(items)
    }).catch(() => { /* Manual target table entry stays available for forward modeling. */ })
    return () => controller.abort()
  }, [api, active, section, binding?.dataSourceId, binding?.namespace])

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
        <label>主表名称<input className="field" aria-label="主表名称" disabled={locked} list="model-source-tables" value={binding.table} onChange={(event) => updateBinding({ table: event.target.value })} placeholder={schema.mode === 'db_2_schema' ? '选择已有表' : '目标表名称'} /><datalist id="model-source-tables">{tables.map((table) => <option key={`${table.namespace}.${table.name}`} value={table.name}>{table.namespace}.{table.name}</option>)}</datalist></label>
        <label>对象主键列<input className="field" aria-label="对象主键列" disabled={locked} value={binding.idColumn ?? 'id'} onChange={(event) => updateBinding({ idColumn: event.target.value })} placeholder="稳定、唯一且非空的主键列" /></label>
        <label>主表租户列<input className="field" aria-label="主表租户列" disabled={locked} value={binding.tenantColumn ?? ''} onChange={(event) => updateBinding({ tenantColumn: event.target.value || undefined })} placeholder={source?.tenantIsolation === 'column' ? '共享数据源必须指定租户列' : '专属数据源可留空'} /></label>
      </>}
    </div>
    <p className="muted model-storage-explanation">{!binding ? '平台自动维护表和属性列；选择已配置的数据源后，可显式设置表列映射。' : schema.mode === 'schema_2_db' ? '以模型为准：保存草稿不修改数据库，预览后显式发布，创建或安全增加目标表列；已有未纳管表不会被直接覆盖。' : '以数据库为准：读取并校验已有表结构，不创建、修改或删除源表。源列名称需人工补充中文业务名称。'}</p>
    {binding && schema.mode === 'db_2_schema' && !locked && <button type="button" className="button" disabled={inspecting || !binding.table} onClick={() => void importColumns()}>{inspecting ? <LoaderCircle size={15} className="model-spin" /> : <RefreshCw size={15} />}从主表读取属性</button>}
    </>}
    {section === 'mappings' && error && <div className="notice notice-error" role="alert">{error}</div>}
    {section !== 'storage' && binding && <>
      <div className="model-section-heading model-mapping-heading"><div><h3>属性数据源与列映射</h3><p className="muted">其他来源通过唯一关联键匹配主表对象主键，每个对象最多匹配一行。跨来源属性须允许空值；一对多使用关系或子对象。</p></div></div>
      <div className="model-property-mappings">{schema.properties.map((property, index) => <PropertyMappingEditor key={property.id || `mapping-${index}`} api={api} index={index} property={property} binding={binding} sources={sources} locked={locked && !!property.id} onChange={(mapping) => onChange({ ...schema, properties: schema.properties.map((item, offset) => offset === index ? { ...item, mapping } : item) })} />)}</div>
      <p className="muted model-storage-explanation">当前跨来源属性支持投影读取，筛选与排序使用主表属性。绑定对象暂不提供实例写入；多来源结构同步可恢复，不承诺跨库原子提交。</p>
    </>}
  </section>
}

function PropertyMappingEditor({ api, index, property, binding, sources, locked, onChange }: {
  api: ApiClient
  index: number
  property: Property
  binding: StorageBinding
  sources: DataSource[]
  locked: boolean
  onChange: (mapping: PropertyMapping) => void
}) {
  const mapping = property.mapping ?? { dataSourceId: binding.dataSourceId, column: '' }
  const primary = sameTable(mapping, binding)
  const [columns, setColumns] = useState<SourceColumn[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const label = `属性${index + 1}`
  const source = sources.find((item) => item.id === mapping.dataSourceId)
  const selection = JSON.stringify([mapping.dataSourceId, mapping.namespace, mapping.table, binding.namespace, binding.table])
  const selectionRef = useRef(selection)
  selectionRef.current = selection
  useEffect(() => { setColumns([]); setError('') }, [mapping.dataSourceId, mapping.namespace, mapping.table, binding.table, binding.namespace])
  function update(patch: Partial<PropertyMapping>) { onChange({ ...mapping, ...patch }) }
  async function readColumns() {
    setBusy(true); setError('')
    try {
      const table = await api.inspectSourceTable(mapping.dataSourceId, mapping.table || binding.table, mapping.namespace ?? (mapping.dataSourceId === binding.dataSourceId ? binding.namespace : undefined))
      if (selectionRef.current !== selection) return
      setColumns(table.columns)
    } catch (cause) { setError(cause instanceof Error ? cause.message : '列目录读取失败') }
    finally { setBusy(false) }
  }
  return <div className="model-property-mapping">
    <div className="model-mapping-title"><strong>{property.name || `未命名属性 ${index + 1}`}</strong><span className="badge">{primary ? '主表属性' : '关联来源属性'}</span>{!locked && <button type="button" className="button subtle" disabled={busy || !mapping.dataSourceId || !(mapping.table || primary && binding.table)} onClick={() => void readColumns()}>{busy ? <LoaderCircle size={14} className="model-spin" /> : <RefreshCw size={14} />}读取列目录</button>}</div>
    <div className="model-mapping-grid">
      <label>数据源<select className="field" aria-label={`${label}数据源`} disabled={locked} value={mapping.dataSourceId} onChange={(event) => onChange({ dataSourceId: event.target.value, column: '' })}><SourceOptions sources={sources} selected={mapping.dataSourceId} /></select></label>
      <label>命名空间<input className="field" aria-label={`${label}命名空间`} disabled={locked} value={mapping.namespace ?? ''} onChange={(event) => update({ namespace: event.target.value || undefined })} placeholder={mapping.dataSourceId === binding.dataSourceId ? binding.namespace || '使用主表命名空间' : source?.driver === 'postgresql' ? '例如：public' : '使用数据源数据库'} /></label>
      <label>来源表<input className="field" aria-label={`${label}来源表`} disabled={locked} value={mapping.table ?? ''} onChange={(event) => update({ table: event.target.value || undefined })} placeholder={mapping.dataSourceId === binding.dataSourceId ? binding.table || '使用主表' : '填写来源表'} /></label>
      <label>映射列<input className="field" aria-label={`${label}映射列`} list={`model-columns-${index}`} disabled={locked} value={mapping.column} onChange={(event) => update({ column: event.target.value })} placeholder="填写物理列名" /><datalist id={`model-columns-${index}`}>{columns.map((column) => <option key={column.name} value={column.name}>{column.databaseType}</option>)}</datalist></label>
      <label>{primary ? '主键列（继承）' : '唯一关联键列'}<input className="field" aria-label={`${label}关联键列`} disabled={locked || primary} value={primary ? binding.idColumn ?? 'id' : mapping.keyColumn ?? ''} onChange={(event) => update({ keyColumn: event.target.value || undefined })} placeholder="唯一、非空，匹配对象主键" /></label>
      <label>租户列<input className="field" aria-label={`${label}租户列`} disabled={locked || primary} value={primary ? binding.tenantColumn ?? '' : mapping.tenantColumn ?? ''} onChange={(event) => update({ tenantColumn: event.target.value || undefined })} placeholder={source?.tenantIsolation === 'column' ? '共享来源须填写租户列' : '专属来源可留空'} /></label>
    </div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {!primary && !property.nullable && <p className="model-mapping-warning">关联来源可能没有匹配行，请在对象属性中启用“允许空值”。</p>}
  </div>
}
