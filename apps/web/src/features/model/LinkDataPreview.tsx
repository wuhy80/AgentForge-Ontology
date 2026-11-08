import { useEffect, useRef, useState } from 'react'
import { ArrowRight, LoaderCircle, RefreshCw } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Property, QueryResult, QuerySpec } from '../../lib/types'
import { valueText } from '../query/query'

export function linkPreviewFields(properties: Property[]) {
  const ordinary = properties.filter((property) => property.dataType !== 'longtext')
  const preferred = ordinary.filter((property) => /标题|名称|姓名|^(name|title)$/i.test(property.name))
  return [...preferred, ...ordinary.filter((property) => !preferred.includes(property))].slice(0, 3).map((property) => property.id)
}

function failureText(cause: unknown) {
  const message = cause instanceof Error ? cause.message : '关系数据读取失败。'
  return cause instanceof ApiError && cause.status === 403
    ? `${message}。查询需要此关系及两端对象的访问权限。`
    : cause instanceof ApiError && cause.code === 'invalid_cursor' ? `${message}。游标已失效，请从第一页重新读取。` : message
}

export function LinkDataPreview({ api, current, schemas, draft }: {
  api: ApiClient; current?: DefinitionRecord; schemas: DefinitionRecord[]; draft: boolean
}) {
  const link = current?.definition.link
  if (!current || current.status !== 'active' || !link) return <div className="notice">此关系尚未生效。保存并发布定义后才能预览真实关联数据。</div>
  if (!link.storageBinding) return <div className="notice">当前只有关系定义；配置关系存储并发布后，才能预览真实数据。</div>
  const source = schemas.find((item) => item.status === 'active' && item.definition.tenantId === current.definition.tenantId && item.definition.id === link.sourceSchemaId && item.definition.schema)
  const target = schemas.find((item) => item.status === 'active' && item.definition.tenantId === current.definition.tenantId && item.definition.id === link.targetSchemaId && item.definition.schema)
  if (!source || !target) return <div className="notice">关系端点的当前定义不可见，无法在此预览数据。可见定义不代表拥有实例查询权限。</div>
  return <div className="link-data-preview">
    {draft && <div className="notice">草稿尚未发布；此处查询当前生效关系和对象的数据。</div>}
    <p className="muted">{link.storageBinding.mode === 'managed' ? '此处只读预览。实际关联维护可在数据探索中完成。' : '此外部关系只读，预览不会修改外键或源表。'}</p>
    <LinkPreviewScope key={`${current.definition.id}:${current.etag}:${source.etag}:${target.etag}`} api={api} current={current} source={source} target={target} />
  </div>
}

function LinkPreviewScope({ api, current, source, target }: { api: ApiClient; current: DefinitionRecord; source: DefinitionRecord; target: DefinitionRecord }) {
  const [direction, setDirection] = useState<'out' | 'in'>('out')
  const link = current.definition.link!
  return <>
    <label>查询方向<select className="field" value={direction} onChange={(event) => setDirection(event.target.value as 'out' | 'in')}>
      <option value="out">正向：{source.definition.name} → {link.forwardName} → {target.definition.name}</option>
      <option value="in">反向：{target.definition.name} → {link.reverseName} → {source.definition.name}</option>
    </select></label>
    <LinkDirectionPreview key={direction} api={api} current={current} direction={direction} origin={direction === 'out' ? source : target} destination={direction === 'out' ? target : source} />
  </>
}

