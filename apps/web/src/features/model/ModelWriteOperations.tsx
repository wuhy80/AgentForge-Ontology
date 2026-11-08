import { useEffect, useRef, useState } from 'react'
import { LoaderCircle, Plus, RefreshCw, Save, Trash2 } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiError, type ApiClient } from '../../lib/api'
import type { BulkWriteItem, ConflictPolicy, Instance, Property, Values, WriteCapabilities, WriteOperation, WriteResult } from '../../lib/types'
import { ValueInput } from '../query/InstanceDialog'
import { buildWriteValues, errorMessage, initialDrafts, parseFieldValue, valueText, type FieldDrafts } from '../query/query'

export const writeOperationLabels: Record<WriteOperation, string> = {
  create: '创建', update: '更新', delete: '删除', upsert: '创建或更新',
  bulk_create: '批量创建', bulk_update: '批量更新', bulk_delete: '批量删除', bulk_upsert: '批量创建或更新',
}
const operationNames = Object.keys(writeOperationLabels) as WriteOperation[]
const isDenied = (cause: unknown) => cause instanceof ApiError && [401, 403, 404].includes(cause.status)
const unavailableReasons: Record<string, string> = {
  forbidden: '当前账号未获此操作权限', storage_unavailable: '数据存储暂不可用', mapping_unavailable: '数据映射尚未就绪',
  mapped_relation_mutation_unsupported: '此映射对象有关联约束，暂不支持删除',
  mapped_write_scope_unsupported: '此来源的租户隔离配置尚不支持写入',
  mapped_write_key_unsupported: '此来源的记录标识尚不支持写入',
  mapped_write_key_property_unsupported: '此来源的标识属性尚不支持写入',
  mapped_write_columns_unsupported: '此来源的字段映射尚不支持写入',
  mapped_write_storage_unsupported: '此来源的存储方式尚不支持写入',
  mapped_write_effects_unsupported: '此来源的数据库写入行为尚不支持',
  mapped_write_metadata_unavailable: '暂时无法确认此来源的写入条件，请稍后重试',
  mapped_write_metadata_visibility_unsupported: '此来源的数据库目录可见性不足，暂不能确认写入安全条件',
}

