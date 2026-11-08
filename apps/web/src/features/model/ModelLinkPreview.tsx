import { useEffect, useRef, useState } from 'react'
import { ArrowRight, Link2, LoaderCircle, RefreshCw } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Property, QueryResult, QuerySpec, SchemaLink } from '../../lib/types'
import { errorMessage, propertyValueText } from '../query/query'

function previewFields(properties: Property[]) {
  const ordinary = properties.filter((property) => property.dataType !== 'longtext')
  const preferred = ordinary.filter((property) => /标题|名称|姓名|^(name|title)$/i.test(property.name))
  return [...preferred, ...ordinary.filter((property) => !preferred.includes(property))].slice(0, 3).map((property) => property.id)
}

function failureText(cause: unknown) {
  const message = errorMessage(cause)
  return cause instanceof ApiError && cause.status === 403
    ? `${message}。需要此 Link 的查询权限及两端对象的实例访问权限；也可调整返回字段后重试。`
    : message
}

export function ModelLinkPreview({ api, schemaId, instanceId, active }: {
  api: ApiClient; schemaId: string; instanceId?: string; active: boolean
}) {
  return <section className="model-preview-links" aria-label="关联 Link 数据预览">
    <div className="model-section-heading"><div><h3><Link2 size={17} />关联 Link 数据</h3><p className="muted">预览所选记录正向和反向关联的实际数据。</p></div></div>
    {!active ? null : !instanceId ? <p className="model-inline-empty">选择上方记录后，在这里预览它的关联数据。</p>
      : <LinkPreviewScope key={`${schemaId}:${instanceId}`} api={api} schemaId={schemaId} instanceId={instanceId} />}
  </section>
}

