import type { Callable, DataWriteCapability, DefinitionRecord } from '../../lib/types'

const operations = [{ id: 'create', label: '创建' }, { id: 'update', label: '更新' }, { id: 'delete', label: '删除' }, { id: 'upsert', label: '创建或更新' }] as const

export function DataWriteCapabilitiesEditor({ callable, schemas, disabled, onChange }: {
  callable: Callable; schemas: DefinitionRecord[]; disabled: boolean; onChange: (value: Callable) => void
}) {
  if (callable.sideEffects !== 'dataWrite') return null
  const items = callable.dataWrite ?? []
  function update(index: number, patch: Partial<DataWriteCapability>) {
    onChange({ ...callable, dataWrite: items.map((item, offset) => offset === index ? { ...item, ...patch } : item) })
  }
  return <section aria-label="业务写入范围">
    <div className="model-section-heading"><div><h3>业务写入范围</h3><p className="muted">按稳定对象 ID 声明允许暂存的操作；运行者仍须拥有每条数据的行列权限。批量方法沿用四种基本操作声明。</p></div>
      <button type="button" className="button" disabled={disabled || items.length >= 32} onClick={() => onChange({ ...callable, dataWrite: [...items, { schemaId: '', operations: [] }] })}>添加写入对象</button></div>
    {!items.length && <p className="notice">至少声明一个对象和一种写操作后才能保存。</p>}
    {items.map((item, index) => <fieldset key={index} className="model-fieldset" disabled={disabled}><legend>写入对象 {index + 1}</legend>
      <label>对象模型<select className="field" aria-label={`写入对象${index + 1}`} value={item.schemaId} onChange={(event) => update(index, { schemaId: event.target.value })}>
        <option value="">选择已生效对象</option>
        {item.schemaId && !schemas.some((entry) => entry.definition.id === item.schemaId) && <option value={item.schemaId}>当前对象（无目录权限）</option>}
        {schemas.map(({ definition }) => <option key={definition.id} value={definition.id} disabled={items.some((other, position) => position !== index && other.schemaId === definition.id)}>{definition.name}</option>)}
      </select></label>
      <div className="model-inline-actions">{operations.map((operation) => <label key={operation.id} className="model-checkbox"><input type="checkbox" checked={item.operations.includes(operation.id)} onChange={(event) => update(index, { operations: event.target.checked ? [...item.operations, operation.id] : item.operations.filter((name) => name !== operation.id) })} />{operation.label}</label>)}</div>
      <button type="button" className="button subtle" onClick={() => onChange({ ...callable, dataWrite: items.filter((_, position) => position !== index) })}>移除此写入对象</button>
    </fieldset>)}
    <p className="muted">源码仅暂存计划，不读取数据、不立即写入、不返回新记录 ID。所有计划待输出校验通过后在一个事务内提交；无权、冲突或类型错误回滚整批。此能力仅供当前用户会话正式调用，MCP 和外部令牌不可调用。</p>
  </section>
}
