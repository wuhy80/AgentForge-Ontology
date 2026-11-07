import { useEffect, useState } from 'react'
import { ArrowRight, Database, LoaderCircle } from 'lucide-react'
import type { ApiClient } from '../../lib/api'
import type { DataSource, DefinitionRecord, Link, LinkStorageBinding, SourceColumn, SourceTableInfo } from '../../lib/types'

export function validateLinkStorage(link: Link, schemas: DefinitionRecord[]): string | undefined {
  const binding = link.storageBinding
  if (!binding) return
  const source = schemas.find((entry) => entry.definition.id === link.sourceSchemaId)?.definition.schema
  const target = schemas.find((entry) => entry.definition.id === link.targetSchemaId)?.definition.schema
  // Catalog visibility is independent of runtime access; the server validates
  // existing bindings when an endpoint is absent from this authorized catalog.
  if (source && target) {
    if (binding.mode === 'managed' && (source.storageBinding || target.storageBinding)) return '平台关系表要求两端对象均使用平台默认存储。'
    if (binding.mode !== 'managed' && (!source.storageBinding || !target.storageBinding || source.storageBinding.dataSourceId !== target.storageBinding.dataSourceId || source.storageBinding.dataSourceId !== binding.dataSourceId)) return '外部关系要求两端对象与关系表使用同一个数据源；当前不支持跨数据源 Link 查询。'
  }
  if (binding.mode === 'managed') return
  if (!binding.dataSourceId) return '请先为关系两端配置同一个外部数据源。'
  if (binding.mode === 'junction') {
    if (!binding.table || !binding.sourceColumn || !binding.targetColumn) return '请选择关联表及来源、目标两端的关联列。'
    if (binding.sourceColumn === binding.targetColumn) return '关联表的来源列与目标列必须不同。'
  } else {
    if (!binding.foreignKeyColumn || !binding.foreignKeySide) return '请选择持有外键的一端及外键列。'
    if (binding.foreignKeySide === 'source' && !['1:1', 'N:1'].includes(link.cardinality) || binding.foreignKeySide === 'target' && !['1:1', '1:N'].includes(link.cardinality)) return '外键方向与关系基数不匹配；多对多关系请使用关联表。'
  }
}

