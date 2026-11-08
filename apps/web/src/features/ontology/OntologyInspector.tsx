import { ArrowUpRight, Box, Braces, Database, GitBranch, List, Search, Waypoints, Workflow } from 'lucide-react'
import type { Callable, DefinitionRecord, Kind, Organization, Property } from '../../lib/types'
import { collectDefinitionReferences } from './ontology-graph'
import './ontology-inspector.css'

type Props = {
  record?: DefinitionRecord
  records: DefinitionRecord[]
  categories: Organization[]
  groups: Organization[]
  categoryId: string
  onSelect: (id: string) => void
  onOpenDefinition: (id: string) => void
  onQuery: (schemaId: string) => void
}

const kindNames: Record<Kind, string> = { schema: '对象模型', enum: '枚举', link: '关系', action: '动作', function: '函数' }
const kindIcons = { schema: Box, enum: List, link: GitBranch, action: Workflow, function: Braces }
const languageNames = { python: 'Python', javascript: 'JavaScript', go: 'Go' }
const effectNames = { none: '无副作用计算', dataWrite: '数据写入声明', externalWrite: '外部写入声明' }

export function OntologyInspector({ record, records, categories, groups, categoryId, onSelect, onOpenDefinition, onQuery }: Props) {
  const tenantId = categories.find((category) => category.id === categoryId)?.tenantId
  const catalog = [...new Map(records.filter((item) => tenantId && item.status === 'active' && item.definition.tenantId === tenantId).map((item) => [item.definition.id, item])).values()]
  // Resolve the selection against the current authorized catalog. An old selected
  // object must not keep its name or details alive after the catalog is cleared.
  const selected = record && catalog.find((item) => item.definition.id === record.definition.id)
  if (!selected) return <div className="ontology-inspector ontology-inspector-empty"><Waypoints size={30} /><h2>选择一项本体资源</h2><p>点击目录、图中节点或关系，查看业务说明、属性与依赖。</p><small>这里展示当前生效定义，查看数据需进入数据探索后运行查询。</small></div>

  const { definition } = selected
  const Icon = kindIcons[definition.kind]
  const categoryName = (id: string) => categories.find((item) => item.tenantId === tenantId && item.id === id)?.name ?? '未加载分类'
  const groupName = groups.find((item) => item.tenantId === tenantId && item.categoryId === definition.categoryId && item.id === definition.groupId)?.name ?? '未加载分组'
  const resolve = (id: string, kind?: Kind) => catalog.find((item) => item.definition.id === id && (!kind || item.definition.kind === kind))
  const expectedKind = (kind: string): Kind => kind === 'enum' ? 'enum' : kind === 'function' ? 'function' : 'schema'
  const outgoing = collectDefinitionReferences(selected)
  const incoming = catalog.flatMap((item) => collectDefinitionReferences(item).filter((reference) => reference.targetId === definition.id && expectedKind(reference.kind) === definition.kind).map((reference) => ({ source: item, reference })))
  const links = catalog.filter((item) => item.definition.kind === 'link' && item.definition.link)

  function resource(id: string, kind?: Kind) {
    const target = resolve(id, kind)
    if (!target) return <span className="ontology-inspector-unavailable">当前不可查看</span>
    return <button type="button" className="ontology-inspector-resource" aria-label={`查看本体资源：${target.definition.name}`} onClick={() => onSelect(target.definition.id)}>
      <span>{target.definition.name}</span>{target.definition.categoryId !== categoryId && <small>{categoryName(target.definition.categoryId)}</small>}<ArrowUpRight size={13} />
    </button>
  }

  function properties(title: string, fields: Property[]) {
    return <section className="ontology-inspector-section"><h3>{title}<span>{fields.length}</span></h3>{!fields.length ? <p className="ontology-inspector-muted">未声明{title}</p> : <ul className="ontology-inspector-properties">{fields.map((field) => <li key={field.id}>
      <div className="ontology-inspector-property-heading"><strong>{field.name}</strong><code>{field.dataType}{field.dataType === 'decimal' && field.precision ? `(${field.precision},${field.scale ?? 0})` : ''}</code></div>
      <div className="ontology-inspector-property-meta">{field.required && <span>必填</span>}{field.nullable && <span>可为空</span>}{field.readOnly && <span>只读</span>}</div>
      {field.enumId && <div className="ontology-inspector-reference-value"><small>引用枚举</small>{resource(field.enumId, 'enum')}</div>}
      {field.mapping?.column && <p className="ontology-inspector-mapping">映射列 <code>{field.mapping.column}</code></p>}
    </li>)}</ul>}</section>
  }

  function callableDetails(callable: Callable, kind: 'action' | 'function') {
    return <>
      <section className="ontology-inspector-section"><h3>实现说明</h3><dl className="ontology-inspector-facts">
        <div><dt>实现方式</dt><dd>{callable.functionId ? '绑定函数' : callable.runtime ? '直接源码' : callable.artifactRef ? '产物引用' : '未配置实现'}</dd></div>
        {callable.functionId && <div><dt>实现函数</dt><dd>{resource(callable.functionId, 'function')}</dd></div>}
        {callable.runtime && <><div><dt>语言</dt><dd>{languageNames[callable.runtime.language]}</dd></div><div><dt>入口</dt><dd><code>{callable.runtime.entrypoint}</code></dd></div><div><dt>超时</dt><dd>{callable.runtime.timeoutSeconds} 秒</dd></div></>}
        <div><dt>副作用声明</dt><dd>{effectNames[callable.sideEffects]}</dd></div>
      </dl><p className="ontology-inspector-muted">本页仅查看声明，不发起调用。</p></section>
      {kind === 'action' && <section className="ontology-inspector-section"><h3>作用对象</h3>{callable.targetSchemaIds?.length ? <ul className="ontology-inspector-resource-list">{callable.targetSchemaIds.map((id) => <li key={id}>{resource(id, 'schema')}</li>)}</ul> : <p className="ontology-inspector-muted">未声明作用对象</p>}</section>}
      {properties('输入参数', callable.input ?? [])}{properties('输出参数', callable.output ?? [])}
    </>
  }

  return <article className="ontology-inspector" aria-label="本体资源详情">
    <header className="ontology-inspector-heading"><span className={`ontology-inspector-kind ontology-inspector-kind-${definition.kind}`}><Icon size={18} />{kindNames[definition.kind]}</span><span className="ontology-inspector-current">已生效</span><h2>{definition.name}</h2><p>{definition.description || '尚未填写业务说明。'}</p></header>
    <div className="ontology-inspector-actions"><button type="button" className="button" onClick={() => onOpenDefinition(definition.id)}>查看定义<ArrowUpRight size={14} /></button>{definition.kind === 'schema' && <button type="button" className="button subtle" onClick={() => onQuery(definition.id)}><Search size={14} />查看数据</button>}</div>
    <dl className="ontology-inspector-facts ontology-inspector-location"><div><dt>分类</dt><dd>{categoryName(definition.categoryId)}</dd></div><div><dt>分组</dt><dd>{groupName}</dd></div></dl>

    {definition.schema && <>
      <section className="ontology-inspector-section"><h3><Database size={15} />来源概要</h3><dl className="ontology-inspector-facts"><div><dt>建模方向</dt><dd>{definition.schema.mode === 'db_2_schema' ? '数据库为准' : '模型为准'}</dd></div><div><dt>来源方式</dt><dd>{definition.schema.storageBinding ? '显式来源映射' : '平台托管'}</dd></div>{definition.schema.storageBinding && <div><dt>主表</dt><dd><code>{[definition.schema.storageBinding.namespace, definition.schema.storageBinding.table].filter(Boolean).join('.')}</code></dd></div>}</dl><p className="ontology-inspector-muted">实际映射与数据源信息在定义页按权限查看。</p></section>
      {properties('属性', definition.schema.properties)}
      <section className="ontology-inspector-section"><h3>入站与出站关系</h3><ul className="ontology-inspector-relations">{links.flatMap((item) => {
        const link = item.definition.link!
        return [link.sourceSchemaId === definition.id ? { direction: 'out', label: link.forwardName, target: link.targetSchemaId } : undefined, link.targetSchemaId === definition.id ? { direction: 'in', label: link.reverseName, target: link.sourceSchemaId } : undefined].filter((value) => value !== undefined).map((value) => <li key={`${item.definition.id}:${value.direction}`}><div><span>{value.direction === 'out' ? '出站' : '入站'}</span><strong>{value.label}</strong><code>{value.direction === 'in' ? ({ '1:1': '1:1', '1:N': 'N:1', 'N:1': '1:N', 'N:M': 'N:M' }[link.cardinality]) : link.cardinality}</code></div>{resource(item.definition.id, 'link')}<small>关联对象</small>{resource(value.target, 'schema')}</li>)
      })}</ul>{!links.some((item) => [item.definition.link!.sourceSchemaId, item.definition.link!.targetSchemaId].includes(definition.id)) && <p className="ontology-inspector-muted">当前已加载范围没有可见关系。</p>}</section>
    </>}

    {definition.link && <section className="ontology-inspector-section"><h3>关系信息</h3><dl className="ontology-inspector-facts"><div><dt>来源对象</dt><dd>{resource(definition.link.sourceSchemaId, 'schema')}</dd></div><div><dt>目标对象</dt><dd>{resource(definition.link.targetSchemaId, 'schema')}</dd></div><div><dt>正向名称</dt><dd>{definition.link.forwardName}</dd></div><div><dt>反向名称</dt><dd>{definition.link.reverseName}</dd></div><div><dt>基数</dt><dd><code>{definition.link.cardinality}</code></dd></div><div><dt>事实来源</dt><dd>{!definition.link.storageBinding ? '仅定义 · 未配置事实映射' : definition.link.storageBinding.mode === 'managed' ? '平台托管关系表' : definition.link.storageBinding.mode === 'foreign_key' ? '外部外键 · 只读' : '外部关联表 · 只读'}</dd></div>{definition.link.storageBinding?.table && <div><dt>映射表</dt><dd><code>{[definition.link.storageBinding.namespace, definition.link.storageBinding.table].filter(Boolean).join('.')}</code></dd></div>}</dl></section>}

    {definition.enum && <section className="ontology-inspector-section"><h3>枚举值<span>{definition.enum.values.length}</span></h3><div className="ontology-inspector-table"><table aria-label="枚举成员"><thead><tr><th>编码</th><th>标签</th><th>状态</th></tr></thead><tbody>{definition.enum.values.map((value) => <tr key={value.code}><td><code>{value.code}</code></td><td>{value.label}</td><td><span className={value.deprecated ? 'ontology-inspector-deprecated' : ''}>{value.deprecated ? '已废弃' : '可用'}</span></td></tr>)}</tbody></table></div></section>}

    {definition.action && callableDetails(definition.action, 'action')}
    {definition.function && callableDetails(definition.function, 'function')}

    <section className="ontology-inspector-section"><h3>引用</h3>{outgoing.length ? <ul className="ontology-inspector-references">{outgoing.map((reference, index) => <li key={`${reference.kind}:${reference.targetId}:${reference.scope ?? ''}:${reference.propertyId ?? index}`}><small>{reference.label}</small>{resource(reference.targetId, expectedKind(reference.kind))}</li>)}</ul> : <p className="ontology-inspector-muted">当前定义没有显式引用。</p>}</section>
    <section className="ontology-inspector-section"><h3>{definition.kind === 'function' ? '绑定动作与其他引用' : '被引用'}</h3>{incoming.length ? <ul className="ontology-inspector-references">{incoming.map(({ source, reference }, index) => <li key={`${source.definition.id}:${reference.kind}:${reference.scope ?? ''}:${reference.propertyId ?? index}`}><small>{kindNames[source.definition.kind]} · {reference.label}</small>{resource(source.definition.id)}</li>)}</ul> : <p className="ontology-inspector-muted">当前已加载范围没有可见引用。</p>}<p className="ontology-inspector-scope">关系与依赖仅统计当前可见、已加载的定义。</p></section>
  </article>
}
