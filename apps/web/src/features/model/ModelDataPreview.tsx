import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowLeft, ArrowRight, Eye, LoaderCircle, RefreshCw, X } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Property, QueryResult } from '../../lib/types'
import { errorMessage, propertyValueText } from '../query/query'
import { ModelLinkPreview } from './ModelLinkPreview'

function initialFields(properties: Property[]): string[] {
  const short = properties.filter((property) => property.dataType !== 'longtext')
  const preferred = short.filter((property) => /标题|名称|姓名|^(name|title)$/i.test(property.name))
  return [...preferred, ...short.filter((property) => !preferred.includes(property))].slice(0, 6).map((property) => property.id)
}

interface PreviewPage {
  result: QueryResult
  index: number
  cursors: (string | undefined)[]
  fields: string[]
  limit: number
}

export function ModelDataPreview({ api, schemaId, active, hasCurrent, draft }: {
  api: ApiClient
  schemaId: string
  active: boolean
  hasCurrent: boolean
  draft: boolean
}) {
  return <section className="model-data-preview" aria-label="模型数据预览">
    <div className="model-section-heading"><div><h3><Eye size={17} />数据预览</h3><p className="muted">预览当前对象的真实数据，选择记录后在下方查看相关 Link 数据。</p></div></div>
    {!hasCurrent ? <div className="model-inline-empty">此对象尚未发布，请先发布模型后再预览数据。</div>
      : <CurrentDataPreview key={schemaId} api={api} schemaId={schemaId} active={active} draft={draft} />}
  </section>
}

