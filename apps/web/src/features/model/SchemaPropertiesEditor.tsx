import { useEffect, useMemo, useState } from 'react'
import { LoaderCircle, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import type { ApiClient } from '../../lib/api'
import type { DataSource, DataType, Property, Schema, StoragePropertyMapping } from '../../lib/types'
import { newProperty, typeLabels } from './PropertyEditor'
import { createCatalog, PropertyMappingEditor } from './SchemaStorageEditor'

type Editing = { index: number; property: Property; adding: boolean }

export function SchemaPropertiesEditor({ api, schema, onChange, locked, allowAppend, disabled, active, currentId, currentETag, draft, maxProperties }: {
  api: ApiClient
  schema: Schema
  onChange: (schema: Schema) => void
  locked: boolean
  allowAppend: boolean
  disabled: boolean
  active: boolean
  currentId?: string
  currentETag?: string
  draft: boolean
  maxProperties: number
}) {
  const [editing, setEditing] = useState<Editing>()
  const [sources, setSources] = useState<DataSource[]>([])
  const [sourceError, setSourceError] = useState('')
  const catalog = useMemo(() => createCatalog(api), [api, active])
  const binding = schema.storageBinding
  const propertyLocked = !!editing && locked && (!allowAppend || !!editing.property.id)

  useEffect(() => {
    if (!active) { setEditing(undefined); return }
    const controller = new AbortController()
    setSources([]); setSourceError('')
    api.listDataSources(controller.signal, true).then((items) => {
      if (!controller.signal.aborted) setSources(items)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setSourceError(cause instanceof Error ? cause.message : '数据源目录读取失败')
    })
    return () => controller.abort()
  }, [api, active])

  function open(index: number) {
    if (disabled) return
    setEditing({ index, property: structuredClone(schema.properties[index]), adding: false })
  }
  function add() {
    if (disabled || locked && !allowAppend || schema.properties.length >= maxProperties) return
    setEditing({
      index: schema.properties.length,
      adding: true,
      property: { ...newProperty(), nullable: allowAppend, mapping: binding ? { dataSourceId: binding.dataSourceId, column: '' } : undefined },
    })
  }
  function update(patch: Partial<Property>) {
    setEditing((value) => value ? { ...value, property: { ...value.property, ...patch } } : value)
  }
  function apply() {
    if (!editing || disabled) return
    const properties = editing.adding ? [...schema.properties, editing.property] : schema.properties.map((property, index) => index === editing.index ? editing.property : property)
    onChange({ ...schema, properties })
    setEditing(undefined)
  }
  function remove(index: number) {
    if (disabled || locked && (!allowAppend || !!schema.properties[index].id)) return
    const properties = schema.properties.filter((_, position) => position !== index)
    onChange({ ...schema, properties, indexes: (schema.indexes ?? []).filter((item) => item.fieldIds.every((id) => properties.some((property) => property.id === id))) })
  }

  return <section className="model-fields">
    <div className="model-section-heading">
      <div><h3>对象属性</h3><p className="muted">点击每行的编辑按钮，配置属性及其数据源、表和字段映射。</p></div>
      {(!locked || allowAppend) && <button type="button" className="button subtle" disabled={disabled || schema.properties.length >= maxProperties} onClick={add}><Plus size={15} />添加属性</button>}
    </div>
    <p className="muted model-field-boundary">当前 {schema.properties.length} / {maxProperties} 个属性。对象属性暂不支持绑定枚举。</p>
    {sourceError && <div className="notice notice-error" role="alert">{sourceError}</div>}
    {schema.properties.length === 0 ? <div className="model-inline-empty">暂未设置对象属性。</div> : <div className="table-wrap model-property-summary"><table aria-label="对象属性列表">
      <thead><tr><th>业务名称</th><th>数据类型</th><th>约束</th><th>数据源</th><th>表</th><th>字段</th><th>操作</th></tr></thead>
      <tbody>{schema.properties.map((property, index) => {
        const mapping = property.mapping
        const sourceId = mapping?.dataSourceId ?? binding?.dataSourceId
        const source = sources.find((item) => item.id === sourceId)
        const sameSource = sourceId === binding?.dataSourceId
        const namespace = mapping?.namespace ?? (sameSource ? binding?.namespace : undefined) ?? (source?.driver === 'postgresql' ? 'public' : source?.database)
        const table = mapping?.table || (sameSource ? binding?.table : '')
        const location = binding ? [namespace, table].filter(Boolean).join('.') : '自动映射'
        const constraints = [property.required && '必须提供', property.nullable ? '允许空值' : '非空', property.readOnly && '只读'].filter(Boolean).join(' · ')
        const canRemove = !locked || allowAppend && !property.id
        return <tr key={property.id || `new-${index}`}>
          <td title={property.name}><strong>{property.name || `未命名属性 ${index + 1}`}</strong></td>
          <td>{typeLabels[property.dataType] ?? property.dataType}{property.dataType === 'decimal' && <span className="muted"> ({property.precision ?? 18}, {property.scale ?? 0})</span>}</td>
          <td>{constraints}</td>
          <td title={source?.name}>{binding ? source?.name || (sourceId ? '当前绑定数据源' : '待配置') : '平台默认存储'}</td>
          <td title={location}><code>{location || '待配置'}</code></td>
          <td title={mapping?.column}><code>{binding ? mapping?.column || '待配置' : '自动映射'}</code></td>
          <td><div className="model-property-row-actions"><button type="button" className="button subtle" aria-label={`编辑对象属性第${index + 1}项`} disabled={disabled} onClick={() => open(index)}><Pencil size={13} />编辑</button>{canRemove && <button type="button" className="button subtle model-icon-button" aria-label={`删除对象属性第${index + 1}项`} disabled={disabled} onClick={() => remove(index)}><Trash2 size={14} /></button>}</div></td>
        </tr>
      })}</tbody>
    </table></div>}
    {editing && active && <Dialog title={editing.adding ? '添加属性' : '编辑属性'} description="应用修改后更新页面草稿，再通过模型页面保存草稿并预览、生效。" wide onClose={() => setEditing(undefined)} closeDisabled={disabled}>
      <div className="dialog-body model-property-dialog">
        <fieldset className="model-property-dialog-fields" disabled={disabled}>
          <section><h3>基本信息</h3><div className="form-grid">
            <label>业务名称<input className="field" aria-label={`对象属性第${editing.index + 1}项名称`} value={editing.property.name} maxLength={128} placeholder="例如：设备名称" onChange={(event) => update({ name: event.target.value })} /></label>
            <label>数据类型<select className="field" aria-label={`对象属性第${editing.index + 1}项类型`} disabled={propertyLocked} value={editing.property.dataType} onChange={(event) => {
              const dataType = event.target.value as DataType
              update({ dataType, precision: dataType === 'decimal' ? 18 : undefined, scale: dataType === 'decimal' ? 2 : undefined, enumId: undefined, default: undefined })
            }}>{Object.entries(typeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            {editing.property.dataType === 'decimal' && <>
              <label>总位数<input className="field" type="number" aria-label={`对象属性第${editing.index + 1}项总位数`} disabled={propertyLocked} min={1} max={38} value={editing.property.precision ?? 18} onChange={(event) => update({ precision: Number(event.target.value) })} /></label>
              <label>小数位<input className="field" type="number" aria-label={`对象属性第${editing.index + 1}项小数位`} disabled={propertyLocked} min={0} max={editing.property.precision ?? 18} value={editing.property.scale ?? 0} onChange={(event) => update({ scale: Number(event.target.value) })} /></label>
            </>}
          </div>
          <div className="model-property-options model-property-dialog-options">
            <label className="model-checkbox"><input type="checkbox" disabled={propertyLocked || allowAppend} checked={!!editing.property.required} onChange={(event) => update({ required: event.target.checked })} />必须提供</label>
            <label className="model-checkbox"><input type="checkbox" disabled={propertyLocked || allowAppend} checked={!!editing.property.nullable} onChange={(event) => update({ nullable: event.target.checked })} />允许空值</label>
            {editing.property.readOnly && <span className="badge">只读</span>}
            {editing.property.enumId && <span className="model-inline-actions"><span className="muted">当前枚举绑定尚未支持</span>{!propertyLocked && <button type="button" className="button subtle" aria-label={`解除对象属性第${editing.index + 1}项枚举绑定`} onClick={() => update({ enumId: undefined })}>解除绑定</button>}</span>}
          </div>
          {editing.property.dataType === 'longtext' && <p className="muted">最多 4 MiB（UTF-8 字节），不支持索引或排序。</p>}
          {propertyLocked && <p className="muted">属性已生效，可修改业务名称；数据类型、约束和映射的变更需通过迁移处理。</p>}
          {allowAppend && !propertyLocked && <p className="muted">为已生效模型新增属性时，必须允许空值。</p>}
          </section>
          <section><h3>数据源与列映射</h3>{binding ? <PropertyMappingEditor catalog={catalog} active={active} forward={schema.mode === 'schema_2_db'} index={editing.index} property={editing.property} binding={binding} sources={sources} locked={propertyLocked} onChange={(mapping) => update({ mapping })} /> : <p className="muted">当前使用平台自动映射。如需指定数据源、表和字段，请先在基础信息中选择主数据源和主表。</p>}
            {currentId && editing.property.id ? <PropertyStorageStatus key={`${currentId}:${currentETag}:${editing.property.id}`} api={api} schemaId={currentId} currentETag={currentETag} propertyId={editing.property.id} explicit={!!binding} draft={draft} /> : <p className="muted model-storage-explanation">此属性尚未生效，发布后可核对实际数据库字段。</p>}
          </section>
        </fieldset>
      </div>
      <div className="dialog-actions"><button type="button" className="button" disabled={disabled} onClick={() => setEditing(undefined)}>取消</button><button type="button" className="button primary" disabled={disabled} onClick={apply}>{editing.adding ? '添加属性' : '应用修改'}</button></div>
    </Dialog>}
  </section>
}

function PropertyStorageStatus({ api, schemaId, currentETag, propertyId, explicit, draft }: {
  api: ApiClient
  schemaId: string
  currentETag?: string
  propertyId: string
  explicit: boolean
  draft: boolean
}) {
  const [mapping, setMapping] = useState<StoragePropertyMapping>()
  const [loading, setLoading] = useState(false)
  const [checked, setChecked] = useState(false)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setMapping(undefined); setChecked(false); setError(''); setLoading(true)
    api.getSchemaStorageMapping(schemaId, controller.signal).then((result) => {
      if (controller.signal.aborted) return
      if (result.schemaId !== schemaId || currentETag && result.definitionETag !== currentETag.replace(/^"|"$/g, '')) {
        setError('当前定义已更新，请载入最新内容后核对字段。'); return
      }
      setMapping(result.properties.find((property) => property.propertyId === propertyId))
      setChecked(true)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '数据库字段核对失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, schemaId, currentETag, propertyId, refresh])

  return <section className="model-property-storage-status" aria-label="属性实际数据库映射">
    <div className="model-property-storage-heading"><strong>数据库核对</strong><button type="button" className="button subtle" disabled={loading} onClick={() => setRefresh((value) => value + 1)}>{loading ? <LoaderCircle size={14} className="model-spin" /> : <RefreshCw size={14} />}刷新核对</button></div>
    {draft && <p className="muted">按当前生效定义核对；草稿修改发布后才会生效。</p>}
    {loading && <p className="muted" role="status">正在核对数据库字段…</p>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {checked && !mapping && <p className="muted">此属性尚未生效，发布后可核对实际数据库字段。</p>}
    {mapping && <>
      {!explicit && <dl className="model-property-storage-location">
        <div><dt>数据源</dt><dd>{mapping.dataSourceName}</dd></div>
        <div><dt>数据库 / 命名空间</dt><dd><code>{mapping.namespace}</code></dd></div>
        <div><dt>表</dt><dd><code>{mapping.table}</code></dd></div>
        <div><dt>字段</dt><dd><code>{mapping.column}</code></dd></div>
        <div><dt>对象主键</dt><dd><code>{mapping.keyColumn}</code></dd></div>
        {mapping.tenantColumn && <div><dt>租户列</dt><dd><code>{mapping.tenantColumn}</code></dd></div>}
      </dl>}
      <p className="model-property-storage-result"><span>数据库类型：<code>{mapping.databaseType || '—'}</code></span><span className={mapping.exists ? 'badge' : 'model-mapping-warning'}>{mapping.exists ? '字段存在' : '字段缺失'}</span></p>
    </>}
  </section>
}
