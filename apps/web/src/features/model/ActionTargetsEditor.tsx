import { useState } from 'react'
import type { DefinitionRecord, Organization } from '../../lib/types'

export function ActionTargetsEditor({ selected, schemas, categories, onChange }: {
  selected: string[]
  schemas: DefinitionRecord[]
  categories: Organization[]
  onChange: (ids: string[]) => void
}) {
  const [search, setSearch] = useState('')
  const options = schemas.map(({ definition }) => ({ id: definition.id, name: `${categories.find((category) => category.id === definition.categoryId)?.name ?? '业务分类'} / ${definition.name}` }))
  const missing = selected.filter((id) => !options.some((option) => option.id === id))
  const term = search.trim().toLocaleLowerCase()
  const visible = options.filter((option) => !term || option.name.toLocaleLowerCase().includes(term))
  return <section className="model-action-targets" aria-label="关联对象">
    <div className="model-section-heading"><div><h3>关联对象</h3><p className="muted">选择此动作面向的已生效对象，最多 32 个。未选择时不归属任何对象；此设置不授予调用或数据权限。</p></div><span className="badge">已选 {selected.length}</span></div>
    {!!options.length && <label className="model-target-search">搜索关联对象<input className="field" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="按分类或对象名称搜索" /></label>}
    <div className="model-target-options">{visible.map((option) => <label className="model-checkbox" key={option.id}><input type="checkbox" aria-label={`关联对象：${option.name}`} checked={selected.includes(option.id)} disabled={selected.length >= 32 && !selected.includes(option.id)} onChange={(event) => onChange(event.target.checked ? [...selected, option.id] : selected.filter((id) => id !== option.id))} />{option.name}</label>)}
    {missing.map((id, index) => <label className="model-checkbox" key={id}><input type="checkbox" checked onChange={() => onChange(selected.filter((value) => value !== id))} />当前关联对象 {index + 1}（目录不可见）</label>)}</div>
    {!visible.length && <div className="model-inline-empty">{search ? '没有匹配的对象。' : '暂无可选的已生效对象。'}</div>}
  </section>
}
