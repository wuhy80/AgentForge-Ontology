import { ArrowUpRight } from 'lucide-react'
import { lazy, Suspense, useState } from 'react'
import type { Definition, DefinitionRecord, Organization } from '../../lib/types'

const RelationGraph = lazy(() => import('./RelationGraph').then((module) => ({ default: module.RelationGraph })))

const effects = { none: '无副作用', dataWrite: '写入业务数据', externalWrite: '修改外部系统' }

// Keep the authorized current definition visible when a pending draft changes
// its endpoints. A current definition and its draft are explicitly labeled.
export function RelatedDefinitions({ kind, definition, entries, currentEntries, categories }: {
  kind: 'link' | 'action'
  definition: Definition
  entries: DefinitionRecord[]
  currentEntries?: DefinitionRecord[]
  categories: Organization[]
}) {
  const [graph, setGraph] = useState(false)
  const [drafts, setDrafts] = useState(false)
  const records = new Map<string, DefinitionRecord>()
  for (const record of [...(currentEntries ?? []), ...entries]) {
    if (record.definition.tenantId !== definition.tenantId || record.definition.kind !== kind) continue
    records.set(`${record.definition.id}:${record.status === 'active' ? 'current' : 'draft'}`, record)
  }
  const related = definition.id ? [...records.values()].filter(({ definition: item }) => kind === 'link'
    ? item.link?.sourceSchemaId === definition.id || item.link?.targetSchemaId === definition.id
    : item.action?.targetSchemaIds?.includes(definition.id)) : []
  related.sort((left, right) => left.definition.name.localeCompare(right.definition.name, 'zh-CN') || Number(right.status === 'active') - Number(left.status === 'active'))
  const schemaName = (id: string) => id === definition.id ? definition.name || '当前对象'
    : entries.find((item) => item.definition.tenantId === definition.tenantId && item.definition.id === id && item.definition.kind === 'schema')?.definition.name ?? '对象不可见'
  const categoryName = (id: string) => categories.find((category) => category.tenantId === definition.tenantId && category.id === id)?.name ?? '分类不可见'
  const label = kind === 'link' ? 'Link 信息' : '动作信息'
  const currentSchemas = new Map<string, DefinitionRecord>()
  for (const item of [...(currentEntries ?? []), ...entries]) {
    if (item.status === 'active' && item.definition.schema && item.definition.tenantId === definition.tenantId) currentSchemas.set(item.definition.id, item)
  }
  const graphRecords = new Map<string, DefinitionRecord>()
  for (const item of related.filter((item) => item.status === 'active')) graphRecords.set(item.definition.id, item)
  if (drafts) for (const item of related.filter((item) => item.status === 'draft')) graphRecords.set(item.definition.id, item)

  return <>
    <div className="model-section-heading"><div><h3>{label}</h3><p className="muted">{kind === 'link' ? '查看以此对象为来源或目标的关系，包含入站、出站和自关联。' : '查看明确关联此对象的动作；在动作定义中维护关联对象。'}仅显示有权查看的定义。</p></div><span className="badge">{related.length} 项</span></div>
    {kind === 'link' && definition.id && <>
      <div className="model-inline-actions" style={{ marginBottom: 14 }}><button type="button" className="button subtle" aria-pressed={graph} onClick={() => setGraph(true)}>图形</button><button type="button" className="button subtle" aria-pressed={!graph} onClick={() => setGraph(false)}>列表</button><a className="button subtle" href="#models?view=graph">打开分类关系图<ArrowUpRight size={13} /></a>{graph && <label className="muted"><input type="checkbox" checked={drafts} onChange={(event) => setDrafts(event.target.checked)} />显示关系草稿</label>}</div>
      {graph && <Suspense fallback={<p className="muted" role="status">正在加载关系画布…</p>}><RelationGraph compact schemas={[...currentSchemas.values()]} links={[...graphRecords.values()]} categories={categories} categoryId={definition.categoryId} focusSchemaId={definition.id} onSelectLink={(record) => { window.location.hash = `models/${encodeURIComponent(record.definition.id)}` }} onConnect={(source, target) => { window.location.hash = `models/new?kind=link&source=${encodeURIComponent(source)}&target=${encodeURIComponent(target)}` }} /></Suspense>}
    </>}
    {(kind !== 'link' || !graph) && (!related.length ? <div className="model-inline-empty">{!definition.id ? '保存对象后，可在这里查看关联定义。' : kind === 'link' ? '暂无与此对象相关的 Link。' : '暂无与此对象相关的动作。'}</div>
      : <div className="table-wrap model-related-table"><table aria-label={kind === 'link' ? '关联 Link' : '关联动作'}>
        <thead><tr><th>{kind === 'link' ? '关系名称' : '动作名称'}</th>{kind === 'link' ? <><th>方向与业务名称</th><th>另一端对象</th><th>基数</th></> : <><th>描述</th><th>副作用</th></>}<th>状态</th><th>分类</th></tr></thead>
        <tbody>{related.map((record) => {
          const item = record.definition
          const self = item.link?.sourceSchemaId === definition.id && item.link?.targetSchemaId === definition.id
          const outgoing = item.link?.sourceSchemaId === definition.id
          return <tr key={`${item.id}:${record.status}`}>
            <td><a className="model-related-link" href={`#models/${encodeURIComponent(item.id)}`}>{item.name}<ArrowUpRight size={13} aria-hidden="true" /></a></td>
            {item.link ? <>
              <td><span>{self ? '自关联' : outgoing ? '出站（正向）' : '入站（反向）'}</span><small>{self ? `${item.link.forwardName} / ${item.link.reverseName}` : outgoing ? item.link.forwardName : item.link.reverseName}</small></td>
              <td>{schemaName(outgoing ? item.link.targetSchemaId : item.link.sourceSchemaId)}</td>
              <td>{outgoing ? item.link.cardinality : ({ '1:N': 'N:1', 'N:1': '1:N' } as Record<string, string>)[item.link.cardinality] ?? item.link.cardinality}</td>
            </> : <><td>{item.description || '—'}</td><td>{item.action ? effects[item.action.sideEffects] : '—'}</td></>}
            <td><span className={`model-status ${record.status === 'active' ? 'model-status-active' : 'model-status-draft'}`}>{record.status === 'active' ? '已生效' : record.hasCurrent ? '待发布变更' : '草稿'}</span></td><td>{categoryName(item.categoryId)}</td>
          </tr>
        })}</tbody>
      </table></div>)}
  </>
}