function LinkPreviewScope({ api, schemaId, instanceId }: { api: ApiClient; schemaId: string; instanceId: string }) {
  const [links, setLinks] = useState<SchemaLink[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  const [visible, setVisible] = useState(4)
  useEffect(() => {
    const controller = new AbortController()
    setLinks([]); setError(''); setLoading(true); setVisible(4)
    api.schemaLinks(schemaId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setLinks(items.filter((item) => item.sourceSchemaId === schemaId))
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setError(failureText(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, schemaId, refresh])
  return <>
    <div className="model-preview-toolbar"><span className="muted">当前记录：<code>{instanceId}</code></span><button type="button" className="button subtle" disabled={loading} onClick={() => setRefresh((value) => value + 1)}><RefreshCw size={14} />刷新关联目录</button></div>
    {loading && <p className="muted" role="status"><LoaderCircle size={15} className="model-spin" />正在读取关联关系…</p>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {!loading && !error && !links.length && <p className="model-inline-empty">当前对象没有可见的生效 Link。</p>}
    {links.slice(0, visible).map((navigation) => <LinkPreviewCard key={`${navigation.linkRef.id}:${navigation.direction}:${refresh}`} api={api} schemaId={schemaId} navigation={navigation} instanceId={instanceId} />)}
    {visible < links.length && <button type="button" className="button" onClick={() => setVisible((count) => count + 4)}>显示更多关系（剩余 {links.length - visible} 项）</button>}
  </>
}

function LinkPreviewCard({ api, schemaId, navigation, instanceId }: {
  api: ApiClient; schemaId: string; navigation: SchemaLink; instanceId: string
}) {
  const [model, setModel] = useState<{ link: DefinitionRecord; target: DefinitionRecord }>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setModel(undefined); setError(''); setLoading(true)
    Promise.all([api.getCurrent(navigation.linkRef.id, controller.signal), api.getCurrent(navigation.targetSchemaId, controller.signal)]).then(([link, target]) => {
      if (controller.signal.aborted) return
      const relation = link.definition.link
      const originId = navigation.direction === 'out' ? relation?.sourceSchemaId : relation?.targetSchemaId
      const targetId = navigation.direction === 'out' ? relation?.targetSchemaId : relation?.sourceSchemaId
      if (link.status !== 'active' || target.status !== 'active' || !target.definition.schema || !relation
        || link.definition.id !== navigation.linkRef.id || target.definition.id !== navigation.targetSchemaId
        || originId !== schemaId || targetId !== navigation.targetSchemaId || link.definition.tenantId !== target.definition.tenantId) {
        setError('关系定义或对象已改变，请刷新关联目录后重试。'); return
      }
      setModel({ link, target })
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setError(failureText(cause)) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, schemaId, navigation.linkRef.id, navigation.targetSchemaId, navigation.direction, refresh])
  return <section className="model-preview-link-card" aria-label={`关联预览：${navigation.name}（${navigation.direction === 'out' ? '正向' : '反向'}）`}>
    <div className="model-preview-link-heading"><strong>{navigation.name}</strong><ArrowRight size={14} /><span>{model?.target.definition.name ?? '关联对象'}</span><span className="badge">{navigation.direction === 'out' ? '正向' : '反向'} · {navigation.cardinality}</span></div>
    {loading && <p className="muted" role="status">正在读取关系映射…</p>}
    {error && <div className="notice notice-error" role="alert">{error}<button type="button" className="button subtle" onClick={() => setRefresh((value) => value + 1)}>重新读取关系</button></div>}
    {model && (!model.link.definition.link!.storageBinding ? <div className="notice"><strong>尚未配置关系数据映射</strong><p>此 Link 当前只有定义，配置并发布关联表或外键映射后才能预览真实关联数据。</p><a href={`#models/${encodeURIComponent(navigation.linkRef.id)}`}>配置“{model.link.definition.name}”</a></div>
      : <LinkPreviewQuery key={`${model.link.etag}:${model.target.etag}`} api={api} navigation={navigation} instanceId={instanceId} target={model.target} />)}
  </section>
}

function LinkPreviewQuery({ api, navigation, instanceId, target }: {
  api: ApiClient; navigation: SchemaLink; instanceId: string; target: DefinitionRecord
}) {
  const properties = target.definition.schema!.properties
  const [selected, setSelected] = useState(() => previewFields(properties))
  const [result, setResult] = useState<QueryResult>()
  const [page, setPage] = useState(1)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [stale, setStale] = useState(false)
  const request = useRef<AbortController | undefined>(undefined)
  function cancel() { request.current?.abort(); setBusy(false) }
  async function run(cursor?: string) {
    cancel()
    const controller = new AbortController(); request.current = controller
    setBusy(true); setError('')
    if (!cursor) { setResult(undefined); setPage(1) }
    const spec: QuerySpec = { from: { schemaId: target.definition.id }, via: { linkId: navigation.linkRef.id, direction: navigation.direction, instanceId }, select: [...selected], page: { limit: 5, ...(cursor ? { cursor } : {}) } }
    try {
      const data = await api.query(spec, controller.signal)
      if (!controller.signal.aborted) { setResult(data); setPage((value) => cursor ? value + 1 : 1); setStale(false) }
    } catch (cause) {
      if (controller.signal.aborted) return
      setError(failureText(cause))
      if (cause instanceof ApiError && [401, 403].includes(cause.status)) setResult(undefined)
      if (cause instanceof ApiError && cause.code === 'invalid_cursor') setStale(true)
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  useEffect(() => {
    if (selected.length) void run()
    return () => request.current?.abort()
    // Each card is mounted for a fixed current target and navigation scope.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  function changeField(id: string, checked: boolean) {
    cancel(); setResult(undefined); setError(''); setStale(true)
    setSelected((fields) => checked ? [...fields, id] : fields.filter((field) => field !== id))
  }
  return <>
    <details className="model-preview-fields"><summary>关联返回字段（已选 {selected.length} 项）</summary><div>{properties.map((property) => <label key={property.id}><input type="checkbox" aria-label={`${navigation.name}返回字段：${property.name}`} checked={selected.includes(property.id)} onChange={(event) => changeField(property.id, event.target.checked)} />{property.name}</label>)}</div></details>
    <div className="model-preview-toolbar"><button type="button" className="button subtle" disabled={busy || !selected.length} onClick={() => void run()}><RefreshCw size={14} className={busy ? 'model-spin' : undefined} />{stale ? '应用关联字段' : '刷新关联数据'}</button>{busy && <><span className="muted" role="status">正在读取关联数据…</span><button type="button" className="button subtle" onClick={cancel}>取消关联预览</button></>}</div>
    {!selected.length && <p className="muted">请选择至少一个关联返回字段。</p>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {result && <>
      <div className="table-wrap model-preview-result-table"><table aria-label={`${navigation.name}关联数据`}><thead><tr><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}</th>)}</tr></thead><tbody>{result.data.map((instance) => <tr key={instance.id}><td><code>{instance.id}</code></td>{result.fields.map((field) => <td key={field.id}><span className="model-preview-cell">{propertyValueText(field, instance.values[field.id])}</span></td>)}</tr>)}</tbody></table></div>
      {!result.data.length && <p className="muted">此记录没有可见的关联数据。</p>}
      <div className="model-preview-toolbar"><span className="muted">第 {page} 页 · 本页 {result.data.length} 条</span><button type="button" className="button subtle" disabled={busy || stale || !result.hasMore || !result.nextCursor} onClick={() => void run(result.nextCursor)}>关联下一页<ArrowRight size={14} /></button></div>
    </>}
  </>
}
