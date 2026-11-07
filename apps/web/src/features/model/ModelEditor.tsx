import { useEffect, useState } from 'react'
import { AlertCircle, ArrowRight, Check, FileCode2, LoaderCircle, Plus, RefreshCw, Save, Send, Trash2 } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiError, type ApiClient } from '../../lib/api'
import type { Callable, Definition, DefinitionRecord, Index, Kind, Link, Organization, Preview, Publication } from '../../lib/types'
import { PropertyEditor } from './PropertyEditor'
import { activeEntries, cloneDefinition, kindDescriptions, kindLabels, MAX_SCHEMA_INDEXES, MAX_SCHEMA_PROPERTIES, newDefinition, validateDefinition } from './model-helpers'

export interface PendingPublication {
  record: DefinitionRecord
  publication: Publication
}

export function ModelEditor({ api, categoryId, categories, entries, initialRecord, initialKind, pending, onClose, onOrganizations, onSaved, onPending, onPublished, active = true }: {
  api: ApiClient
  categoryId: string
  categories: Organization[]
  entries: DefinitionRecord[]
  initialRecord?: DefinitionRecord
  initialKind: Kind
  pending?: PendingPublication
  onClose: () => void
  onOrganizations: () => void
  onSaved: (record: DefinitionRecord) => Promise<void>
  onPending: (pending: PendingPublication) => void
  onPublished: (record: DefinitionRecord) => Promise<void>
  active?: boolean
}) {
  const [record, setRecord] = useState(initialRecord)
  const [definition, setDefinition] = useState(() => initialRecord ? cloneDefinition(initialRecord.definition) : newDefinition(initialKind, categoryId))
  const [initialDefinition] = useState(() => initialRecord ? cloneDefinition(initialRecord.definition) : newDefinition(initialKind, categoryId))
  const [groups, setGroups] = useState<Organization[]>([])
  const [groupsLoading, setGroupsLoading] = useState(false)
  const [groupError, setGroupError] = useState('')
  const [busy, setBusy] = useState<'save' | 'preview' | 'publish' | 'reload' | ''>('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [conflict, setConflict] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const [publication, setPublication] = useState(pending?.publication)
  const [preview, setPreview] = useState<Preview>()
  const dirty = JSON.stringify(definition) !== JSON.stringify(record?.definition ?? initialDefinition)
  const locked = !!(record?.hasCurrent || record?.status === 'active')
  const uncertain = !!publication && !['succeeded', 'rejected'].includes(publication.status)
  const disabled = !!busy || uncertain
  const category = categories.find((item) => item.id === categoryId)
  const bindingReady = !!category && definition.categoryId === categoryId && (!!record || !groupsLoading && groups.some((group) => group.id === definition.groupId))
  const currentSchemas = activeEntries(entries, 'schema')
  const currentEnums = activeEntries(entries, 'enum')
  const currentFunctions = activeEntries(entries, 'function')

  useEffect(() => {
    if (!active) { setGroupsLoading(false); return }
    if (!categoryId) { setGroups([]); setGroupsLoading(false); setGroupError(''); return }
    const controller = new AbortController()
    setGroups([]); setGroupsLoading(true)
    setGroupError('')
    api.listGroups(categoryId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setGroups(items.filter((group) => group.categoryId === categoryId))
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setGroupError(cause instanceof Error ? cause.message : '分组读取失败')
    }).finally(() => { if (!controller.signal.aborted) setGroupsLoading(false) })
    return () => controller.abort()
  }, [api, categoryId, active])

  function update(patch: Partial<Definition>) { setDefinition((value) => ({ ...value, ...patch })); setMessage(''); setPreview(undefined) }
  function requestClose() {
    if (busy) return
    if (dirty && !uncertain) setConfirmClose(true)
    else onClose()
  }
  function report(cause: unknown) {
    setError(cause instanceof Error ? cause.message : '操作失败，请稍后重试。')
    if (cause instanceof ApiError && cause.status === 412) setConflict(true)
  }
  async function save() {
    if (!bindingReady) { setError('请先选择当前分类下的分组；如目录尚未建立，请前往「分类与分组」。'); return }
    const invalid = validateDefinition(definition)
    if (invalid) { setError(invalid); return }
    setBusy('save'); setError(''); setMessage('')
    try {
      const saved = record ? await api.saveDraft(record.definition.id, definition, record.etag) : await api.createDefinition(definition)
      saved.hasCurrent = locked
      setRecord(saved); setDefinition(cloneDefinition(saved.definition)); setConflict(false); setPreview(undefined); setPublication(undefined)
      setMessage('草稿已保存。可以继续配置索引、预览存储计划或发布。')
      try { await onSaved(saved) } catch { setMessage('草稿已保存，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }
  async function showPreview() {
    if (!record || dirty) return
    setBusy('preview'); setError(''); setMessage('')
    try { setPreview(await api.preview(record.definition.id)) } catch (cause) { report(cause) } finally { setBusy('') }
  }
  async function publish() {
    if (!record || dirty) return
    setBusy('publish'); setError(''); setMessage('')
    try {
      const completed = await api.publish(record.definition.id, record.etag)
      setPublication(completed)
      if (completed.status !== 'succeeded') {
        onPending({ record, publication: completed })
        setError('发布尚未完成，请保留当前记录并继续恢复。')
        return
      }
      const active = { ...record, status: 'active' as const, hasCurrent: true }
      setRecord(active); setDefinition(cloneDefinition(active.definition)); setMessage('发布成功，当前定义已生效。')
      try { await onPublished(active) } catch { setMessage('发布成功，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) {
      report(cause)
      if (cause instanceof ApiError && cause.publication) {
        setPublication(cause.publication)
        onPending({ record, publication: cause.publication })
      } else if (cause instanceof ApiError && (cause.status === 0 || cause.status >= 500) || !(cause instanceof ApiError)) {
        // A lost response does not establish whether Data was already prepared.
        const unknown: Publication = { id: publication?.id ?? '', tenantId: record.definition.tenantId, definitionId: record.definition.id, status: 'unknown', error: '响应未确认' }
        setPublication(unknown); onPending({ record, publication: unknown })
      }
    } finally { setBusy('') }
  }
  async function reload() {
    if (!record) return
    setBusy('reload'); setError(''); setConfirmReload(false)
    try {
      let latest: DefinitionRecord
      try { latest = await api.getDraft(record.definition.id) } catch (cause) {
        if (!(cause instanceof ApiError) || cause.status !== 404) throw cause
        latest = await api.getCurrent(record.definition.id)
      }
      latest.hasCurrent = locked || latest.status === 'active'
      setRecord(latest); setDefinition(cloneDefinition(latest.definition)); setConflict(false); setPreview(undefined); setMessage('已载入服务器最新内容。')
      try { await onSaved(latest) } catch { /* Editor already has the authoritative record. */ }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }

  function updateIndex(position: number, patch: Partial<Index>) {
    update({ schema: { ...definition.schema!, indexes: (definition.schema!.indexes ?? []).map((index, offset) => offset === position ? { ...index, ...patch } : index) } })
  }

  return <Dialog title={record ? definition.name || '编辑定义' : `新建${kindLabels[definition.kind]}`} description={kindDescriptions[definition.kind]} onClose={requestClose} wide>
    <div className="model-editor">
      <div className="model-editor-meta"><span className={`badge model-kind-${definition.kind}`}>{kindLabels[definition.kind]}</span><span className="badge">{record?.status === 'active' ? '当前生效' : '草稿'}</span>{dirty && <span className="model-unsaved">有未保存修改</span>}</div>
      {locked && <div className="notice model-lock-notice">此定义已生效。可修改名称、描述和显示标签；数据结构与绑定保持锁定。</div>}
      {uncertain && <div className="notice notice-error" role="status"><strong>发布结果待确认</strong><p>编辑已暂停。使用同一份已保存内容继续恢复；关闭窗口后可从模型页的待恢复记录重新进入。</p><span className="muted">阶段：{publication?.status === 'activation_pending' ? '已提交，等待激活' : publication?.status === 'prepare_failed' ? '准备未完成' : '等待确认'}</span></div>}
      {error && <div className="notice notice-error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      {conflict && <div className="notice model-conflict"><p>服务器内容已改变，本地编辑仍然保留。载入最新内容会放弃当前未保存的修改。</p><button type="button" className="button subtle" disabled={!!busy} onClick={() => setConfirmReload(true)}><RefreshCw size={15} />载入最新内容</button></div>}
      {confirmReload && <div className="notice notice-error"><p>确定放弃本地编辑并载入服务器最新内容？</p><div className="model-inline-actions"><button type="button" className="button primary" onClick={() => void reload()}>确认载入</button><button type="button" className="button subtle" onClick={() => setConfirmReload(false)}>保留本地编辑</button></div></div>}
      {confirmClose && <div className="notice notice-error" role="alert"><p>还有未保存的修改。关闭后这些修改将丢失。</p><div className="model-inline-actions"><button type="button" className="button subtle" onClick={() => setConfirmClose(false)}>继续编辑</button><button type="button" className="button" onClick={onClose}>放弃修改并关闭</button></div></div>}
      <fieldset disabled={disabled} className="model-fieldset">
        <div className="form-grid model-basics">
          <label>业务名称<input className="field" autoFocus value={definition.name} maxLength={128} onChange={(event) => update({ name: event.target.value })} placeholder="例如：生产设备" /></label>
          <label>类型<select className="field" disabled={!!record} value={definition.kind} onChange={(event) => setDefinition(newDefinition(event.target.value as Kind, categoryId, definition.groupId))}>{Object.entries(kindLabels).map(([kind, label]) => <option key={kind} value={kind}>{label}</option>)}</select></label>
          <label>分类<input className="field" readOnly value={category?.name ?? '未选择分类'} /></label>
          <label>分组<select className="field" disabled={!!record || groupsLoading || !category} value={definition.groupId} onChange={(event) => update({ groupId: event.target.value })}><option value="">{groupsLoading ? '正在读取分组…' : '选择分组'}</option>{record && definition.groupId && !groups.some((group) => group.id === definition.groupId) && <option value={definition.groupId}>当前分组</option>}{groups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select></label>
          <label className="model-span-full">描述<textarea className="field" rows={2} value={definition.description ?? ''} onChange={(event) => update({ description: event.target.value })} placeholder="说明这一业务定义的用途与边界" /></label>
        </div>
        {groupError && <div className="notice notice-error">{groupError}</div>}
        {!record && (!category || !groupsLoading && !groupError && !groups.length) && <div className="notice"><p>{category ? '当前分类还没有分组，请前往「分类与分组」创建分组后再新建定义。' : '请先在顶部选择当前分类，或前往「分类与分组」创建分类和分组。'}</p><button type="button" className="button subtle" onClick={onOrganizations}>前往分类与分组<ArrowRight size={15} /></button></div>}
        {definition.schema && <>
          <PropertyEditor title="对象属性" properties={definition.schema.properties} locked={locked} enums={currentEnums} allowEnumBinding={false} maxProperties={MAX_SCHEMA_PROPERTIES} onChange={(properties) => update({ schema: { ...definition.schema!, properties, indexes: (definition.schema!.indexes ?? []).filter((index) => index.fieldIds.every((id) => properties.some((property) => property.id === id))) } })} />
          <section><div className="model-section-heading"><div><h3>索引</h3><p className="muted">最多 {MAX_SCHEMA_INDEXES} 个普通或唯一索引，每个索引最多组合两个属性。</p></div>{!locked && <button type="button" className="button subtle" disabled={!definition.schema.properties.some((property) => property.id) || (definition.schema.indexes?.length ?? 0) >= MAX_SCHEMA_INDEXES} onClick={() => update({ schema: { ...definition.schema!, indexes: [...definition.schema!.indexes ?? [], { id: '', name: '', unique: false, fieldIds: [] }] } })}><Plus size={15} />添加索引</button>}</div>
            {definition.schema.properties.some((property) => !property.id) && <div className="notice model-index-hint">新增属性需先保存草稿，再在此选择属性建立索引。</div>}
            {(definition.schema.indexes ?? []).length === 0 && <div className="model-inline-empty">尚未设置索引。</div>}
            {(definition.schema.indexes ?? []).map((index, position) => <div className="model-index-row" key={index.id || `new-index-${position}`}>
              <label>索引名称<input className="field" value={index.name} aria-label={`索引${position + 1}名称`} onChange={(event) => updateIndex(position, { name: event.target.value })} placeholder="例如：设备编码唯一" /></label>
              <label>类型<select className="field" disabled={locked} value={index.unique ? 'unique' : 'normal'} onChange={(event) => updateIndex(position, { unique: event.target.value === 'unique' })}><option value="normal">普通索引</option><option value="unique">唯一索引</option></select></label>
              <label>第一个属性<select className="field" disabled={locked} aria-label={`索引${position + 1}第一个属性`} value={index.fieldIds[0] ?? ''} onChange={(event) => updateIndex(position, { fieldIds: [event.target.value, ...index.fieldIds.slice(1)].filter(Boolean) })}><option value="">选择属性</option>{definition.schema!.properties.filter((property) => property.id).map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}</select></label>
              <label>第二个属性<select className="field" disabled={locked || !index.fieldIds[0]} aria-label={`索引${position + 1}第二个属性`} value={index.fieldIds[1] ?? ''} onChange={(event) => updateIndex(position, { fieldIds: [index.fieldIds[0], event.target.value].filter(Boolean) })}><option value="">不组合</option>{definition.schema!.properties.filter((property) => property.id && property.id !== index.fieldIds[0]).map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}</select></label>
              {!locked && <button type="button" className="button subtle model-icon-button" aria-label={`删除索引${position + 1}`} onClick={() => update({ schema: { ...definition.schema!, indexes: definition.schema!.indexes!.filter((_, offset) => offset !== position) } })}><Trash2 size={16} /></button>}
            </div>)}
          </section>
        </>}
        {definition.enum && <section><div className="model-section-heading"><div><h3>枚举业务值</h3><p className="muted">业务编码保持稳定，显示标签用于业务界面。</p></div>{!locked && <button type="button" className="button subtle" onClick={() => update({ enum: { values: [...definition.enum!.values, { code: '', label: '', deprecated: false }] } })}><Plus size={15} />添加值</button>}</div><div className="table-wrap"><table><thead><tr><th>业务编码</th><th>显示标签</th><th>状态</th>{!locked && <th>操作</th>}</tr></thead><tbody>{definition.enum.values.map((value, position) => <tr key={position}><td><input className="field" disabled={locked} value={value.code} aria-label={`枚举值${position + 1}编码`} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, code: event.target.value } : entry) } })} placeholder="例如：RUNNING" /></td><td><input className="field" value={value.label} aria-label={`枚举值${position + 1}标签`} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, label: event.target.value } : entry) } })} placeholder="例如：运行中" /></td><td><label className="model-checkbox"><input type="checkbox" disabled={locked} checked={!!value.deprecated} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, deprecated: event.target.checked } : entry) } })} />停用值</label></td>{!locked && <td><button type="button" className="button subtle model-icon-button" aria-label={`删除枚举值${position + 1}`} onClick={() => update({ enum: { values: definition.enum!.values.filter((_, offset) => offset !== position) } })}><Trash2 size={16} /></button></td>}</tr>)}</tbody></table></div></section>}
        {definition.link && <section><div className="model-section-heading"><div><h3>关系两端</h3><p className="muted">仅选择已经生效的对象模型，支持同一模型之间的关系。</p></div></div><div className="form-grid">
          <label>来源模型<select className="field" disabled={locked} value={definition.link.sourceSchemaId} onChange={(event) => update({ link: { ...definition.link!, sourceSchemaId: event.target.value } })}><option value="">选择来源模型</option>{definition.link.sourceSchemaId && !currentSchemas.some((entry) => entry.definition.id === definition.link!.sourceSchemaId) && <option value={definition.link.sourceSchemaId}>当前来源模型（无目录权限）</option>}{currentSchemas.map(({ definition: schema }) => <option key={schema.id} value={schema.id}>{categories.find((category) => category.id === schema.categoryId)?.name} / {schema.name}</option>)}</select></label>
          <label>目标模型<select className="field" disabled={locked} value={definition.link.targetSchemaId} onChange={(event) => update({ link: { ...definition.link!, targetSchemaId: event.target.value } })}><option value="">选择目标模型</option>{definition.link.targetSchemaId && !currentSchemas.some((entry) => entry.definition.id === definition.link!.targetSchemaId) && <option value={definition.link.targetSchemaId}>当前目标模型（无目录权限）</option>}{currentSchemas.map(({ definition: schema }) => <option key={schema.id} value={schema.id}>{categories.find((category) => category.id === schema.categoryId)?.name} / {schema.name}</option>)}</select></label>
          <label>正向业务名称<input className="field" value={definition.link.forwardName} onChange={(event) => update({ link: { ...definition.link!, forwardName: event.target.value } })} placeholder="例如：所属车间" /></label>
          <label>反向业务名称<input className="field" value={definition.link.reverseName} onChange={(event) => update({ link: { ...definition.link!, reverseName: event.target.value } })} placeholder="例如：包含设备" /></label>
          <label>关系基数<select className="field" disabled={locked} value={definition.link.cardinality} onChange={(event) => update({ link: { ...definition.link!, cardinality: event.target.value as Link['cardinality'] } })}><option value="1:1">一对一</option><option value="1:N">一对多</option><option value="N:1">多对一</option><option value="N:M">多对多</option></select></label>
          <label>删除策略<select className="field" disabled value="restrict"><option value="restrict">有关联时限制删除</option></select></label>
        </div></section>}
        {(definition.action || definition.function) && (() => {
          const key = definition.action ? 'action' : 'function'
          const callable = definition[key]!
          return <><section><div className="model-section-heading"><div><h3>实现声明</h3><p className="muted">当前支持元数据登记与发现，暂未开放执行。</p></div></div><div className="form-grid"><label>产物引用<input className="field" disabled={locked} value={callable.artifactRef ?? ''} onChange={(event) => update({ [key]: { ...callable, artifactRef: event.target.value || undefined } })} placeholder="例如：registry.example.com/business/handler" /></label><label>副作用<select className="field" disabled={locked} value={callable.sideEffects} onChange={(event) => update({ [key]: { ...callable, sideEffects: event.target.value as Callable['sideEffects'] } })}><option value="none">无副作用</option><option value="dataWrite">写入业务数据</option><option value="externalWrite">修改外部系统</option></select></label>{definition.action && <label className="model-span-full">实现函数<select className="field" disabled={locked} value={callable.functionId ?? ''} onChange={(event) => update({ action: { ...callable, functionId: event.target.value || undefined } })}><option value="">暂不绑定函数</option>{callable.functionId && !currentFunctions.some((entry) => entry.definition.id === callable.functionId) && <option value={callable.functionId}>当前实现函数（无目录权限）</option>}{currentFunctions.map(({ definition: fn }) => <option key={fn.id} value={fn.id}>{fn.name}</option>)}</select></label>}</div></section><PropertyEditor title="输入参数" properties={callable.input ?? []} enums={currentEnums} locked={locked} onChange={(input) => update({ [key]: { ...callable, input } })} /><PropertyEditor title="输出参数" properties={callable.output ?? []} enums={currentEnums} locked={locked} onChange={(output) => update({ [key]: { ...callable, output } })} /></>
        })()}
      </fieldset>
      {preview && <section className="model-preview"><div className="model-section-heading"><div><h3><FileCode2 size={17} />存储计划预览</h3><p className="muted">预览不会执行 SQL，也不会占用发布状态。</p></div></div>{preview.statements?.length ? <pre>{preview.statements.join('\n\n')}</pre> : <p className="model-inline-empty">此变更没有需要执行的存储语句。</p>}</section>}
      <div className="model-editor-footer"><span className="muted">{dirty ? '先保存草稿，再预览或发布。' : record?.status === 'active' ? '当前内容已生效，可再次确认或恢复发布。' : '草稿已保存，发布后对业务生效。'}</span><div className="model-inline-actions"><button type="button" className="button subtle" onClick={requestClose} disabled={!!busy}>关闭</button><button type="button" className="button" disabled={!!busy || !record || dirty || uncertain} onClick={() => void showPreview()}>{busy === 'preview' ? <LoaderCircle className="model-spin" size={16} /> : <FileCode2 size={16} />}预览计划</button>{!uncertain && <button type="button" className="button" disabled={!!busy || !dirty || conflict || !bindingReady} onClick={() => void save()}>{busy === 'save' ? <LoaderCircle className="model-spin" size={16} /> : <Save size={16} />}保存草稿</button>}<button type="button" className="button primary" disabled={!!busy || !record || dirty || conflict} onClick={() => void publish()}>{busy === 'publish' ? <LoaderCircle className="model-spin" size={16} /> : uncertain ? <RefreshCw size={16} /> : <Send size={16} />}{uncertain ? '继续恢复发布' : record?.status === 'active' ? '确认生效状态' : '发布定义'}<ArrowRight size={15} /></button></div></div>
    </div>
  </Dialog>
}
