import { Plus, Trash2 } from 'lucide-react'
import type { Schema, SchemaLifecycle } from '../../lib/types'
import { MAX_LIFECYCLE_NAME, MAX_LIFECYCLE_STAGES, MAX_LIFECYCLE_TRANSITIONS } from './model-helpers'

const newID = () => crypto.randomUUID()

/**
 * Declares the record lifecycle that travels with the Schema definition. Stages
 * and transitions keep stable IDs, so renaming the display text never changes a
 * record's current stage or its history. Records are never moved automatically:
 * removing a declared stage makes Data report that stage as unknown instead.
 */
export function SchemaLifecycleEditor({ schema, disabled, onChange }: {
  schema: Schema
  disabled: boolean
  onChange: (schema: Schema) => void
}) {
  const lifecycle = schema.lifecycle
  if (schema.storageBinding) return <p className="model-inline-empty">外部映射模型的阶段由来源系统维护，暂不支持在此声明记录流程。</p>
  if (!lifecycle) return <>
    <div className="model-section-heading">
      <div><h3>记录流程</h3><p className="muted">为对象声明业务阶段与流转，托管记录的详情按当前定义显示阶段和历史。</p></div>
      <button type="button" className="button subtle" disabled={disabled}
        onClick={() => { const id = newID(); onChange({ ...schema, lifecycle: { initialStageId: id, stages: [{ id, name: '草稿' }], transitions: [] } }) }}>
        <Plus size={15} />添加记录流程
      </button>
    </div>
    <div className="model-inline-empty">尚未声明记录流程。未声明时记录详情不显示阶段与流转入口。</div>
  </>
  const stages = lifecycle.stages ?? []
  const transitions = lifecycle.transitions ?? []
  const referenced = new Set(transitions.flatMap((transition) => [transition.from, transition.to]))
  const apply = (next: SchemaLifecycle) => onChange({ ...schema, lifecycle: next })
  const updateStage = (index: number, patch: Partial<SchemaLifecycle['stages'][number]>) =>
    apply({ ...lifecycle, stages: stages.map((stage, position) => position === index ? { ...stage, ...patch } : stage) })
  const updateTransition = (index: number, patch: Partial<NonNullable<SchemaLifecycle['transitions']>[number]>) =>
    apply({ ...lifecycle, transitions: transitions.map((transition, position) => position === index ? { ...transition, ...patch } : transition) })
  const addStage = () => {
    if (stages.length >= MAX_LIFECYCLE_STAGES) return
    apply({ ...lifecycle, stages: [...stages, { id: newID(), name: `阶段${stages.length + 1}` }] })
  }
  const removeStage = (index: number) => {
    const stage = stages[index]
    if (stages.length <= 1 || stage.id === lifecycle.initialStageId || referenced.has(stage.id)) return
    apply({ ...lifecycle, stages: stages.filter((_, position) => position !== index) })
  }
  const addTransition = () => {
    if (transitions.length >= MAX_LIFECYCLE_TRANSITIONS || stages.length < 2) return
    const from = stages[0], to = stages.find((stage) => stage.id !== from.id)!
    apply({ ...lifecycle, transitions: [...transitions, { id: newID(), name: `${from.name} → ${to.name}`, from: from.id, to: to.id }] })
  }
  const removeTransition = (index: number) =>
    apply({ ...lifecycle, transitions: transitions.filter((_, position) => position !== index) })

  return <>
    <div className="model-section-heading">
      <div><h3>记录流程</h3><p className="muted">阶段与流转随当前定义发布，不需要数据库迁移。已有记录停留在原阶段；移除某阶段后该记录显示阶段未知并暂停流转。</p></div>
      <div className="model-inline-actions">
        <button type="button" className="button subtle" disabled={disabled} onClick={() => onChange({ ...schema, lifecycle: undefined })}>移除记录流程</button>
      </div>
    </div>
    <p className="model-lifecycle-note">当前 {stages.length} / {MAX_LIFECYCLE_STAGES} 个阶段，{transitions.length} / {MAX_LIFECYCLE_TRANSITIONS} 条流转。阶段和流转名称最多 {MAX_LIFECYCLE_NAME} 个字符且不能重复。</p>
    {stages.map((stage, index) => <div className="model-lifecycle-row" key={stage.id}>
      <label>阶段名称<input className="field" value={stage.name} maxLength={MAX_LIFECYCLE_NAME} disabled={disabled}
        aria-label={`阶段${index + 1}名称`} onChange={(event) => updateStage(index, { name: event.target.value })} /></label>
      <label className="model-lifecycle-initial"><input type="radio" name={`lifecycle-initial-${schema.mode}`} disabled={disabled}
        aria-label={`设为初始阶段${index + 1}`} checked={stage.id === lifecycle.initialStageId}
        onChange={() => apply({ ...lifecycle, initialStageId: stage.id })} />初始阶段</label>
      <button type="button" className="button subtle model-icon-button" aria-label={`删除阶段${index + 1}`}
        disabled={disabled || stages.length <= 1 || stage.id === lifecycle.initialStageId || referenced.has(stage.id)}
        title={referenced.has(stage.id) ? '该阶段仍有流转引用，请先删除相关流转' : stage.id === lifecycle.initialStageId ? '初始阶段不能删除，请先切换初始阶段' : undefined}
        onClick={() => removeStage(index)}><Trash2 size={16} /></button>
    </div>)}
    <div className="model-section-heading model-lifecycle-heading">
      <div><h4>阶段流转</h4></div>
      <div className="model-inline-actions">
        <button type="button" className="button subtle" disabled={disabled || stages.length >= MAX_LIFECYCLE_STAGES} onClick={addStage}><Plus size={15} />添加阶段</button>
        <button type="button" className="button subtle" disabled={disabled || stages.length < 2 || transitions.length >= MAX_LIFECYCLE_TRANSITIONS} onClick={addTransition}><Plus size={15} />添加流转</button>
      </div>
    </div>
    {transitions.length === 0 && <div className="model-inline-empty">尚未声明流转。记录只有初始阶段，无法推进。</div>}
    {transitions.map((transition, index) => <div className="model-lifecycle-row model-lifecycle-transition" key={transition.id}>
      <label>流转名称<input className="field" value={transition.name} maxLength={MAX_LIFECYCLE_NAME} disabled={disabled}
        aria-label={`流转${index + 1}名称`} onChange={(event) => updateTransition(index, { name: event.target.value })} /></label>
      <label>起点<select className="field" disabled={disabled} aria-label={`流转${index + 1}起点`} value={transition.from}
        onChange={(event) => updateTransition(index, { from: event.target.value })}>{stages.map((stage) => <option key={stage.id} value={stage.id}>{stage.name}</option>)}</select></label>
      <label>终点<select className="field" disabled={disabled} aria-label={`流转${index + 1}终点`} value={transition.to}
        onChange={(event) => updateTransition(index, { to: event.target.value })}>{stages.map((stage) => <option key={stage.id} value={stage.id}>{stage.name}</option>)}</select></label>
      <button type="button" className="button subtle model-icon-button" aria-label={`删除流转${index + 1}`} disabled={disabled}
        onClick={() => removeTransition(index)}><Trash2 size={16} /></button>
    </div>)}
  </>
}
