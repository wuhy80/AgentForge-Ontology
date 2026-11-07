import { useEffect, useRef, useState } from 'react'
import { ArrowRight, Check, Link2, LoaderCircle, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Instance, LinkInstance, Page, QueryResult, QuerySpec, SchemaLink } from '../../lib/types'
import { availableOperators, compileQuery, errorMessage, newCondition, newGroup, valueText } from './query'
import type { QueryEditor } from './query'
import { ValueInput } from './InstanceDialog'

function permissionMessage(message: string) {
  return `${message}。请管理员在“用户与权限”中检查此 Link 的关系操作权限，以及两端对象的实例访问权限。`
}

export function RelatedInstances({ api, schema, instanceId, onClose, onForbidden }: {
  api: ApiClient
  schema: DefinitionRecord
  instanceId: string
  onClose: () => void
  onForbidden: (message: string) => void
}) {
  const [links, setLinks] = useState<SchemaLink[]>([])
  const [selected, setSelected] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [writing, setWriting] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true); setLinks([]); setError('')
    api.schemaLinks(schema.definition.id, controller.signal).then((items) => {
      if (!controller.signal.aborted) { setLinks(items); setSelected(items[0] ? `${items[0].linkRef.id}:${items[0].direction}` : '') }
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setError(errorMessage(cause))
        if (cause instanceof ApiError && [401, 403].includes(cause.status)) onForbidden(cause.message)
      }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, schema.definition.id, onForbidden])
  const navigation = links.find((item) => `${item.linkRef.id}:${item.direction}` === selected)
  return <Dialog title="关联数据" description={`从“${schema.definition.name}”的当前实例出发，查询与它关联的真实对象。`} wide closeDisabled={writing} onClose={() => { if (!writing) onClose() }}>
    <div className="query-related-content">
      <div className="query-related-origin"><span className="badge">{schema.definition.name}</span><code>{instanceId}</code></div>
      {loading ? <p className="muted"><LoaderCircle size={15} className="query-spin" />正在读取可用关系…</p>
        : error ? <div className="notice notice-error" role="alert">{error}</div>
          : !links.length ? <div className="empty-state"><Link2 size={28} /><h3>此对象暂无可见的生效关系</h3><p className="muted">在模型管理中配置并发布 Link 后，再从这里查询。</p></div>
            : <><label className="field">查询关系<select aria-label="查询关系" value={selected} disabled={writing} onChange={(event) => setSelected(event.target.value)}>{links.map((item) => <option key={`${item.linkRef.id}:${item.direction}`} value={`${item.linkRef.id}:${item.direction}`}>{item.name} · {item.direction === 'out' ? '正向' : '反向'} · {item.cardinality}</option>)}</select></label>
              {navigation && <RelationshipWorkspace key={selected} api={api} navigation={navigation} instanceId={instanceId} onForbidden={onForbidden} onWriting={setWriting} />}</>}
    </div>
  </Dialog>
}

function RelationshipWorkspace({ api, navigation, instanceId, onForbidden, onWriting }: {
  api: ApiClient
  navigation: SchemaLink
  instanceId: string
  onForbidden: (message: string) => void
  onWriting: (busy: boolean) => void
}) {
  const [model, setModel] = useState<{ link: DefinitionRecord; target: DefinitionRecord }>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true); setError(''); setModel(undefined)
    Promise.all([api.getCurrent(navigation.linkRef.id, controller.signal), api.getCurrent(navigation.targetSchemaId, controller.signal)]).then(([link, target]) => {
      if (!controller.signal.aborted) setModel({ link, target })
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) {
        setError(errorMessage(cause))
        if (cause instanceof ApiError && [401, 403].includes(cause.status)) onForbidden(cause.message)
      }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, navigation.linkRef.id, navigation.targetSchemaId, onForbidden])
  if (loading) return <p className="muted">正在读取关系映射…</p>
  if (error) return <div className="notice notice-error" role="alert">{error}</div>
  if (!model?.link.definition.link || !model.target.definition.schema) return <div className="notice notice-error" role="alert">关系或对象定义不完整，请重新读取。</div>
  if (!model.link.definition.link.storageBinding) return <div className="notice"><strong>此关系尚未连接关系数据</strong><p>当前只有关系定义。请在关系编辑页选择平台关联表、已有数据库关联表或外键映射，保存并发布后再查询。</p><a href={`#models/${encodeURIComponent(navigation.linkRef.id)}`}>配置“{model.link.definition.name}”</a></div>
  return <RelationshipQuery api={api} model={model} navigation={navigation} instanceId={instanceId} onForbidden={onForbidden} onWriting={onWriting} />
}