export function LinkStorageEditor({ api, link, schemas, locked, active, onChange, onSources }: {
  api: ApiClient
  link: Link
  schemas: DefinitionRecord[]
  locked: boolean
  active: boolean
  onChange: (link: Link) => void
  onSources?: () => void
}) {
  const binding = link.storageBinding
  const source = schemas.find((entry) => entry.definition.id === link.sourceSchemaId)?.definition
  const target = schemas.find((entry) => entry.definition.id === link.targetSchemaId)?.definition
  const sourceStorage = source?.schema?.storageBinding
  const targetStorage = target?.schema?.storageBinding
  const [sources, setSources] = useState<DataSource[]>([])
  const [tables, setTables] = useState<SourceTableInfo[]>([])
  const [columns, setColumns] = useState<SourceColumn[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const foreignStorage = binding?.foreignKeySide === 'target' ? targetStorage : sourceStorage
  const inspectedTable = binding?.mode === 'foreign_key' ? foreignStorage?.table : binding?.table
  const inspectedNamespace = binding?.mode === 'foreign_key' ? foreignStorage?.namespace : binding?.namespace
  const validation = validateLinkStorage(link, schemas)
  const dataSource = sources.find((item) => item.id === binding?.dataSourceId)

  useEffect(() => {
    if (!active || locked || !binding || binding.mode === 'managed') return
    const controller = new AbortController()
    api.listDataSources(controller.signal, true).then((items) => { if (!controller.signal.aborted) setSources(items) })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '数据源目录读取失败') })
    return () => controller.abort()
  }, [api, active, locked, binding?.mode]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    setTables([]); setColumns([]); setError('')
    if (!active || locked || !binding?.dataSourceId || binding.mode === 'managed') return
    const controller = new AbortController()
    setLoading(true)
    const read = binding.mode === 'junction'
      ? api.listSourceTables(binding.dataSourceId, binding.namespace, controller.signal).then((items) => { if (!controller.signal.aborted) setTables(items) })
      : Promise.resolve()
    read.catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '表目录读取失败') })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, active, locked, binding?.mode, binding?.dataSourceId, binding?.namespace])

  useEffect(() => {
    setColumns([])
    if (!active || locked || !binding?.dataSourceId || !inspectedTable) return
    const controller = new AbortController()
    api.inspectSourceTable(binding.dataSourceId, inspectedTable, inspectedNamespace, controller.signal).then((table) => {
      if (!controller.signal.aborted) setColumns(table.columns)
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '列目录读取失败') })
    return () => controller.abort()
  }, [api, active, locked, binding?.dataSourceId, inspectedTable, inspectedNamespace])

  function update(patch: Partial<LinkStorageBinding>) { if (binding) onChange({ ...link, storageBinding: { ...binding, ...patch } }) }
  function chooseMode(mode: string) {
    setError('')
    const dataSourceId = sourceStorage?.dataSourceId === targetStorage?.dataSourceId ? sourceStorage?.dataSourceId : undefined
    onChange({ ...link, storageBinding: !mode ? undefined : mode === 'managed' ? { mode } : mode === 'junction'
      ? { mode, dataSourceId, namespace: sourceStorage?.namespace, table: '', sourceColumn: '', targetColumn: '' }
      : { mode: 'foreign_key', dataSourceId, foreignKeySide: link.cardinality === '1:N' ? 'target' : 'source', foreignKeyColumn: '' } })
  }
  function columnOptions(current?: string) {
    return <><option value="">选择物理列</option>{current && !columns.some((column) => column.name === current) && <option value={current}>{current}</option>}{columns.map((column) => <option key={column.name} value={column.name}>{column.name} · {column.databaseType}</option>)}</>
  }

  return <section className="model-link-storage" aria-label="关系存储映射">
    <div className="model-section-heading"><div><h3><Database size={17} />关系存储与查询</h3><p className="muted">指定关系事实保存在哪里，发布后可在数据探索中正向或反向查询。</p></div>{onSources && <button type="button" className="button subtle" onClick={onSources}>数据源配置<ArrowRight size={14} /></button>}</div>
    <div className="form-grid">
      <label>关系存储方式<select className="field" aria-label="关系存储方式" disabled={locked} value={binding?.mode ?? ''} onChange={(event) => chooseMode(event.target.value)}><option value="">仅维护关系定义</option><option value="managed">平台关联表（可维护关系）</option><option value="junction">映射已有的关联表（只读）</option><option value="foreign_key">映射对象表中的外键（只读）</option></select></label>
      {binding && binding.mode !== 'managed' && <label>关系数据源<input className="field" readOnly value={dataSource?.name ?? (binding.dataSourceId ? '继承两端对象的主数据源' : '请先为两端对象配置同一数据源')} /></label>}
      {binding?.mode === 'junction' && <>
        <label>关联表命名空间<input className="field" disabled={locked} value={binding.namespace ?? ''} onChange={(event) => update({ namespace: event.target.value || undefined, table: '', sourceColumn: '', targetColumn: '' })} placeholder="使用数据源默认命名空间" /></label>
        <label>关联表{loading && <LoaderCircle size={13} className="model-spin" />}<select className="field" aria-label="关联表" disabled={locked || loading || !binding.dataSourceId} value={binding.table ? JSON.stringify([binding.namespace ?? '', binding.table]) : ''} onChange={(event) => {
          const [namespace, table] = event.target.value ? JSON.parse(event.target.value) as string[] : ['', '']
          update({ namespace: namespace || undefined, table, sourceColumn: '', targetColumn: '', tenantColumn: undefined })
        }}><option value="">选择已有表</option>{binding.table && !tables.some((table) => table.name === binding.table && table.namespace === (binding.namespace ?? '')) && <option value={JSON.stringify([binding.namespace ?? '', binding.table])}>{binding.table}</option>}{tables.map((table) => <option key={`${table.namespace}.${table.name}`} value={JSON.stringify([table.namespace, table.name])}>{table.namespace ? `${table.namespace}.` : ''}{table.name}</option>)}</select></label>
        <label>来源对象关联列<select className="field" aria-label="来源对象关联列" disabled={locked || !binding.table} value={binding.sourceColumn ?? ''} onChange={(event) => update({ sourceColumn: event.target.value })}>{columnOptions(binding.sourceColumn)}</select></label>
        <label>目标对象关联列<select className="field" aria-label="目标对象关联列" disabled={locked || !binding.table} value={binding.targetColumn ?? ''} onChange={(event) => update({ targetColumn: event.target.value })}>{columnOptions(binding.targetColumn)}</select></label>
        <label>关联表租户列<select className="field" aria-label="关联表租户列" disabled={locked || !binding.table} value={binding.tenantColumn ?? ''} onChange={(event) => update({ tenantColumn: event.target.value || undefined })}><option value="">专属数据源可留空</option>{binding.tenantColumn && !columns.some((column) => column.name === binding.tenantColumn) && <option value={binding.tenantColumn}>{binding.tenantColumn}</option>}{columns.map((column) => <option key={column.name} value={column.name}>{column.name}</option>)}</select></label>
      </>}
      {binding?.mode === 'foreign_key' && <>
        <label>持有外键的对象<select className="field" aria-label="持有外键的对象" disabled={locked} value={binding.foreignKeySide ?? 'source'} onChange={(event) => update({ foreignKeySide: event.target.value as 'source' | 'target', foreignKeyColumn: '' })}><option value="source">来源对象：{source?.name || '未选择'}</option><option value="target">目标对象：{target?.name || '未选择'}</option></select></label>
        <label>外键所在表<input className="field" readOnly value={foreignStorage ? [foreignStorage.namespace, foreignStorage.table].filter(Boolean).join('.') : '先配置对象主表'} /></label>
        <label>外键列<select className="field" aria-label="外键列" disabled={locked || !foreignStorage} value={binding.foreignKeyColumn ?? ''} onChange={(event) => update({ foreignKeyColumn: event.target.value })}>{columnOptions(binding.foreignKeyColumn)}</select></label>
      </>}
    </div>
    <div className="model-link-diagram" aria-label="关系映射示意"><span><strong>{source?.name || '来源对象'}</strong><small>{sourceStorage ? `${sourceStorage.table}.${sourceStorage.idColumn || 'id'}` : '平台对象主键'}</small></span><ArrowRight size={18} /><span><strong>{!binding ? '尚未连接关系数据' : binding.mode === 'managed' ? '平台关系表' : binding.mode === 'junction' ? binding.table || '选择关联表' : `${binding.foreignKeySide === 'target' ? target?.name || '目标' : source?.name || '来源'}.${binding.foreignKeyColumn || '选择外键列'}`}</strong><small>{binding?.mode === 'junction' ? `${binding.sourceColumn || '来源列'} → ${binding.targetColumn || '目标列'}` : link.forwardName || '正向关系'}</small></span><ArrowRight size={18} /><span><strong>{target?.name || '目标对象'}</strong><small>{targetStorage ? `${targetStorage.table}.${targetStorage.idColumn || 'id'}` : '平台对象主键'}</small></span></div>
    <p className="muted model-storage-explanation">{!binding ? '当前只有关系定义，发布不会创建关系数据，不能查询关联实例。选择存储方式后才能连接实际数据。' : binding.mode === 'managed' ? '发布时由平台创建关系表；在数据探索中添加或移除对象之间的关系。正反向查询共用同一份关系事实，并执行两端对象权限。' : '读取已有数据库关系，不修改源表或关系数据。发布会校验表列、租户隔离及基数所需的唯一约束；当前仅支持同一数据源中的一跳关联。'}</p>
    {binding?.mode === 'foreign_key' && <p className="muted">来源持有外键适用于多对一或一对一；目标持有外键适用于一对多或一对一；多对多请选择关联表。</p>}
    {validation && <div className="notice notice-error" role="alert">{validation}</div>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
  </section>
}
