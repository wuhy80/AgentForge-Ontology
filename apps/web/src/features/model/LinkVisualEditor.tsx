import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { AlertCircle, ArrowLeft, ArrowRight, Check, FileCode2, GitBranch, LoaderCircle, RefreshCw, Save, Send } from 'lucide-react'
import { ApiError } from '../../lib/api'
import type { Definition, DefinitionRecord, Link, Organization, Preview, Publication } from '../../lib/types'
import type { ModelEditorProps } from './ModelEditor'
import { LinkStorageEditor, validateLinkStorage } from './LinkStorageEditor'
import { LinkDataPreview } from './LinkDataPreview'
import { RelationGraph } from './RelationGraph'
import { cloneDefinition, newDefinition, validateDefinition } from './model-helpers'
import './link-editor.css'

const tabs = [{ id: 'business', name: '业务定义' }, { id: 'mapping', name: '数据映射' }, { id: 'data', name: '数据预览' }] as const
type LinkTab = typeof tabs[number]['id']

function initialValue(props: ModelEditorProps): Definition {
  if (props.initialRecord) return cloneDefinition(props.initialRecord.definition)
  const value = newDefinition('link', props.categoryId)
  if (props.initialLink) value.link = { ...value.link!, ...props.initialLink }
  return value
}