function RelationshipQuery({ api, model, navigation, instanceId, onForbidden, onWriting }: {
  api: ApiClient
  model: { link: DefinitionRecord; target: DefinitionRecord }
  navigation: SchemaLink
  instanceId: string
  onForbidden: (message: string) => void
  onWriting: (busy: boolean) => void
}) {
  const properties = model.target.definition.schema!.properties
  const primary = model.target.definition.schema!.storageBinding
  const filterProperties = primary ? properties.filter((property) => property.mapping?.dataSourceId === primary.dataSourceId && (!property.mapping.table || property.mapping.table === primary.table) && (!property.mapping.namespace || property.mapping.namespace === primary.namespace)) : properties
  const managed = model.link.definition.link!.storageBinding!.mode === 'managed'
  const [select, setSelect] = useState<string[]>([])
  const [filterField, setFilterField] = useState('')
  const [op, setOp] = useState('eq')
  const [raw, setRaw] = useState('')
  const [limit, setLimit] = useState('25')
  const [executed, setExecuted] = useState<{ spec: QuerySpec; result: QueryResult; page: number }>()
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [managing, setManaging] = useState(false)
  const [mutationBusy, setMutationBusy] = useState(false)
  const request = useRef<AbortController | undefined>(undefined)
  const property = filterProperties.find((item) => item.id === filterField)
  useEffect(() => () => request.current?.abort(), [])
  function change() {
    request.current?.abort(); setBusy(false); setStale(!!executed); setError(''); setNotice('')
  }
  async function run(cursor?: string) {
    let spec: QuerySpec
    try {
      if (cursor && executed) spec = { ...executed.spec, page: { ...executed.spec.page, cursor } }
      else {
        const editor: QueryEditor = { select, limit, sort: '', direction: 'asc', filter: { ...newGroup(), children: filterField ? [{ ...newCondition(filterProperties), field: filterField, op, raw }] : [] } }
        spec = { ...compileQuery(navigation.targetSchemaId, properties, editor), via: { linkId: navigation.linkRef.id, direction: navigation.direction, instanceId } }
      }
    } catch (cause) { setError(errorMessage(cause)); return }
    request.current?.abort()
    const controller = new AbortController(); request.current = controller
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await api.query(spec, controller.signal)
      if (!controller.signal.aborted) {
        setExecuted({ spec: { ...spec, page: { limit: spec.page.limit } }, result, page: cursor ? (executed?.page ?? 1) + 1 : 1 }); setStale(false)
      }
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(errorMessage(cause))
        if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setExecuted(undefined); onForbidden(cause.status === 403 ? permissionMessage(cause.message) : cause.message) }
        if (cause instanceof ApiError && cause.code === 'invalid_cursor') setStale(true)
      }
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  function changed(message: string) { change(); setNotice(`${message}。请重新查询关联数据。`) }
  function writingChanged(value: boolean) {
    setMutationBusy(value); onWriting(value)
    if (value) { request.current?.abort(); setBusy(false); setStale(!!executed) }
  }
  const result = executed?.result
  return <>
    <div className="query-related-heading"><strong>{navigation.name}</strong><ArrowRight size={15} /><span>{model.target.definition.name}</span><span className="badge">{navigation.direction === 'out' ? '正向' : '反向'} · {navigation.cardinality}</span></div>
    {!managed && <p className="muted">已有数据库映射 · 只读关系</p>}
    <fieldset disabled={mutationBusy} className="query-related-fields"><legend>返回字段</legend>{properties.map((field) => <label key={field.id}><input type="checkbox" aria-label={`关联字段：${field.name}`} checked={select.includes(field.id)} onChange={(event) => { change(); setSelect(event.target.checked ? [...select, field.id] : select.filter((id) => id !== field.id)) }} />{field.name}</label>)}</fieldset>
    <fieldset disabled={mutationBusy} className="query-related-filter"><label className="field">筛选关联对象<select aria-label="关联筛选属性" value={filterField} onChange={(event) => { change(); setFilterField(event.target.value); setOp('eq'); setRaw('') }}><option value="">不附加筛选</option>{filterProperties.map((field) => <option key={field.id} value={field.id}>{field.name}</option>)}</select></label>
      {property && <><label className="field">条件<select aria-label="关联筛选条件" value={op} onChange={(event) => { change(); setOp(event.target.value) }}>{availableOperators(property).map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></label>{!['isNull', 'isNotNull'].includes(op) && <label className="field" htmlFor="related-value">比较值{['in', 'notIn'].includes(op) ? <textarea id="related-value" value={raw} onChange={(event) => { change(); setRaw(event.target.value) }} placeholder="每行一个值" /> : <ValueInput id="related-value" property={property} value={raw} onChange={(value) => { change(); setRaw(value) }} />}</label>}</>}
      <label className="field">每页条数<input aria-label="关联每页条数" type="number" min="1" max="100" value={limit} onChange={(event) => { change(); setLimit(event.target.value) }} /></label>
    </fieldset>
    <div className="query-related-toolbar"><button type="button" className="button primary" disabled={busy || mutationBusy || !select.length} onClick={() => void run()}>{busy ? <LoaderCircle size={15} className="query-spin" /> : <Link2 size={15} />}查询关联对象</button>{busy && <button type="button" className="button" onClick={() => { request.current?.abort(); setBusy(false); setNotice('关联查询已取消。') }}>取消关联查询</button>}{managed && <button type="button" className="button" aria-expanded={managing} disabled={mutationBusy} onClick={() => setManaging(!managing)}>{managing ? '收起关系维护' : '维护对象关系'}</button>}</div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {notice && <div className="notice" role="status">{notice}</div>}
    {stale && result && <div className="notice" role="status">关联查询配置或数据已改变，请重新查询；续页已停用。</div>}
    {result && <section aria-label="关联查询结果" className="query-related-results"><div className="table-wrap"><table><thead><tr><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}</th>)}</tr></thead><tbody>{result.data.map((instance) => <tr key={instance.id}><td><code>{instance.id}</code></td>{result.fields.map((field) => <td key={field.id}>{valueText(instance.values[field.id])}</td>)}</tr>)}</tbody></table></div>{!result.data.length && <p className="muted">没有匹配的关联对象。</p>}<div className="query-related-toolbar"><span className="muted">第 {executed!.page} 页 · 本页 {result.data.length} 条</span><button type="button" className="button" disabled={busy || stale || !result.hasMore || !result.nextCursor} onClick={() => void run(result.nextCursor)}>关联下一页<ArrowRight size={14} /></button></div></section>}
    {managed && managing && <ManagedRelations api={api} navigation={navigation} instanceId={instanceId} target={model.target} fields={select} onChanged={changed} onForbidden={onForbidden} onWriting={writingChanged} />}
  </>
}

