import { useEffect, useState } from 'react'
import type { ApiClient } from '../../lib/api'
import type { DefinitionDependency, DefinitionDependencyImpact, DefinitionRecord } from '../../lib/types'
import { kindLabels } from './model-helpers'
import './definition-dependencies.css'

const reasons: Record<string, string> = {
  enum: '枚举取值', 'link-source': '关系来源', 'link-target': '关系目标',
  'action-target': '动作关联对象', 'data-write': '业务写入范围', implementation: '函数实现',
}

export function DefinitionDependencies({ api, record, dirty, active = true }: {
  api: ApiClient; record?: DefinitionRecord; dirty: boolean; active?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<'current' | 'draft'>()
  if (!record || !active) return null
  const selected = view === 'current' && (record.hasCurrent || record.status === 'active') ? 'current'
    : view === 'draft' && record.status === 'draft' ? 'draft' : record.status === 'draft' ? 'draft' : 'current'
  return <section className="model-definition-dependencies" aria-label="定义依赖影响">
    <div className="model-section-heading"><div><h3>定义依赖影响</h3><p className="muted">查看当前定义与已保存草稿中的直接引用。</p></div><button type="button" className="button subtle" aria-expanded={open} onClick={() => setOpen(!open)}>{open ? '收起定义依赖' : '查看定义依赖'}</button></div>
    {open && <>
      <p className="muted">仅展示有权查看的定义。业务数据、权限策略、保存查询、运行任务及外部应用不在此范围；空列表不能作为停用或删除依据。</p>
      {dirty ? <p role="status">存在未保存修改，请先保存草稿后查看依赖。</p> : <>
        {record.status === 'draft' && record.hasCurrent && <label className="dependency-view">查看内容<select className="field" value={selected} onChange={(event) => setView(event.target.value as 'current' | 'draft')}><option value="draft">已保存草稿</option><option value="current">当前生效定义</option></select></label>}
        <DependencyReport key={`${record.definition.tenantId}:${record.definition.id}:${record.etag}:${record.status}:${selected}`} api={api} record={record} view={selected} />
      </>}
    </>}
  </section>
}

function DependencyReport({ api, record, view }: { api: ApiClient; record: DefinitionRecord; view: 'current' | 'draft' }) {
  const [report, setReport] = useState<DefinitionDependencyImpact>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refresh, setRefresh] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setReport(undefined); setError(''); setLoading(true)
    api.definitionDependencies(record.definition.id, view, controller.signal).then((value) => {
      if (controller.signal.aborted) return
      const sameView = view === (record.status === 'draft' ? 'draft' : 'current')
      if (value.scope !== 'direct-definition-references' || value.definition.id !== record.definition.id || value.definition.view !== view || (sameView && value.definition.etag !== record.etag)) throw new Error('定义内容已变化，请重新载入模型后查看依赖。')
      setReport(value)
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '依赖读取失败。') })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, record, view, refresh])
  return <>
    <button type="button" className="button subtle" disabled={loading} onClick={() => { setReport(undefined); setError(''); setRefresh(refresh + 1) }}>刷新依赖</button>
    {loading && <p role="status">正在读取定义依赖…</p>}
    {error && <p className="notice notice-error" role="alert">{error}</p>}
    {report && <div className="dependency-columns">
      <DependencyList label="引用了谁" items={report.dependencies} categoryId={record.definition.categoryId} />
      <DependencyList label="被谁引用" items={report.dependents} categoryId={record.definition.categoryId} />
    </div>}
  </>
}

function DependencyList({ label, items, categoryId }: { label: string; items: DefinitionDependency[]; categoryId: string }) {
  return <section aria-label={label}><h4>{label}<span className="badge">{items.length} 项可见</span></h4>
    {!items.length ? <p className="model-inline-empty">暂无可展示的直接定义引用。</p> : <ul>{items.map(({ definition: d, reasons: tags }) => <li key={`${d.id}:${d.view}`}>
      <div>{d.categoryId === categoryId ? <a href={`#models/${encodeURIComponent(d.id)}`}>{d.name}</a> : <strong>{d.name}</strong>}<span className="badge">{kindLabels[d.kind]}</span></div>
      <small>{d.view === 'draft' ? '已保存草稿' : '当前生效'}{d.categoryId !== categoryId ? ' · 跨分类' : ''}</small>
      <p>{tags.map((tag) => reasons[tag] ?? '定义引用').join('、')}</p>
    </li>)}</ul>}
  </section>
}
