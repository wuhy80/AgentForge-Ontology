import type { EnumValue, Property } from '../../lib/types'
import { ValueInput } from '../query/InstanceDialog'
import { draftText } from '../query/query'

export function PropertyDefaultEditor({ property, onChange, label, disabled, enumValues }: {
  property: Property; onChange: (patch: Partial<Property>) => void; label: string; disabled: boolean; enumValues?: EnumValue[]
}) {
  const mode = property.default === undefined ? 'omit' : property.default === null ? 'null' : 'value'
  return <div className="model-property-default">
    <label>{label}方式<select className="field" aria-label={`${label}方式`} disabled={disabled} value={mode} onChange={(event) => {
      const next = event.target.value
      onChange({ default: next === 'omit' ? undefined : next === 'null' ? null : property.dataType === 'boolean' ? false : ['int64', 'decimal'].includes(property.dataType) ? '0' : '' })
    }}><option value="omit">无默认值</option><option value="value">设置默认值</option>{property.nullable && <option value="null">默认空值 NULL</option>}</select></label>
    {mode === 'value' && <label>{label}<ValueInput property={{ ...property, enumValues: enumValues ?? property.enumValues }} id={`${label}-value`} value={draftText(property.default)} disabled={disabled} onChange={(value) => onChange({ default: property.dataType === 'boolean' ? value === 'true' : value })} /></label>}
    {mode === 'null' && !property.nullable && <p className="notice notice-error">非空属性不能使用 NULL 默认值，请调整默认值。</p>}
  </div>
}