function ManagedRelations({ api, navigation, instanceId, target, fields, onChanged, onForbidden, onWriting }: {
  api: ApiClient; navigation: SchemaLink; instanceId: string; target: DefinitionRecord; fields: string[]
  onChanged: (message: string) => void; onForbidden: (message: string) => void; onWriting: (busy: boolean) => void
}) {
  const [edges, setEdges] = useState<Page<LinkInstance>>()
  const [candidates, setCandidates] = useState<QueryResult>()
  const [candidateSpec, setCandidateSpec] = useState<QuerySpec>()
  const [selected, setSelected] = useState('')
  const [loading, setLoading] = useState(false)
  const [writing, setWriting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirmRemove, setConfirmRemove] = useState<LinkInstance>()
  const alive = useRef(true)
  const request = useRef<AbortController | undefined>(undefined)
  useEffect(() => { alive.current = true; return () => { alive.current = false; request.current?.abort() } }, [])
  function report(cause: unknown) {
    setError(errorMessage(cause))
    if (cause instanceof ApiError && [401, 403].includes(cause.status)) { setEdges(undefined); setCandidates(undefined); onForbidden(cause.status === 403 ? permissionMessage(cause.message) : cause.message) }
  }
  async function readEdges(cursor?: string) {
    request.current?.abort()
    const controller = new AbortController(); request.current = controller
    setLoading(true); setError(''); setConfirmRemove(undefined)
    try {
      const result = await api.listLinkInstances(navigation.linkRef.id, navigation.direction, instanceId, cursor, controller.signal)
      if (!controller.signal.aborted) setEdges(result)
    } catch (cause) { if (!controller.signal.aborted) report(cause) }
    finally { if (!controller.signal.aborted) setLoading(false) }
  }
  async function readCandidates(cursor?: string) {
    if (!fields.length) { setError('请先选择关联对象的返回字段，再读取可关联对象。'); return }
    request.current?.abort()
    const controller = new AbortController(); request.current = controller
    setLoading(true); setError(''); setSelected('')
    const spec: QuerySpec = cursor && candidateSpec ? { ...candidateSpec, page: { ...candidateSpec.page, cursor } } : { from: { schemaId: target.definition.id }, select: fields, page: { limit: 25 } }
    try {
      const result = await api.query(spec, controller.signal)
      if (!controller.signal.aborted) { setCandidates(result); setCandidateSpec({ ...spec, page: { limit: spec.page.limit } }) }
    } catch (cause) { if (!controller.signal.aborted) report(cause) }
    finally { if (!controller.signal.aborted) setLoading(false) }
  }
  async function write(edge?: LinkInstance) {
    if (writing || !edge && !selected) return
    setWriting(true); onWriting(true); setError(''); setNotice('')
    try {
      if (edge) await api.deleteLinkInstance(navigation.linkRef.id, edge.id, edge.etag)
      else await api.createLinkInstance(navigation.linkRef.id, navigation.direction === 'out' ? instanceId : selected, navigation.direction === 'out' ? selected : instanceId)
      if (!alive.current) return
      setConfirmRemove(undefined); setSelected(''); setEdges(undefined)
      const message = edge ? '关系已移除' : '关系已添加'
      setNotice(message); onChanged(message)
    } catch (cause) {
      if (!alive.current) return
      report(cause)
      if (cause instanceof ApiError && cause.status === 412) { setConfirmRemove(undefined); setEdges(undefined); setNotice('关系已改变，请重新读取已有关系后再操作。') }
      else if (!(cause instanceof ApiError) || cause.status === 0 || cause.status >= 500) setNotice('请求结果未确认，请重新读取已有关系核对结果后再操作。')
    } finally { if (alive.current) setWriting(false); onWriting(false) }
  }
  function candidateLabel(instance: Instance) {
    const values = (candidates?.fields ?? []).slice(0, 3).map((field) => valueText(instance.values[field.id])).filter(Boolean).join(' · ')
    return `${values || target.definition.name}（${instance.id}）`
  }
  return <section className="query-related-manage" aria-label="维护对象关系">
    <h3>维护对象关系</h3><p className="muted">添加或移除关联，不会删除两端对象。只显示当前有权访问的实例。</p>
    {error && <div className="notice notice-error" role="alert">{error}</div>}{notice && <div className="notice" role="status"><Check size={15} />{notice}</div>}
    <fieldset disabled={writing || loading} className="query-related-manage-fields"><div className="query-related-toolbar"><button type="button" className="button" onClick={() => void readCandidates()}><RefreshCw size={14} />读取可关联对象</button>{candidates?.hasMore && <button type="button" className="button" disabled={!candidates.nextCursor} onClick={() => void readCandidates(candidates.nextCursor)}>更多可关联对象</button>}</div>
      {candidates && <div className="query-related-add"><label className="field">选择{target.definition.name}<select aria-label="选择要关联的对象" value={selected} onChange={(event) => setSelected(event.target.value)}><option value="">{candidates.data.length ? '选择对象' : '本页没有可关联对象'}</option>{candidates.data.map((instance) => <option key={instance.id} value={instance.id}>{candidateLabel(instance)}</option>)}</select></label><button type="button" className="button primary" disabled={!selected} onClick={() => void write()}><Plus size={14} />添加关系</button></div>}
      <div className="query-related-toolbar"><button type="button" className="button" onClick={() => void readEdges()}><RefreshCw size={14} />读取已有关系</button>{edges?.hasMore && <button type="button" className="button" disabled={!edges.nextCursor} onClick={() => void readEdges(edges.nextCursor)}>更多已有关系</button>}</div>
      {edges && <div className="table-wrap"><table aria-label="已有关系"><thead><tr><th>关联对象 ID</th><th>操作</th></tr></thead><tbody>{edges.data.map((edge) => <tr key={edge.id}><td><code>{navigation.direction === 'out' ? edge.targetId : edge.sourceId}</code></td><td><button type="button" className="button subtle" onClick={() => setConfirmRemove(edge)}><Trash2 size={14} />移除关系</button></td></tr>)}</tbody></table>{!edges.data.length && <p className="muted">暂无可见关系。</p>}</div>}
      {confirmRemove && <div className="notice" role="alert"><p>确认移除与对象 {navigation.direction === 'out' ? confirmRemove.targetId : confirmRemove.sourceId} 的关系？</p><div className="query-related-toolbar"><button type="button" className="button" onClick={() => void write(confirmRemove)}>确认移除关系</button><button type="button" className="button subtle" onClick={() => setConfirmRemove(undefined)}>保留关系</button></div></div>}
    </fieldset>{(loading || writing) && <p className="muted"><LoaderCircle size={14} className="query-spin" />{writing ? '正在保存关系…' : '正在读取…'}</p>}
  </section>
}
