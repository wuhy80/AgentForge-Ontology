import { useId } from 'react'
import type { Property, Values } from '../../lib/types'
import { ValueInput } from '../query/InstanceDialog'
import { draftText, propertyValueText, type FieldDrafts } from '../query/query'

export function testCaseDrafts(properties: Property[], input: Values): FieldDrafts {
  return Object.fromEntries(properties.map((property) => [property.id, Object.hasOwn(input, property.id)
    ? { mode: input[property.id] === null ? 'null' : 'value', text: draftText(input[property.id]) }
    : { mode: 'omit', text: '' }]))
}

export function CallableInputs({ properties, drafts, onChange }: {
  properties: Property[]; drafts: FieldDrafts; onChange: (id: string, patch: Partial<FieldDrafts[string]>) => void
}) {
  const componentId = useId()
  return <div className="model-invocation-inputs">{properties.map((property, index) => {
    const draft = drafts[property.id] ?? { mode: 'omit', text: '' }
    const inputId = `${componentId}-input-${index}`
    return <div className="model-invocation-input" key={property.id}>
      <label htmlFor={inputId}>{property.name}<small className="muted">{property.dataType}</small></label>
      <select className="field" aria-label={`${property.name}的传入方式`} value={draft.mode} onChange={(event) => onChange(property.id, { mode: event.target.value as FieldDrafts[string]['mode'] })}>
        <option value="value">填写值</option><option value="omit">不传入 / 使用默认值</option>{property.nullable && <option value="null">显式 NULL</option>}
      </select>
      <ValueInput property={property} id={inputId} disabled={draft.mode !== 'value'} value={draft.text} onChange={(text) => onChange(property.id, { text })} />
      {draft.mode === 'omit' && Object.hasOwn(property, 'default') && <small className="muted">默认值：{propertyValueText(property, property.default)}</small>}
    </div>
  })}{!properties.length && <p className="muted">此定义没有输入参数。</p>}</div>
}
