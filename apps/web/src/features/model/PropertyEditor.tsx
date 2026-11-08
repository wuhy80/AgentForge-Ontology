import { Plus, Trash2 } from 'lucide-react'
import type { DataType, DefinitionRecord, Property } from '../../lib/types'
import { PropertyDefaultEditor } from './PropertyDefaultEditor'

export const typeLabels: Record<string, string> = {
  string: '短文本', text: '长文本', longtext: '超长文本（4 MiB）', int64: '整数', decimal: '精确小数',
  boolean: '布尔值', timestamp: '日期时间', uuid: '唯一标识',
}

export function newProperty(): Property {
  return { id: '', name: '', dataType: 'string', required: false, nullable: false }
}

export function PropertyEditor({ title, properties, onChange, locked, enums, allowEnumBinding = true, allowAppend = false, maxProperties }: {
  title: string
  properties: Property[]
  onChange: (properties: Property[]) => void
  locked: boolean
  enums: DefinitionRecord[]
  allowEnumBinding?: boolean
  allowAppend?: boolean
  maxProperties?: number
}) {
  function update(index: number, patch: Partial<Property>) {
    onChange(properties.map((property, position) => position === index ? { ...property, ...patch } : property))
  }

  return <section className="model-fields">
    <div className="model-section-heading">
      <div><h3>{title}</h3><p className="muted">业务名称用于查询与表单，保存后自动建立稳定绑定。</p></div>
      {(!locked || allowAppend) && <button type="button" className="button subtle" disabled={maxProperties !== undefined && properties.length >= maxProperties} onClick={() => onChange([...properties, { ...newProperty(), nullable: allowAppend }])}><Plus size={15} />添加属性</button>}
    </div>
    {maxProperties !== undefined && <p className="muted model-field-boundary">当前 {properties.length} / {maxProperties} 个属性。</p>}
    {properties.length === 0 ? <div className="model-inline-empty">暂未设置{title}。</div> : <div className="table-wrap model-properties-table"><table>
      <thead><tr><th>业务名称</th><th>数据类型</th><th>约束与配置</th>{(!locked || allowAppend) && <th><span className="model-sr-only">操作</span></th>}</tr></thead>
      <tbody>{properties.map((property, index) => {
        const propertyLocked = locked && (!allowAppend || !!property.id)
        return <tr key={property.id || `new-${index}`}>
        <td><input className="field" aria-label={`${title}第${index + 1}项名称`} value={property.name} onChange={(event) => update(index, { name: event.target.value })} placeholder="例如：设备名称" maxLength={128} required /></td>
        <td><select className="field" aria-label={`${title}第${index + 1}项类型`} disabled={propertyLocked} value={property.dataType} onChange={(event) => {
          const dataType = event.target.value as DataType
          update(index, { dataType, precision: dataType === 'decimal' ? 18 : undefined, scale: dataType === 'decimal' ? 2 : undefined, enumId: undefined, default: undefined })
        }}>{Object.entries(typeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></td>
        <td><div className="model-property-options">
          <label className="model-checkbox"><input type="checkbox" disabled={propertyLocked || allowAppend} checked={!!property.required} onChange={(event) => update(index, { required: event.target.checked })} />必须提供</label>
          <label className="model-checkbox"><input type="checkbox" disabled={propertyLocked || allowAppend} checked={!!property.nullable} onChange={(event) => update(index, { nullable: event.target.checked })} />允许空值</label>
          {property.readOnly && <span className="badge">只读</span>}
          {property.dataType === 'longtext' && <span className="muted">最多 4 MiB（UTF-8 字节），不支持索引或排序</span>}
          {property.dataType === 'decimal' && <div className="model-decimal-config">
            <label>总位数<input className="field" type="number" aria-label={`${title}第${index + 1}项总位数`} disabled={propertyLocked} min={1} max={38} value={property.precision ?? 18} onChange={(event) => update(index, { precision: Number(event.target.value) })} /></label>
            <label>小数位<input className="field" type="number" aria-label={`${title}第${index + 1}项小数位`} disabled={propertyLocked} min={0} max={property.precision ?? 18} value={property.scale ?? 0} onChange={(event) => update(index, { scale: Number(event.target.value) })} /></label>
          </div>}
          {allowEnumBinding && property.dataType === 'string' && (enums.length > 0 || property.enumId) && <select className="field model-enum-select" aria-label={`${title}第${index + 1}项枚举`} disabled={propertyLocked} value={property.enumId ?? ''} onChange={(event) => update(index, { enumId: event.target.value || undefined, enumValues: undefined, enumEtag: undefined, default: undefined })}>
            <option value="">不绑定枚举</option>
            {property.enumId && !enums.some((entry) => entry.definition.id === property.enumId) && <option value={property.enumId}>当前已绑定枚举（无目录权限）</option>}
            {enums.map(({ definition }) => <option key={definition.id} value={definition.id}>{definition.name}</option>)}
          </select>}
          {!allowEnumBinding && property.enumId && <span className="model-inline-actions"><span className="muted">当前枚举绑定尚未支持</span>{!propertyLocked && <button type="button" className="button subtle" aria-label={`解除${title}第${index + 1}项枚举绑定`} onClick={() => update(index, { enumId: undefined })}>解除绑定</button>}</span>}
          <PropertyDefaultEditor property={property} label={`${title}第${index + 1}项默认值`} disabled={propertyLocked} enumValues={enums.find((entry) => entry.definition.id === property.enumId)?.definition.enum?.values} onChange={(patch) => update(index, patch)} />
        </div></td>
        {(!locked || allowAppend) && <td>{!propertyLocked && <button type="button" className="button subtle model-icon-button" aria-label={`删除${title}第${index + 1}项`} onClick={() => onChange(properties.filter((_, position) => position !== index))}><Trash2 size={16} /></button>}</td>}
      </tr>})}</tbody>
    </table></div>}
  </section>
}
