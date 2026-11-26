import { useState } from 'react'
import type { DefinitionRecord, Schema } from '../../lib/types'

export function SchemaClassificationEditor({ schema, entries, categoryId, disabled, onChange }: {
  schema: Schema
  entries: DefinitionRecord[]
  categoryId: string
  disabled: boolean
  onChange: (schema: Schema) => void
}) {
  const [text, setText] = useState('')
  const tags = schema.tags ?? []
  const key = (name: string) => name.normalize('NFC').toLowerCase()
  const selected = new Set(tags.map(key))
  const available = new Map<string, string>()
  for (const { definition, status } of entries) {
    if (status !== 'active' || definition.categoryId !== categoryId) continue
    for (const tag of definition.schema?.tags ?? []) if (!selected.has(key(tag))) available.set(key(tag), tag)
  }
  const name = text.trim().normalize('NFC')
  function add(tag: string) {
    if (!tag || selected.has(key(tag)) || tags.length >= 20) return
    onChange({ ...schema, tags: [...tags, available.get(key(tag)) ?? tag] })
    setText('')
  }
  return <section className="model-basics">
    <h3>标签与全景层位</h3>
    <div className="form-grid">
      <label>全景层位<select className="field" disabled={disabled} value={schema.panoramaLayer ?? ''} onChange={event => onChange({ ...schema, panoramaLayer: (event.target.value || undefined) as Schema['panoramaLayer'] })}>
        <option value="">待分层</option><option value="L1">L1 · 底座与主数据</option><option value="L2">L2 · 概念与规则</option><option value="L3">L3 · 事件与单据</option>
      </select></label>
      <label>选择已有标签<select className="field" disabled={disabled || tags.length >= 20 || !available.size} value="" onChange={event => add(event.target.value)}>
        <option value="">选择标签</option>{[...available.values()].sort((a, b) => a.localeCompare(b, 'zh-CN')).map(tag => <option key={tag}>{tag}</option>)}
      </select></label>
      <label>新标签<input className="field" value={text} maxLength={32} disabled={disabled} placeholder="输入标签名称" onChange={event => setText(event.target.value)} onKeyDown={event => {
        if (event.key === 'Enter') { event.preventDefault(); add(name) }
      }} /></label>
      <div><button className="button" type="button" disabled={disabled || !name || tags.length >= 20 || selected.has(key(name))} onClick={() => add(name)}>新增标签</button></div>
    </div>
    <div className="model-tag-list" aria-label="已选标签">{tags.map(tag => <button className="button subtle" type="button" disabled={disabled} aria-label={`移除标签 ${tag}`} key={tag} onClick={() => onChange({ ...schema, tags: tags.filter(value => value !== tag) })}>{tag} ×</button>)}</div>
  </section>
}