function LinkDirectionPreview({ api, current, direction, origin, destination }: {
  api: ApiClient; current: DefinitionRecord; direction: 'out' | 'in'; origin: DefinitionRecord; destination: DefinitionRecord
}) {
  const [starts, setStarts] = useState<QueryResult>()
  const [selectedId, setSelectedId] = useState('')
  const [fields, setFields] = useState(() => linkPreviewFields(destination.definition.schema!.properties))
  const [result, setResult] = useState<QueryResult>()
  const [originPage, setOriginPage] = useState(1)
  const [resultPage, setResultPage] = useState(1)
  const [busy, setBusy] = useState<'origin' | 'result' | ''>('')
  const [error, setError] = useState('')
  const [stale, setStale] = useState(false)
  const originRequest = useRef<AbortController | undefined>(undefined)
  const resultRequest = useRef<AbortController | undefined>(undefined)
  useEffect(() => () => { originRequest.current?.abort(); resultRequest.current?.abort() }, [])
  const originFields = linkPreviewFields(origin.definition.schema!.properties)
  function cancel() {
    originRequest.current?.abort(); resultRequest.current?.abort(); setBusy('')
  }
  function clearResult() { resultRequest.current?.abort(); setResult(undefined); setResultPage(1); setError(''); setStale(false); setBusy('') }
  async function loadStarts(cursor?: string) {
    cancel()
    const controller = new AbortController(); originRequest.current = controller
    setBusy('origin'); setError(''); setSelectedId(''); setResult(undefined); setResultPage(1); setStale(false)
    if (!cursor) { setStarts(undefined); setOriginPage(1) }
    try {
      const response = await api.query({ from: { schemaId: origin.definition.id }, select: originFields, page: { limit: 10, ...(cursor ? { cursor } : {}) } }, controller.signal)
      if (controller.signal.aborted) return
      setStarts(response); setOriginPage((page) => cursor ? page + 1 : 1)
    } catch (cause) {
      if (!controller.signal.aborted) { setError(failureText(cause)); setStarts(undefined) }
    } finally { if (!controller.signal.aborted) setBusy('') }
  }
  async function run(cursor?: string) {
    if (!selectedId || !fields.length) return
    cancel()
    const controller = new AbortController(); resultRequest.current = controller
    setBusy('result'); setError('')
    if (!cursor) { setResult(undefined); setResultPage(1) }
    const query: QuerySpec = {
      from: { schemaId: destination.definition.id },
      via: { linkId: current.definition.id, direction, instanceId: selectedId },
      select: [...fields], page: { limit: 5, ...(cursor ? { cursor } : {}) },
    }
    try {
      const response = await api.query(query, controller.signal)
      if (controller.signal.aborted) return
      setResult(response); setResultPage((page) => cursor ? page + 1 : 1); setStale(false)
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(failureText(cause))
      if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setStarts(undefined); setSelectedId(''); setResult(undefined) }
      if (cause instanceof ApiError && cause.code === 'invalid_cursor') { setStale(true); setResult(undefined) }
    } finally { if (!controller.signal.aborted) setBusy('') }
  }
  function selectStart(id: string) { cancel(); clearResult(); setSelectedId(id) }
  function selectField(id: string, checked: boolean) { cancel(); clearResult(); setFields((items) => checked ? [...items, id] : items.filter((item) => item !== id)) }
  return <>
    <div className="link-preview-stage">
      <strong>1. 选择{origin.definition.name}记录</strong>
      <div className="model-preview-toolbar"><button type="button" className="button" disabled={!!busy || !originFields.length} onClick={() => void loadStarts()}><RefreshCw size={14} />读取起点记录</button>
        {starts && <><span className="muted">起点第 {originPage} 页 · 本页 {starts.data.length} 条</span><button type="button" className="button subtle" disabled={!!busy || !starts.hasMore || !starts.nextCursor} onClick={() => void loadStarts(starts.nextCursor)}>起点下一页<ArrowRight size={14} /></button></>}
      </div>
      {!originFields.length && <p className="muted">起点没有可用于预览的普通字段。</p>}
      {starts && <label>起点记录<select className="field" value={selectedId} disabled={!!busy} onChange={(event) => selectStart(event.target.value)}><option value="">选择一条起点记录</option>{starts.data.map((item) => <option key={item.id} value={item.id}>{starts.fields.map((field) => valueText(item.values[field.id])).filter(Boolean).join(' · ') || '未命名记录'} · {item.id}</option>)}</select></label>}
      {starts && !starts.data.length && <p className="muted">没有可见的起点记录。</p>}
    </div>
    <div className="link-preview-stage"><strong>2. 查询关联的{destination.definition.name}</strong>
      <details className="model-preview-fields"><summary>关联返回字段（已选 {fields.length} 项）</summary><div>{destination.definition.schema!.properties.map((property) => <label key={property.id}><input type="checkbox" aria-label={`关联返回字段：${property.name}`} checked={fields.includes(property.id)} disabled={!!busy} onChange={(event) => selectField(property.id, event.target.checked)} />{property.name}</label>)}</div></details>
      <div className="model-preview-toolbar"><button type="button" className="button primary" disabled={!!busy || !selectedId || !fields.length} onClick={() => void run()}>查询关联记录</button>{busy && <><span role="status"><LoaderCircle size={14} className="model-spin" />正在读取{busy === 'origin' ? '起点' : '关联'}记录…</span><button type="button" className="button subtle" onClick={cancel}>取消读取</button></>}</div>
      {stale && <p className="muted">请重新查询关联记录，旧游标已失效。</p>}
      {result && <>
        <div className="table-wrap model-preview-result-table"><table aria-label="关系数据预览"><thead><tr><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}</th>)}</tr></thead><tbody>{result.data.map((item) => <tr key={item.id}><td><code>{item.id}</code></td>{result.fields.map((field) => <td key={field.id}><span className="model-preview-cell">{valueText(item.values[field.id])}</span></td>)}</tr>)}</tbody></table></div>
        {!result.data.length && <p className="muted">此起点没有可见的关联记录。</p>}
        <div className="model-preview-toolbar"><span className="muted">关联第 {resultPage} 页 · 本页 {result.data.length} 条</span><button type="button" className="button subtle" disabled={!!busy || stale || !result.hasMore || !result.nextCursor} onClick={() => void run(result.nextCursor)}>关联下一页<ArrowRight size={14} /></button></div>
      </>}
    </div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
  </>
}
