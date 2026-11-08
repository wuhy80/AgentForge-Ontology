import { useState } from 'react'
import { Plus, Save, Trash2 } from 'lucide-react'
import type { Callable, CallableTestCase } from '../../lib/types'
import { buildWriteValues, errorMessage, type FieldDrafts } from '../query/query'
import { CallableInputs, testCaseDrafts } from './CallableInputs'
import './callable-test-cases.css'

export function CallableTestCasesEditor({ callable, disabled, onChange }: { callable: Callable; disabled: boolean; onChange: (testCases: CallableTestCase[]) => void }) {
  const [editing, setEditing] = useState('')
  const [name, setName] = useState('')
  const [drafts, setDrafts] = useState<FieldDrafts>({})
  const [error, setError] = useState('')
  const properties = callable.input ?? []
  const samples = callable.testCases ?? []
  const parametersReady = properties.every((property) => !!property.id)

  function reset() { setEditing(''); setName(''); setDrafts({}); setError('') }
  function save() {
    if (disabled || !parametersReady) return
    const normalized = name.trim()
    if (!normalized || [...normalized].length > 80) { setError('请填写 1–80 字的样例名称。'); return }
    if (samples.some((sample) => sample.id !== editing && sample.name.normalize('NFC').toLocaleLowerCase() === normalized.normalize('NFC').toLocaleLowerCase())) { setError('样例名称不能重复。'); return }
    if (!editing && samples.length >= 10) { setError('每个定义最多保存 10 个测试样例。'); return }
    try {
      // A reusable sample may omit required values; running still validates the full input.
      const input = buildWriteValues(properties, drafts, {})
      const sample = { id: editing || crypto.randomUUID(), name: normalized, input }
      const next = editing ? samples.map((item) => item.id === editing ? sample : item) : [...samples, sample]
      if (new TextEncoder().encode(JSON.stringify(next)).length > 65536) throw new Error('测试样例总大小不能超过 64 KiB。')
      onChange(next); reset()
    } catch (cause) { setError(errorMessage(cause)) }
  }

  return <section className="model-test-cases" aria-label="常用测试样例">
    <div className="model-section-heading"><div><h3>常用测试样例</h3><p className="muted">最多 10 个，随定义草稿保存，共享定义的读取权限。加入或更新后，点击「保存草稿」。</p></div><span className="badge">{samples.length} / 10</span></div>
    {!parametersReady && <p className="muted">请先保存参数草稿，建立稳定参数绑定后再添加样例。</p>}
    <fieldset className="model-fieldset" disabled={disabled || !parametersReady}>
      {samples.length > 0 && <ul className="model-test-case-list">{samples.map((sample) => <li key={sample.id}><span>{sample.name}</span><div className="model-inline-actions">
        <button type="button" className="button subtle" aria-label={`编辑样例 ${sample.name}`} onClick={() => { setEditing(sample.id); setName(sample.name); setDrafts(testCaseDrafts(properties, sample.input)); setError('') }}>编辑</button>
        <button type="button" className="button subtle" aria-label={`删除样例 ${sample.name}`} onClick={() => { onChange(samples.filter((item) => item.id !== sample.id)); if (editing === sample.id) reset() }}><Trash2 size={15} />删除</button>
      </div></li>)}</ul>}
      <details key={`${editing}:${samples.length}`} open={!!editing}>
        <summary>{editing ? '编辑测试样例' : '添加测试样例'}</summary>
        <label>样例名称<input className="field" value={name} maxLength={80} onChange={(event) => { setName(event.target.value); setError('') }} /></label>
        <CallableInputs properties={properties} drafts={drafts} onChange={(id, patch) => setDrafts((current) => ({ ...current, [id]: { ...current[id] ?? { mode: 'omit', text: '' }, ...patch } }))} />
        {error && <p className="notice notice-error" role="alert">{error}</p>}
        <div className="model-inline-actions"><button type="button" className="button" disabled={!editing && samples.length >= 10} onClick={save}>{editing ? <Save size={15} /> : <Plus size={15} />}{editing ? '更新样例到草稿' : '加入草稿样例'}</button>{editing && <button type="button" className="button subtle" onClick={reset}>取消编辑样例</button>}</div>
      </details>
    </fieldset>
  </section>
}
