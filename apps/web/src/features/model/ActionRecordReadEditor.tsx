import type { Callable, DefinitionRecord } from '../../lib/types'

export function ActionRecordReadEditor({ action, schemas, locked, onChange }: {
  action: Callable
  schemas: DefinitionRecord[]
  locked: boolean
  onChange: (read: Callable['recordRead']) => void
}) {
  const available = schemas.filter(({ definition }) => definition.schema && !definition.schema.storageBinding && action.targetSchemaIds?.includes(definition.id))
  const input = (action.input ?? []).filter((parameter) => parameter.id && parameter.dataType === 'uuid' && parameter.required && !parameter.nullable && parameter.default === undefined)
  const read = action.recordRead
  const schema = available.find(({ definition }) => definition.id === read?.schemaId)?.definition.schema
  if (!read && (locked || !action.runtime || action.sideEffects !== 'none')) return null
  return <section className="model-editor-panel">
    <h3>读取当前记录</h3>
    <p className="muted">发布后按调用者权限读取声明字段。源码从输入「当前记录」按字段 ID 取值。</p>
    <div className="form-grid">
      <label>读取模型<select className="field" disabled={locked} value={read?.schemaId ?? ''} onChange={(event) => onChange(event.target.value ? { schemaId: event.target.value, instanceInputId: input[0]?.id ?? '', fieldIds: [] } : undefined)}>
        <option value="">不读取</option>
        {read && !schema && <option value={read.schemaId}>当前模型不可见</option>}
        {available.map(({ definition }) => <option key={definition.id} value={definition.id}>{definition.name}</option>)}
      </select></label>
      {read && <label>实例 ID 参数<select className="field" disabled={locked} value={read.instanceInputId} onChange={(event) => onChange({ ...read, instanceInputId: event.target.value })}>
        <option value="">选择已保存的必填 UUID 参数</option>
        {input.map((parameter) => <option key={parameter.id} value={parameter.id}>{parameter.name}</option>)}
      </select></label>}
    </div>
    {read && schema && <fieldset disabled={locked} className="model-fieldset"><legend>读取字段</legend>
      {schema.properties.filter((field) => field.dataType !== 'longtext').map((field) => {
        const selected = read.fieldIds.includes(field.id)
        return <label key={field.id}><input type="checkbox" checked={selected} disabled={!selected && read.fieldIds.length >= 16} onChange={(event) =>
          onChange({ ...read, fieldIds: event.target.checked ? [...read.fieldIds, field.id] : read.fieldIds.filter((id) => id !== field.id) })
        } />{field.name}</label>
      })}
    </fieldset>}
  </section>
}