function CurrentDataPreview({ api, schemaId, active, draft }: {
  api: ApiClient; schemaId: string; active: boolean; draft: boolean
}) {
  const [current, setCurrent] = useState<DefinitionRecord>()
  const [fields, setFields] = useState<string[]>([])
  const [limit, setLimit] = useState('10')
  const [page, setPage] = useState<PreviewPage>()
  const [selectedId, setSelectedId] = useState('')
  const [loading, setLoading] = useState<'definition' | 'data' | ''>('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [retry, setRetry] = useState(0)
  const request = useRef<AbortController | undefined>(undefined)
  const properties = current?.definition.schema?.properties ?? []
  const selected = page?.result.data.find((instance) => instance.id === selectedId)

  const run = useCallback(async (record: DefinitionRecord, selection: string[], size: number, index = 0, cursors: (string | undefined)[] = [undefined]) => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setLoading('data'); setError(''); setNotice(''); setPage(undefined); setSelectedId('')
    try {
      const result = await api.query({ from: { schemaId: record.definition.id }, select: selection, page: { limit: size, ...(cursors[index] ? { cursor: cursors[index] } : {}) } }, controller.signal)
      if (controller.signal.aborted) return
      setPage({ result, index, cursors, fields: selection, limit: size })
      setSelectedId(result.data[0]?.id ?? '')
    } catch (cause) {
      if (controller.signal.aborted) return
      const denied = cause instanceof ApiError && [401, 403].includes(cause.status)
      setError(denied ? `${errorMessage(cause)}。数据预览需要此对象的实例查询权限；请检查所选字段权限。` : errorMessage(cause))
      if (cause instanceof ApiError && cause.code === 'invalid_cursor') setNotice('分页已失效，请刷新预览，从第一页重新读取。')
    } finally { if (!controller.signal.aborted) setLoading('') }
  }, [api])

  useEffect(() => {
    request.current?.abort()
    setCurrent(undefined); setPage(undefined); setSelectedId(''); setError(''); setNotice(''); setLoading('')
    if (!active) return
    const controller = new AbortController()
    request.current = controller
    setLoading('definition')
    api.getCurrent(schemaId, controller.signal).then(async (record) => {
      if (controller.signal.aborted) return
      if (record.status !== 'active' || record.definition.id !== schemaId || !record.definition.schema) throw new Error('对象当前定义未生效，暂时不能预览数据。')
      const selection = initialFields(record.definition.schema.properties)
      setCurrent(record); setFields(selection); setLimit('10')
      if (!selection.length) { setLoading(''); setNotice('请展开预览字段，选择要读取的属性后加载数据。'); return }
      await run(record, selection, 10)
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) { setError(errorMessage(cause)); setLoading('') }
    })
    return () => { controller.abort(); request.current?.abort() }
  }, [api, schemaId, active, run, retry])

  function changeFields(selection: string[]) {
    request.current?.abort(); setLoading(''); setFields(selection); setPage(undefined); setSelectedId(''); setError('')
    setNotice('预览字段已改变，请点击“加载预览”读取数据。')
  }
  function changeLimit(value: string) {
    request.current?.abort(); setLoading(''); setLimit(value); setPage(undefined); setSelectedId(''); setError('')
    setNotice('每页条数已改变，请点击“加载预览”读取数据。')
  }
  function refresh() {
    if (!current || !fields.length) return
    if (!/^\d+$/.test(limit) || Number(limit) < 1 || Number(limit) > 100) { setError('每页条数应为 1 至 100 的整数。'); return }
    void run(current, fields, Number(limit))
  }
  if (!active) return null
  return <>
    {draft && <div className="notice" role="status">草稿尚未发布；此处读取当前生效模型的数据，未保存的属性和映射修改不参与预览。</div>}
    {current && <>
      <details className="model-data-preview-fields"><summary>预览字段<span className="muted">已选 {fields.length} / {properties.length}</span></summary>
        <p className="muted">默认预览常用字段。长正文可按需勾选；预览仍遵循当前账号的行和字段权限。</p>
        <div className="model-data-preview-field-options">{properties.map((property) => <label key={property.id}><input type="checkbox" aria-label={`预览字段：${property.name}`} checked={fields.includes(property.id)} onChange={(event) => changeFields(event.target.checked ? [...fields, property.id] : fields.filter((field) => field !== property.id))} />{property.name}{property.dataType === 'longtext' && <small>长正文</small>}</label>)}</div>
      </details>
      <div className="model-data-preview-toolbar"><label>每页条数<select className="field" aria-label="预览每页条数" value={limit} onChange={(event) => changeLimit(event.target.value)}>{[10, 25, 50, 100].map((size) => <option key={size} value={size}>{size}</option>)}</select></label>
        <button type="button" className="button" disabled={!!loading || !fields.length} onClick={refresh}><RefreshCw size={15} />{page ? '刷新预览' : '加载预览'}</button>
        {page && <span className="muted">第 {page.index + 1} 页 · 本页 {page.result.data.length} 条</span>}
        {loading && <button type="button" className="button subtle" onClick={() => { request.current?.abort(); setLoading(''); setNotice('数据预览已取消。') }}><X size={14} />取消预览</button>}
      </div>
    </>}
    {loading && <p className="muted" role="status"><LoaderCircle size={15} className="model-spin" />{loading === 'definition' ? '正在读取当前生效模型…' : '正在读取预览数据…'}</p>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {!current && !loading && error && <button type="button" className="button" onClick={() => setRetry((value) => value + 1)}><RefreshCw size={15} />重新读取模型</button>}
    {notice && <div className="notice" role="status">{notice}</div>}
    {page && <>
      <div className="table-wrap model-data-preview-table"><table aria-label="对象数据预览"><thead><tr><th>#</th>{page.result.fields.map((field) => <th key={field.id}>{field.name}</th>)}<th>记录</th></tr></thead><tbody>{page.result.data.map((instance, index) => <tr key={instance.id} aria-selected={selectedId === instance.id} onClick={() => setSelectedId(instance.id)}><td>{index + 1}</td>{page.result.fields.map((field) => { const text = propertyValueText(field, instance.values[field.id]); return <td key={field.id}><span className="model-data-preview-cell">{text.length > 160 ? `${text.slice(0, 160)}…` : text}</span></td> })}<td><button type="button" className="button subtle" aria-label={`预览记录 ${instance.id}`} onClick={() => setSelectedId(instance.id)}>{selectedId === instance.id ? '已选中' : '查看记录'}</button></td></tr>)}</tbody></table></div>
      {!page.result.data.length && <div className="model-inline-empty">当前对象暂无可预览的数据。</div>}
      <div className="model-data-preview-toolbar"><button type="button" className="button" disabled={!!loading || !page.index} onClick={() => { if (current) void run(current, page.fields, page.limit, page.index - 1, page.cursors.slice(0, page.index)) }}><ArrowLeft size={14} />预览上一页</button><button type="button" className="button" disabled={!!loading || !page.result.hasMore || !page.result.nextCursor} onClick={() => { if (current && page.result.nextCursor) void run(current, page.fields, page.limit, page.index + 1, [...page.cursors, page.result.nextCursor]) }}>预览下一页<ArrowRight size={14} /></button></div>
      {selected && <section className="model-data-preview-record" aria-label="选中记录预览"><div className="model-section-heading"><div><h3>当前记录</h3><p className="muted"><code>{selected.id}</code></p></div></div><dl>{page.result.fields.map((field) => <div key={field.id}><dt>{field.name}</dt><dd><pre>{propertyValueText(field, selected.values[field.id])}</pre></dd></div>)}</dl></section>}
    </>}
    <ModelLinkPreview api={api} schemaId={schemaId} instanceId={selected?.id} active={active} />
  </>
}