export function LinkVisualEditor(props: ModelEditorProps) {
  const { api, categoryId, categories, entries, currentEntries, initialRecord, pending, onClose, onOrganizations, onSources, onSaved, onPending, onPublished, onNavigationGuard, onGraph, layoutScope, embedded = false, active = true } = props
  const [initialDefinition] = useState(() => initialValue(props))
  const [definition, setDefinition] = useState(() => initialValue(props))
  const [record, setRecord] = useState(initialRecord)
  const [groups, setGroups] = useState<Organization[]>([])
  const [groupsLoading, setGroupsLoading] = useState(false)
  const [groupError, setGroupError] = useState('')
  const [tab, setTab] = useState<LinkTab>('business')
  const [view, setView] = useState<'business' | 'mapping'>('business')
  const [neighbors, setNeighbors] = useState(false)
  const [reverseView, setReverseView] = useState(false)
  const [selectedSchema, setSelectedSchema] = useState<string>()
  const [busy, setBusy] = useState<'save' | 'preview' | 'publish' | 'reload' | ''>('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [conflict, setConflict] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const [publication, setPublication] = useState(pending?.publication)
  const [preview, setPreview] = useState<Preview>()
  const tabId = useId()
  const dirty = JSON.stringify(definition) !== JSON.stringify(record?.definition ?? initialDefinition)
  const locked = !!(record?.hasCurrent || record?.status === 'active')
  const uncertain = !!publication && !['succeeded', 'rejected'].includes(publication.status)
  const disabled = !!busy || uncertain
  const category = categories.find((item) => item.id === categoryId)
  const tenantId = definition.tenantId || category?.tenantId || ''
  const schemas = (currentEntries ?? entries).filter((item) => item.status === 'active' && item.definition.tenantId === tenantId && item.definition.schema)
  const directoryCurrent = (currentEntries ?? entries).find((item) => item.status === 'active' && item.definition.tenantId === tenantId && item.definition.id === definition.id && item.definition.link)
  const current = directoryCurrent ?? (!currentEntries && record?.status === 'active' ? record : undefined)
  const storageLocked = locked && !!(record?.status === 'active' ? record.definition.link?.storageBinding : current?.definition.link?.storageBinding ?? (initialRecord?.status === 'active' ? initialRecord.definition.link?.storageBinding : undefined))
  const bindingReady = !!category && definition.categoryId === categoryId && (!!record || !groupsLoading && groups.some((group) => group.id === definition.groupId))
  const link = definition.link!
  const source = schemas.find((item) => item.definition.id === link.sourceSchemaId)
  const target = schemas.find((item) => item.definition.id === link.targetSchemaId)
  const changeRef = useRef(props.onDefinitionChange)
  changeRef.current = props.onDefinitionChange
  useEffect(() => { changeRef.current?.(definition, dirty) }, [definition, dirty])

  useEffect(() => {
    if (!active || !categoryId) return
    const controller = new AbortController()
    setGroupsLoading(true); setGroupError(''); setGroups([])
    api.listGroups(categoryId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setGroups(items.filter((item) => item.categoryId === categoryId && item.tenantId === tenantId))
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setGroupError(cause instanceof Error ? cause.message : '分组读取失败。') })
      .finally(() => { if (!controller.signal.aborted) setGroupsLoading(false) })
    return () => controller.abort()
  }, [api, active, categoryId, tenantId])

  useEffect(() => {
    if (!active) return
    const guard = () => {
      if (busy) return false
      if (!dirty || uncertain) return true
      if (!window.confirm('还有未保存的关系修改，离开后将丢失。确定离开？')) return false
      setDefinition(cloneDefinition(record?.definition ?? initialDefinition))
      return true
    }
    const beforeUnload = (event: BeforeUnloadEvent) => { if (dirty || busy) { event.preventDefault(); event.returnValue = '' } }
    onNavigationGuard?.(guard)
    window.addEventListener('beforeunload', beforeUnload)
    return () => { onNavigationGuard?.(); window.removeEventListener('beforeunload', beforeUnload) }
  }, [active, busy, dirty, uncertain, record, initialDefinition, onNavigationGuard])

  function update(patch: Partial<Definition>) { setDefinition((value) => ({ ...value, ...patch })); setMessage(''); setPreview(undefined) }
  function updateLink(patch: Partial<Link>) { update({ link: { ...link, ...patch } }) }
  function report(cause: unknown) {
    setError(cause instanceof Error ? cause.message : '操作失败，请稍后重试。')
    if (cause instanceof ApiError && cause.status === 412) setConflict(true)
  }
  async function save() {
    if (disabled) return
    if (!bindingReady) { setTab('business'); setError('请先选择当前分类下的已有分组。'); return }
    const invalid = validateDefinition(definition) ?? validateLinkStorage(link, schemas)
    if (invalid) { setError(invalid); setTab(/数据源|关联表|外键|跨数据源|平台默认存储/.test(invalid) ? 'mapping' : 'business'); return }
    setBusy('save'); setError(''); setMessage('')
    try {
      const saved = record ? await api.saveDraft(record.definition.id, definition, record.etag) : await api.createDefinition(definition)
      saved.hasCurrent = locked
      setRecord(saved); setDefinition(cloneDefinition(saved.definition)); setConflict(false); setPreview(undefined); setPublication(undefined)
      setMessage('草稿已保存。预览并发布后，关系定义才会生效。')
      try { await onSaved(saved) } catch { setMessage('草稿已保存，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }
  async function showPreview() {
    if (!record || dirty || disabled) return
    setBusy('preview'); setError(''); setMessage('')
    try { setPreview(await api.preview(record.definition.id)) } catch (cause) { report(cause) } finally { setBusy('') }
  }
  async function publish() {
    if (!record || dirty || busy || conflict) return
    setBusy('publish'); setError(''); setMessage('')
    try {
      const completed = await api.publish(record.definition.id, record.etag)
      setPublication(completed)
      if (completed.status !== 'succeeded') { onPending({ record, publication: completed }); setError('发布尚未完成，请保留当前记录并继续恢复。'); return }
      const published: DefinitionRecord = { ...record, status: 'active', hasCurrent: true }
      setRecord(published); setDefinition(cloneDefinition(published.definition)); setMessage('发布成功，当前定义已生效。')
      try { await onPublished(published) } catch { setMessage('发布成功，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) {
      report(cause)
      if (cause instanceof ApiError && cause.publication) { setPublication(cause.publication); onPending({ record, publication: cause.publication }) }
      else if (!(cause instanceof ApiError) || cause.status === 0 || cause.status >= 500) {
        const unknown: Publication = { id: publication?.id ?? '', tenantId: record.definition.tenantId, definitionId: record.definition.id, status: 'unknown', error: '响应未确认' }
        setPublication(unknown); onPending({ record, publication: unknown })
      }
    } finally { setBusy('') }
  }
  async function reload() {
    if (!record || busy) return
    setBusy('reload'); setError(''); setConfirmReload(false)
    try {
      let latest: DefinitionRecord
      try { latest = await api.getDraft(record.definition.id) } catch (cause) { if (!(cause instanceof ApiError) || cause.status !== 404) throw cause; latest = await api.getCurrent(record.definition.id) }
      latest.hasCurrent = locked || latest.status === 'active'
      setRecord(latest); setDefinition(cloneDefinition(latest.definition)); setConflict(false); setPreview(undefined); setMessage('已载入服务器最新内容。')
      try { await onSaved(latest) } catch { /* The authoritative record is already present. */ }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }
  function leave(action: () => void) {
    if (busy) return
    if (dirty && !uncertain) { if (action === onClose) setConfirmClose(true); else if (window.confirm('还有未保存的关系修改，离开后将丢失。确定离开？')) action() }
    else action()
  }
  function chooseTab(value: LinkTab) { setTab(value); if (value !== 'data') setView(value) }
  function navigateTabs(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
    if (next < 0) return
    event.preventDefault(); chooseTab(tabs[next].id); document.getElementById(`${tabId}-${tabs[next].id}`)?.focus()
  }
  const displayId = definition.id || 'unsaved-relation'
  const displayRecord: DefinitionRecord = { definition: { ...definition, id: displayId, tenantId }, status: dirty || record?.status !== 'active' ? 'draft' : 'active', etag: record?.etag ?? 'unsaved', hasCurrent: locked }
  const neighboringLinks = neighbors ? (currentEntries ?? entries).filter((item) => item.status === 'active' && item.definition.tenantId === tenantId && item.definition.id !== definition.id && item.definition.link && [link.sourceSchemaId, link.targetSchemaId].some((id) => item.definition.link!.sourceSchemaId === id || item.definition.link!.targetSchemaId === id)) : []
  const graphSchemas = schemas.filter((item) => !record || neighbors || item.definition.id === link.sourceSchemaId || item.definition.id === link.targetSchemaId)
  const displayLink = reverseView ? { ...displayRecord, definition: { ...displayRecord.definition, link: { ...link, sourceSchemaId: link.targetSchemaId, targetSchemaId: link.sourceSchemaId, forwardName: link.reverseName, reverseName: link.forwardName, cardinality: ({ '1:N': 'N:1', 'N:1': '1:N' } as Partial<Record<Link['cardinality'], Link['cardinality']>>)[link.cardinality] ?? link.cardinality } } } : displayRecord
  const selected = schemas.find((item) => item.definition.id === selectedSchema)
  const formatSchema = (item: DefinitionRecord) => `${categories.find((entry) => entry.id === item.definition.categoryId && entry.tenantId === tenantId)?.name ?? '其他分类'} / ${item.definition.name}`

  return <section className={`model-editor-page link-visual-editor ${embedded ? 'link-editor-embedded' : ''}`} aria-label="模型编辑">
    {!embedded && <div className="model-editor-page-heading"><div><button type="button" className="button subtle model-back-button" disabled={!!busy} onClick={() => leave(onClose)}><ArrowLeft size={16} />返回模型列表</button><h1>{record ? definition.name || '编辑关系' : '新建关系'}</h1><p className="muted">连接业务对象，配置关系语义与真实数据来源。</p></div>{onGraph && <button type="button" className="button" disabled={!!busy} onClick={() => leave(onGraph)}><GitBranch size={15} />打开分类关系图</button>}</div>}
    <div className="model-editor link-editor-body">
      <div className="model-editor-meta"><span className="badge model-kind-link">关系</span><span className="badge">{record?.status === 'active' ? '当前生效' : locked ? '待发布变更' : '草稿'}</span><span className="badge">{link.storageBinding ? link.storageBinding.mode === 'managed' ? '平台托管' : '外部只读' : '仅定义'}</span>{dirty && <span className="model-unsaved">有未保存修改</span>}</div>
      {locked && <div className="notice model-lock-notice">{storageLocked ? '此关系已生效。两端、基数、删除策略与数据绑定保持锁定；可修改中文名称和描述。' : '此关系定义已生效。可补充关系存储映射；关系两端、基数及删除策略保持锁定。'}</div>}
      {uncertain && <div className="notice notice-error" role="status"><strong>发布结果待确认</strong><p>编辑已暂停。请使用同一份已保存内容继续恢复发布。</p><span className="muted">阶段：{publication?.status}</span></div>}
      {error && <div className="notice notice-error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      {conflict && <div className="notice model-conflict"><p>服务器内容已改变，本地编辑仍然保留。载入最新内容会放弃当前未保存修改。</p><button type="button" className="button subtle" disabled={!!busy} onClick={() => setConfirmReload(true)}><RefreshCw size={15} />载入最新内容</button></div>}
      {confirmReload && <div className="notice notice-error"><p>确定放弃本地编辑并载入服务器最新内容？</p><div className="model-inline-actions"><button type="button" className="button primary" onClick={() => void reload()}>确认载入</button><button type="button" className="button subtle" onClick={() => setConfirmReload(false)}>保留本地编辑</button></div></div>}
      {confirmClose && <div className="notice notice-error" role="alert"><p>还有未保存的关系修改。返回列表后这些修改将丢失。</p><div className="model-inline-actions"><button type="button" className="button subtle" onClick={() => setConfirmClose(false)}>继续编辑</button><button type="button" className="button" onClick={onClose}>放弃修改并返回</button></div></div>}
      <div className="link-editor-workspace">
        {!embedded && <div className="link-editor-canvas"><div className="link-canvas-toolbar"><div className="link-view-switch" role="group" aria-label="画布内容"><button type="button" aria-pressed={view === 'business'} onClick={() => setView('business')}>业务关系</button><button type="button" aria-pressed={view === 'mapping'} onClick={() => { setView('mapping'); setTab('mapping') }}>数据映射</button></div>{view === 'business' && <><label className="link-neighbors"><input type="checkbox" checked={neighbors} onChange={(event) => setNeighbors(event.target.checked)} />展开周边关系</label><button type="button" className="button subtle" onClick={() => setReverseView((value) => !value)}>{reverseView ? '恢复正向查看' : '反向查看'}</button></>}</div>
          {view === 'business' ? <RelationGraph schemas={graphSchemas} links={[...neighboringLinks, displayLink]} categories={categories} categoryId={categoryId} selectedLinkId={displayId} disabled={disabled} onSelectLink={() => { setSelectedSchema(undefined); chooseTab('business') }} onSelectSchema={(item) => setSelectedSchema(item.definition.id)} onConnect={!locked && !reverseView && !disabled ? (sourceSchemaId, targetSchemaId) => updateLink({ sourceSchemaId, targetSchemaId, storageBinding: undefined }) : undefined} layoutKey={layoutScope ? `${layoutScope}:${categoryId}:link:${displayId}` : undefined} /> : <PhysicalLinkDiagram link={link} source={source} target={target} />}
          {selected && view === 'business' && <div className="link-selected-schema"><strong>{selected.definition.name}</strong><span>{formatSchema(selected)}</span><a href={`#models/${encodeURIComponent(selected.definition.id)}`}>查看对象定义<ArrowRight size={13} /></a></div>}
          <p className="link-canvas-help muted">{view === 'business' ? locked ? '选中连线编辑当前关系；拖动节点调整布局。周边关系仅用于查看。' : '从对象连接点拖线，或在右侧选择两端对象，配置后保存草稿。' : '物理映射依据所选真实表列展示，业务方向与外键指向分别表达。'}</p>
        </div>}
        <div className="link-editor-config"><div className="model-editor-tabs link-editor-tabs" role="tablist" aria-label="关系配置">{tabs.map((item, index) => <button type="button" role="tab" key={item.id} id={`${tabId}-${item.id}`} aria-controls={`${tabId}-panel-${item.id}`} aria-selected={tab === item.id} tabIndex={tab === item.id ? 0 : -1} onClick={() => chooseTab(item.id)} onKeyDown={(event) => navigateTabs(event, index)}>{item.name}</button>)}</div>
          <fieldset disabled={disabled} className="model-fieldset">
            <section role="tabpanel" hidden={tab !== 'business'} id={`${tabId}-panel-business`} aria-labelledby={`${tabId}-business`} className="link-config-panel">
              <div className="form-grid"><label>业务名称<input className="field" value={definition.name} maxLength={128} placeholder="例如：笔记产生引用" onChange={(event) => update({ name: event.target.value })} /></label><label>类型<input className="field" value="关系" readOnly /></label><label>分类<input className="field" readOnly value={category?.name ?? '未选择分类'} /></label><label>分组<select className="field" value={definition.groupId} disabled={!!record || groupsLoading || !category} onChange={(event) => update({ groupId: event.target.value })}><option value="">{groupsLoading ? '正在读取分组…' : '选择分组'}</option>{record && definition.groupId && !groups.some((group) => group.id === definition.groupId) && <option value={definition.groupId}>当前分组</option>}{groups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select></label><label className="model-span-full">描述<textarea className="field" rows={2} value={definition.description ?? ''} onChange={(event) => update({ description: event.target.value })} /></label></div>
              {groupError && <div className="notice notice-error">{groupError}</div>}
              {!record && !groupsLoading && !groups.length && <div className="notice"><p>请先在「分类与分组」建立目录。</p><button type="button" className="button subtle" onClick={onOrganizations}>前往分类与分组</button></div>}
              <div className="model-section-heading"><div><h3>关系两端</h3><p className="muted">选择已生效的对象模型，允许同一对象自关联。</p></div></div>
              <div className="form-grid"><label>来源模型<select className="field" disabled={locked} value={link.sourceSchemaId} onChange={(event) => updateLink({ sourceSchemaId: event.target.value, storageBinding: undefined })}><option value="">选择来源模型</option>{link.sourceSchemaId && !source && <option value={link.sourceSchemaId}>当前来源模型（目录不可见）</option>}{schemas.map((item) => <option key={item.definition.id} value={item.definition.id}>{formatSchema(item)}</option>)}</select></label><label>目标模型<select className="field" disabled={locked} value={link.targetSchemaId} onChange={(event) => updateLink({ targetSchemaId: event.target.value, storageBinding: undefined })}><option value="">选择目标模型</option>{link.targetSchemaId && !target && <option value={link.targetSchemaId}>当前目标模型（目录不可见）</option>}{schemas.map((item) => <option key={item.definition.id} value={item.definition.id}>{formatSchema(item)}</option>)}</select></label><label>正向业务名称<input className="field" value={link.forwardName} onChange={(event) => updateLink({ forwardName: event.target.value })} placeholder="例如：产生引用" /></label><label>反向业务名称<input className="field" value={link.reverseName} onChange={(event) => updateLink({ reverseName: event.target.value })} placeholder="例如：源自笔记" /></label><label>关系基数<select className="field" disabled={locked} value={link.cardinality} onChange={(event) => updateLink({ cardinality: event.target.value as Link['cardinality'] })}><option value="1:1">一对一</option><option value="1:N">一对多</option><option value="N:1">多对一</option><option value="N:M">多对多</option></select></label><label>删除策略<select className="field" disabled={locked} value={link.onDelete} onChange={(event) => updateLink({ onDelete: event.target.value })}><option value="restrict">有关联时限制删除</option></select></label></div>
              <p className="link-cardinality-hint">{cardinalityText(link, source?.definition.name ?? '来源对象', target?.definition.name ?? '目标对象')}</p>
              {!link.storageBinding && <div className="notice link-definition-only">此关系尚未配置数据存储。请在“数据映射”中完成配置，发布后才能查询真实关联数据。</div>}
            </section>
            <section role="tabpanel" hidden={tab !== 'mapping'} id={`${tabId}-panel-mapping`} aria-labelledby={`${tabId}-mapping`} className="link-config-panel">{source && target ? <LinkStorageEditor api={api} link={link} schemas={schemas} locked={storageLocked} active={active && tab === 'mapping'} onSources={onSources} onChange={(value) => update({ link: value })} /> : <div className="notice">请先选择两个可见的已生效对象模型，才可配置或查看物理映射。</div>}</section>
          </fieldset>
          <section role="tabpanel" hidden={tab !== 'data'} id={`${tabId}-panel-data`} aria-labelledby={`${tabId}-data`} className="link-config-panel">{active && tab === 'data' && !uncertain && !busy && <LinkDataPreview api={api} current={current} schemas={schemas} draft={dirty || record?.status !== 'active'} />}</section>
        </div>
      </div>
      {preview && <section className="model-preview"><div className="model-section-heading"><div><h3><FileCode2 size={17} />存储计划预览</h3><p className="muted">预览不会执行 SQL。</p></div></div>{preview.statements?.length ? <pre>{preview.statements.join('\n\n')}</pre> : <p className="model-inline-empty">此变更没有需要执行的存储语句。</p>}</section>}
      <div className="model-editor-footer link-editor-footer"><span className="muted">{dirty ? '修改仅在画布预览，保存草稿后再发布。' : record?.status === 'active' ? '当前关系已生效。' : '草稿保存后，预览并发布关系。'}</span><div className="model-inline-actions"><button type="button" className="button" disabled={!!busy || !record || dirty || uncertain} onClick={() => void showPreview()}>{busy === 'preview' ? <LoaderCircle size={15} className="model-spin" /> : <FileCode2 size={15} />}预览计划</button>{!uncertain && <button type="button" className="button" disabled={!!busy || !dirty || conflict || !bindingReady} onClick={() => void save()}>{busy === 'save' ? <LoaderCircle size={15} className="model-spin" /> : <Save size={15} />}保存草稿</button>}<button type="button" className="button primary" disabled={!!busy || !record || dirty || conflict} onClick={() => void publish()}>{busy === 'publish' ? <LoaderCircle size={15} className="model-spin" /> : uncertain ? <RefreshCw size={15} /> : <Send size={15} />}{uncertain ? '继续恢复发布' : record?.status === 'active' ? '确认生效状态' : '发布定义'}</button></div></div>
    </div>
  </section>
}

function cardinalityText(link: Link, source: string, target: string) {
  const [left, right] = link.cardinality.split(':')
  return `每个${source}最多关联${right === '1' ? '一个' : '多个'}${target}；每个${target}最多关联${left === '1' ? '一个' : '多个'}${source}。`
}

function PhysicalLinkDiagram({ link, source, target }: { link: Link; source?: DefinitionRecord; target?: DefinitionRecord }) {
  const binding = link.storageBinding
  const key = (item?: DefinitionRecord) => item?.definition.schema?.storageBinding ? `${item.definition.schema.storageBinding.table}.${item.definition.schema.storageBinding.idColumn || 'id'}` : item ? '平台对象主键' : '端点目录不可见'
  if (!source || !target) return <div className="link-physical-canvas"><GitBranch size={34} /><h3>端点信息尚不可用</h3><p>选择两个可见的已生效对象后，再查看物理映射。</p></div>
  if (!binding) return <div className="link-physical-canvas"><GitBranch size={34} /><h3>尚未连接关系数据</h3><p>在右侧选择存储方式，配置后预览映射。</p></div>
  let cards: { title: string; detail: string }[]
  if (binding.mode === 'foreign_key') {
    const holder = binding.foreignKeySide === 'target' ? target : source
    const referenced = binding.foreignKeySide === 'target' ? source : target
    cards = [{ title: holder?.definition.name ?? '受限端点', detail: holder ? `${holder.definition.schema?.storageBinding?.table ?? '对象表'}.${binding.foreignKeyColumn || '待选择外键列'}` : '物理信息不可见' }, { title: referenced?.definition.name ?? '受限端点', detail: key(referenced) }]
  } else cards = [{ title: source?.definition.name ?? '受限端点', detail: key(source) }, { title: binding.mode === 'managed' ? '平台托管关系表' : binding.table || '待选择关联表', detail: binding.mode === 'managed' ? '正反向共用同一份事实' : `${binding.sourceColumn || '来源列'} / ${binding.targetColumn || '目标列'}` }, { title: target?.definition.name ?? '受限端点', detail: key(target) }]
  return <div className="link-physical-canvas"><span className="badge">{binding.mode === 'managed' ? '平台托管映射' : '外部只读映射'}</span><h3>{binding.mode === 'foreign_key' ? '外键列指向被引用的主键' : '对象与关联表的对应关系'}</h3><div className="link-physical-chain">{cards.map((card, index) => <div className="link-physical-step" key={index}>{index > 0 && <ArrowRight size={20} />}<div className="link-physical-card"><strong>{card.title}</strong><code>{card.detail}</code></div></div>)}</div><p>修改右侧映射配置后图示同步更新，发布前由服务端核验真实表列和约束。</p></div>
}