export function ModelWriteOperations({ api, schemaId, active, hasCurrent }: {
  api: ApiClient; schemaId: string; active: boolean; hasCurrent: boolean
}) {
  const [capabilities, setCapabilities] = useState<WriteCapabilities>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [reload, setReload] = useState(0)
  const [operation, setOperation] = useState<WriteOperation>()
  const intents = useRef(new Map<string, string>())
  useEffect(() => {
    setCapabilities(undefined); setOperation(undefined); setError(''); setLoading(false)
    if (!active || !hasCurrent) return
    const abort = new AbortController()
    setLoading(true)
    api.writeCapabilities(schemaId, abort.signal).then((value) => {
      if (abort.signal.aborted) return
      if (value.schemaId !== schemaId) throw new Error('写入能力不属于当前对象，请重新读取。')
      setCapabilities(value)
    }).catch((cause: unknown) => { if (!abort.signal.aborted) setError(errorMessage(cause)) })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [api, schemaId, active, hasCurrent, reload])
  if (!active) return null
  return <section className="model-write-operations" aria-label="对象内置写操作">
    <div className="model-section-heading"><div><h3>内置数据操作</h3><p className="muted">操作当前生效对象的数据；批量提交在同一事务内全部成功或全部回滚。</p></div>
      {hasCurrent && <button className="button subtle" type="button" disabled={loading} onClick={() => setReload((value) => value + 1)}><RefreshCw size={15} />刷新操作权限</button>}</div>
    {!hasCurrent ? <p className="model-inline-empty">发布对象后可查看内置操作。</p> : <>
      {loading && <p role="status" className="muted"><LoaderCircle size={15} className="model-spin" />正在检查写入能力…</p>}
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <div className="model-write-grid">{operationNames.map((name) => {
        const capability = capabilities?.operations.find((item) => item.name === name)
        return <article key={name}><div><strong>{writeOperationLabels[name]}</strong><code>{name}</code></div>
          <p className="muted">{capability?.allowed ? name.startsWith('bulk_') ? `同一批次最多 ${capabilities?.maxBulkItems} 条` : '当前可用，提交时重新校验' : unavailableReasons[capability?.reason ?? ''] ?? (loading ? '检查中' : '当前不可用，请检查操作权限与来源状态')}</p>
          <button className="button" type="button" disabled={!capability?.allowed} onClick={() => setOperation(name)}>{writeOperationLabels[name]}数据</button></article>
      })}</div>
    </>}
    {operation && capabilities && <WriteOperationDialog key={`${schemaId}:${operation}`} api={api} capabilities={capabilities} operation={operation} intents={intents.current}
      onClose={() => setOperation(undefined)} onDenied={(message) => { setCapabilities(undefined); setOperation(undefined); setError(message) }} />}
  </section>
}

interface WriteRow { key: string; id: string; record?: Instance; drafts: FieldDrafts; matchValues: Record<string, string> }
function newRow(fields: Property[]): WriteRow { return { key: crypto.randomUUID(), id: '', drafts: initialDrafts(fields, {}), matchValues: {} } }
function quotedETag(etag: string): string {
  if (!etag || /["\s\x00-\x1f\x7f]/.test(etag) || etag.startsWith('W/')) throw new Error('记录缺少有效的并发凭证，请重新读取。')
  return `"${etag}"`
}

export function WriteOperationDialog({ api, capabilities, operation, intents, onClose, onDenied }: {
  api: ApiClient; capabilities: WriteCapabilities; operation: WriteOperation; intents: Map<string, string>
  onClose: () => void; onDenied: (message: string) => void
}) {
  const bulk = operation.startsWith('bulk_')
  const base = operation.replace('bulk_', '') as 'create' | 'update' | 'delete' | 'upsert'
  const capability = capabilities.operations.find((item) => item.name === operation)
  const [policy, setPolicy] = useState<ConflictPolicy>('check')
  const canOverwrite = capabilities.overwriteAllowed && Array.isArray(capabilities.overwriteWriteFields) && Array.isArray(capabilities.overwriteUniqueKeyIds)
  const fields = capabilities.fields.filter((field) => !field.readOnly && (capability?.writeFields.includes('*') || capability?.writeFields.includes(field.id)) &&
    (policy !== 'overwrite' || capabilities.overwriteWriteFields?.includes('*') || capabilities.overwriteWriteFields?.includes(field.id)))
  const [rows, setRows] = useState<WriteRow[]>(() => [newRow(fields)])
  const [matchMode, setMatchMode] = useState(capabilities.matchModes.includes('id') ? 'id' : capabilities.uniqueKeys[0]?.id ?? '')
  const [busy, setBusy] = useState(false)
  const [reading, setReading] = useState('')
  const [error, setError] = useState('')
  const [results, setResults] = useState<WriteResult[]>()
  const [uncertain, setUncertain] = useState(false)
  const [confirmed, setConfirmed] = useState(false)
  const locked = busy || uncertain
  const request = useRef<AbortController | undefined>(undefined)
  useEffect(() => () => request.current?.abort(), [])
  const uniqueKeys = capabilities.uniqueKeys.filter((key) => policy !== 'overwrite' || capabilities.overwriteUniqueKeyIds?.includes(key.id))
  const uniqueKey = uniqueKeys.find((item) => item.id === matchMode)
  const matchFields = uniqueKey?.fieldIds.map((id) => capabilities.fields.find((field) => field.id === id)).filter((field): field is Property => !!field) ?? []
  function patchRow(key: string, patch: Partial<WriteRow>) {
    setRows((current) => current.map((row) => row.key === key ? { ...row, ...patch } : row)); setError(''); setResults(undefined); setConfirmed(false)
  }
  async function readRow(row: WriteRow) {
    if (!row.id || busy || reading) return
    const abort = new AbortController(); request.current = abort; setReading(row.key); setError('')
    try {
      const record = await api.getInstance(capabilities.schemaId, row.id, abort.signal)
      if (abort.signal.aborted) return
      if (record.schemaId !== capabilities.schemaId || record.id !== row.id) throw new Error('读取结果与所选对象不一致。')
      patchRow(row.key, { record, drafts: initialDrafts(fields, record.values) })
    } catch (cause) {
      if (!abort.signal.aborted) { if (isDenied(cause)) onDenied(errorMessage(cause)); else setError(errorMessage(cause)) }
    } finally { if (!abort.signal.aborted) setReading('') }
  }
  async function submit() {
    if (busy || reading || !capability?.allowed || base === 'delete' && !confirmed) return
    setError(''); setResults(undefined)
    let items: BulkWriteItem[]
    try {
      items = rows.map((row) => {
        const values = base === 'delete' ? undefined : buildWriteValues(fields, row.drafts, row.record?.values ?? {})
        if (base === 'update' && (!values || !Object.keys(values).length)) throw new Error('请为每条更新记录修改至少一个字段。')
        if (base === 'update' || base === 'delete') {
          if (!row.record || row.record.id !== row.id) throw new Error('请先读取每条目标记录，再基于已读取的内容提交。')
          return { clientKey: row.key, id: row.id, ifMatch: quotedETag(row.record.etag), ...(values ? { values } : {}) }
        }
        if (base === 'upsert') {
          if (matchMode === 'id' && !row.id) throw new Error('请填写实例 ID，或生成一个新 ID。')
          const matchValues: Values = {}
          for (const field of matchFields) {
            if (!Object.hasOwn(row.matchValues, field.id)) throw new Error(`请填写匹配键：${field.name}`)
            matchValues[field.id] = parseFieldValue(field, row.matchValues[field.id])
          }
          const match = matchMode === 'id' ? { id: row.id } : { indexId: uniqueKey?.id, values: matchValues }
          if (matchMode !== 'id' && (!uniqueKey || matchFields.length !== uniqueKey.fieldIds.length)) throw new Error('匹配键已失效，请刷新操作权限。')
          return { clientKey: row.key, match, values, ...(policy === 'check' && row.record ? { ifMatch: quotedETag(row.record.etag) } : {}) }
        }
        return { clientKey: row.key, values }
      })
    } catch (cause) { setError(errorMessage(cause)); return }
    const signature = JSON.stringify({ schemaId: capabilities.schemaId, operation, policy, items })
    let key = intents.get(signature)
    if (!key) { key = crypto.randomUUID(); intents.set(signature, key) }
    const abort = new AbortController(); request.current = abort; setBusy(true)
    try {
      let saved: WriteResult[]
      if (bulk) {
        const result = await api.bulkWrite(capabilities.schemaId, operation as `bulk_${'create' | 'update' | 'delete' | 'upsert'}`, {
          atomic: true, idempotencyKey: key, items, ...(base === 'upsert' ? { conflictPolicy: policy } : {}),
        }, abort.signal)
        const keys = new Set(items.map((item) => item.clientKey))
        if (result.schemaId !== capabilities.schemaId || !result.atomic || !result.committed || !Array.isArray(result.results) || result.results.length !== rows.length ||
          result.results.some((item) => !item.clientKey || !keys.delete(item.clientKey)) || keys.size) throw new ApiError(502, 'invalid_response', '批量提交结果无法核对，请保留本次内容重试。')
        saved = result.results
      } else if (base === 'upsert') {
        const item = items[0]
        saved = [await api.upsertInstance(capabilities.schemaId, { match: item.match!, values: item.values!, conflictPolicy: policy, idempotencyKey: key }, item.ifMatch, abort.signal)]
      } else if (base === 'create') {
        const result = await api.createInstance(capabilities.schemaId, items[0].values!, key)
        saved = [{ ...result, outcome: 'created' }]
      } else if (base === 'update') {
        const result = await api.patchInstance(capabilities.schemaId, items[0].id!, items[0].values!, items[0].ifMatch!)
        saved = [{ ...result, outcome: 'updated' }]
      } else {
        await api.deleteInstance(capabilities.schemaId, items[0].id!, items[0].ifMatch!)
        saved = [{ schemaId: capabilities.schemaId, id: items[0].id!, outcome: 'deleted' }]
      }
      if (abort.signal.aborted) return
      if (saved.some((item) => item.schemaId !== capabilities.schemaId || !item.id || !['created', 'updated', 'deleted'].includes(item.outcome) ||
        (base === 'create' ? item.outcome !== 'created' : base === 'update' ? item.outcome !== 'updated' : base === 'delete' ? item.outcome !== 'deleted' : item.outcome === 'deleted'))) {
        throw new ApiError(502, 'invalid_response', '提交结果无法核对，请保留本次内容重试。')
      }
      intents.delete(signature)
      setResults(saved); setUncertain(false)
    } catch (cause) {
      if (abort.signal.aborted) return
      if (isDenied(cause)) { setRows([]); onDenied(errorMessage(cause)); return }
      setError(errorMessage(cause)); setUncertain(!(cause instanceof ApiError) || cause.status === 0 || cause.status >= 500 || cause.status === 408)
    } finally { if (!abort.signal.aborted) setBusy(false) }
  }
  return <Dialog title={`${writeOperationLabels[operation]}数据`} description={bulk ? '本批所有记录一起提交；任何一条失败都会回滚整批。' : '按当前生效模型和当前权限提交。'} onClose={onClose} closeDisabled={locked} wide>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {uncertain && <div className="notice" role="status">{!bulk && (base === 'update' || base === 'delete')
      ? '提交结果尚未确认。重试会使用原并发凭证；若提示记录已变化或不存在，请重新读取并确认实际结果。'
      : '提交结果尚未确认。请保持此窗口并重试；重试会复用本次幂等键和内容，避免重复写入。'}</div>}
    {base === 'upsert' && <div className="form-grid">
      <label>匹配方式<select className="field" value={matchMode} disabled={locked || !!reading} onChange={(event) => { setMatchMode(event.target.value); setRows([newRow(fields)]); setResults(undefined) }}>
        {capabilities.matchModes.includes('id') && <option value="id">实例 ID</option>}{uniqueKeys.map((key) => <option key={key.id} value={key.id}>{key.name}</option>)}
      </select></label>
      <label>并发策略<select className="field" value={policy} disabled={locked || !!reading} onChange={(event) => {
        const next = event.target.value as ConflictPolicy
        setPolicy(next); setResults(undefined); setRows([newRow(fields)])
        if (next === 'overwrite' && matchMode !== 'id' && !capabilities.overwriteUniqueKeyIds?.includes(matchMode)) setMatchMode('id')
      }}><option value="check">校验已读取内容</option>{canOverwrite && <option value="overwrite">同步覆盖提交字段</option>}</select></label>
      <p className="muted">校验模式中，新对象可直接创建；已有对象需要先读取。同步覆盖仅修改本次提交的字段。</p>
    </div>}
    {!results && <>
      <div className="model-write-rows">{rows.map((row, index) => <section key={row.key} className="model-write-row" aria-label={`写入记录 ${index + 1}`}>
        <div className="model-section-heading"><strong>{bulk ? `第 ${index + 1} 条` : '记录内容'}</strong>{bulk && rows.length > 1 && <button type="button" className="button subtle" disabled={locked || !!reading} onClick={() => setRows((current) => current.filter((item) => item.key !== row.key))}><Trash2 size={14} />移除此条</button>}</div>
        {(base === 'update' || base === 'delete' || base === 'upsert') && <div className="model-write-target"><label>{base === 'upsert' && uniqueKey ? '已有实例 ID（校验更新时填写）' : '实例 ID'}<input className="field" value={row.id} disabled={locked || !!reading} onChange={(event) => patchRow(row.key, { id: event.target.value, record: undefined, drafts: initialDrafts(fields, {}) })} /></label>
          <button type="button" className="button" disabled={locked || !!reading || !row.id} onClick={() => void readRow(row)}>{reading === row.key ? '正在读取…' : '读取记录'}</button>
          {base === 'upsert' && !uniqueKey && <button type="button" className="button subtle" disabled={locked || !!reading} onClick={() => patchRow(row.key, { id: crypto.randomUUID(), record: undefined })}>生成新 ID</button>}
        </div>}
        {row.record && <p className="muted">已读取当前记录，提交时将校验此内容是否被修改。</p>}
        {base === 'upsert' && uniqueKey && <fieldset><legend>匹配键</legend><p className="muted">更新已有记录时，填写匹配键后通过上方已有实例 ID 读取内容。服务端会校验匹配目标与并发凭证。</p><div className="form-grid">{matchFields.map((field) => <label key={field.id}>{field.name}<ValueInput property={field} id={`${row.key}-match-${field.id}`} value={row.matchValues[field.id] ?? ''} disabled={locked || !!reading} onChange={(text) => patchRow(row.key, { matchValues: { ...row.matchValues, [field.id]: text }, record: undefined })} /></label>)}</div></fieldset>}
        {base !== 'delete' && <div className="query-instance-fields">{fields.map((field) => {
          const draft = row.drafts[field.id] ?? { mode: 'unchanged', text: '' }
          return <div className="query-instance-field" key={field.id}><div className="query-field-heading"><label htmlFor={`${row.key}-${field.id}`}>{field.name}</label><span className="badge">{field.dataType}</span></div>
            {row.record && <small className="muted">当前值：{valueText(row.record.values[field.id])}</small>}
            <select className="field" aria-label={`${field.name}的写入方式`} value={draft.mode} disabled={locked || !!reading} onChange={(event) => patchRow(row.key, { drafts: { ...row.drafts, [field.id]: { ...draft, mode: event.target.value as FieldDrafts[string]['mode'] } } })}>
              <option value="unchanged">{row.record ? '保留当前值' : '不提交，由服务端校验默认值'}</option><option value="value">填写值</option>{field.nullable && <option value="null">设为空值 NULL</option>}
            </select>
            {draft.mode === 'value' && <ValueInput property={field} id={`${row.key}-${field.id}`} value={draft.text} disabled={locked || !!reading} onChange={(text) => patchRow(row.key, { drafts: { ...row.drafts, [field.id]: { mode: 'value', text } } })} />}
          </div>
        })}</div>}
      </section>)}</div>
      {bulk && <button className="button" type="button" disabled={locked || !!reading || rows.length >= capabilities.maxBulkItems} onClick={() => setRows((current) => [...current, newRow(fields)])}><Plus size={15} />添加记录（{rows.length}/{capabilities.maxBulkItems}）</button>}
      {base === 'delete' && <label className="model-checkbox"><input type="checkbox" checked={confirmed} disabled={locked} onChange={(event) => setConfirmed(event.target.checked)} />确认删除以上{rows.length}条记录</label>}
      <div className="query-dialog-footer"><span className="muted">int64 与 decimal 保留完整字符串精度。</span><button className="button primary" type="button" disabled={busy || !!reading || !rows.length || base === 'delete' && !confirmed} onClick={() => void submit()}>{busy ? <LoaderCircle className="model-spin" size={16} /> : <Save size={16} />}{busy ? '正在提交…' : bulk ? '原子提交整批' : '提交'}</button></div>
    </>}
    {results && <div className="notice notice-success" role="status"><p>{bulk ? '整批已提交' : '操作已完成'}，共 {results.length} 条。</p><ul>{results.map((result, index) => <li key={result.clientKey ?? index}>{({ created: '已创建', updated: '已更新', deleted: '已删除' })[result.outcome]} <code>{result.id}</code></li>)}</ul><button type="button" className="button" onClick={onClose}>完成</button></div>}
  </Dialog>
}
