import { lazy, Suspense, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { AlertCircle, ArrowLeft, ArrowRight, Check, FileCode2, LoaderCircle, Plus, RefreshCw, Save, Send, Trash2 } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { Definition, DefinitionRecord, Index, Invocation, Kind, Link, MetadataCapabilities, Organization, Preview, Publication, SourceDiagnostic } from '../../lib/types'
import { PropertyEditor } from './PropertyEditor'
import { SchemaStorageEditor } from './SchemaStorageEditor'
import { SchemaClassificationEditor } from './SchemaClassificationEditor'
import { SchemaPropertiesEditor } from './SchemaPropertiesEditor'
import { SchemaLifecycleEditor } from './SchemaLifecycleEditor'
import { ModelDataPreview } from './ModelDataPreview'
import { ModelWriteOperations } from './ModelWriteOperations'
import { RelatedDefinitions } from './RelatedDefinitions'
import { DefinitionDependencies } from './DefinitionDependencies'
import { ActionTargetsEditor } from './ActionTargetsEditor'
import { LinkStorageEditor, validateLinkStorage } from './LinkStorageEditor'
import { CallableImplementationEditor } from './CallableImplementationEditor'
import { CallableRunner } from './CallableRunner'
import { ActionRecordReadEditor } from './ActionRecordReadEditor'
import { CallableTestCasesEditor } from './CallableTestCasesEditor'
import { activeEntries, cloneDefinition, kindLabels, MAX_SCHEMA_INDEXES, MAX_SCHEMA_PROPERTIES, newDefinition, validateDefinition } from './model-helpers'

const LinkVisualEditor = lazy(() => import('./LinkVisualEditor').then((module) => ({ default: module.LinkVisualEditor })))

export interface PendingPublication {
  record: DefinitionRecord
  publication: Publication
}

const schemaTabs = [
  { id: 'basics', label: '基础信息' },
  { id: 'properties', label: '属性配置' },
  { id: 'indexes', label: '索引信息' },
  { id: 'lifecycle', label: '记录流程' },
  { id: 'links', label: 'Link 信息' },
  { id: 'actions', label: '动作信息' },
  { id: 'data', label: '数据预览' },
] as const
type EditorTab = typeof schemaTabs[number]['id']

export interface ModelEditorProps {
  api: ApiClient
  categoryId: string
  categories: Organization[]
  entries: DefinitionRecord[]
  currentEntries?: DefinitionRecord[]
  initialRecord?: DefinitionRecord
  initialKind: Kind
  openBasics?: boolean
  pending?: PendingPublication
  onClose: () => void
  onDeleted?: () => void
  onOrganizations: () => void
  onSources?: () => void
  onSaved: (record: DefinitionRecord) => Promise<void>
  onPending: (pending: PendingPublication) => void
  onPublished: (record: DefinitionRecord) => Promise<void>
  onNavigationGuard?: (guard?: () => boolean) => void
  active?: boolean
  initialLink?: { sourceSchemaId: string; targetSchemaId: string }
  onGraph?: () => void
  layoutScope?: string
  embedded?: boolean
  onDefinitionChange?: (definition: Definition, dirty: boolean) => void
}

export function ModelEditor(props: ModelEditorProps) {
  return props.initialRecord?.definition.link || props.initialKind === 'link'
    ? <Suspense fallback={<p className="muted" role="status">正在加载关系编辑器…</p>}><LinkVisualEditor {...props} /></Suspense>
    : <LegacyModelEditor {...props} />
}

function LegacyModelEditor({ api, categoryId, categories, entries, currentEntries, initialRecord, initialKind, openBasics, pending, onClose, onDeleted, onOrganizations, onSources, onSaved, onPending, onPublished, onNavigationGuard, active = true }: ModelEditorProps) {
  const [record, setRecord] = useState(initialRecord)
  const [definition, setDefinition] = useState(() => initialRecord ? cloneDefinition(initialRecord.definition) : newDefinition(initialKind, categoryId))
  const [initialDefinition] = useState(() => initialRecord ? cloneDefinition(initialRecord.definition) : newDefinition(initialKind, categoryId))
  const [currentRecord, setCurrentRecord] = useState(() => (currentEntries ?? entries).find((entry) => entry.status === 'active' && entry.definition.id === initialRecord?.definition.id && entry.definition.tenantId === initialRecord.definition.tenantId) ?? (initialRecord?.status === 'active' ? initialRecord : undefined))
  const [baselineError, setBaselineError] = useState('')
  const [enumProtectedCodes, setEnumProtectedCodes] = useState<(string | undefined)[]>([])
  const [schemaMigrations, setSchemaMigrations] = useState<MetadataCapabilities['schemaMigrations']>()
  const [capabilityError, setCapabilityError] = useState('')
  const [groups, setGroups] = useState<Organization[]>([])
  const [groupsLoading, setGroupsLoading] = useState(false)
  const [groupError, setGroupError] = useState('')
  const [busy, setBusy] = useState<'save' | 'preview' | 'publish' | 'reload' | 'lifecycle' | 'delete' | ''>('')
  const [lifecyclePreviewAvailable, setLifecyclePreviewAvailable] = useState(false)
  const [lifecycleAvailable, setLifecycleAvailable] = useState(false)
  const [recordReadAvailable, setRecordReadAvailable] = useState(false)
  const [managedDeleteAvailable, setManagedDeleteAvailable] = useState(false)
  const [mappedDeleteAvailable, setMappedDeleteAvailable] = useState(false)
  const [draftDeleteAvailable, setDraftDeleteAvailable] = useState(false)
  const [disabledEnumDeleteAvailable, setDisabledEnumDeleteAvailable] = useState(false)
  const [disabledCallableDeleteAvailable, setDisabledCallableDeleteAvailable] = useState(false)
  const [deleteETag, setDeleteETag] = useState<string>()
  const [confirmLifecycle, setConfirmLifecycle] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [conflict, setConflict] = useState(false)
  const [confirmClose, setConfirmClose] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const [publication, setPublication] = useState(pending?.publication)
  const [preview, setPreview] = useState<Preview>()
  const [tab, setTab] = useState<EditorTab>(!openBasics && initialRecord?.definition.schema ? 'properties' : 'basics')
  useEffect(() => { if (openBasics) setTab('basics') }, [openBasics])
  const [runtimeDiagnostics, setRuntimeDiagnostics] = useState<{ source: string; items: SourceDiagnostic[] }>()
  const [diagnosticFocus, setDiagnosticFocus] = useState<SourceDiagnostic>()
  const diagnosticEpoch = useRef(0)
  const tabId = useId()
  const dirty = JSON.stringify(definition) !== JSON.stringify(record?.definition ?? initialDefinition)
  // External mapped schemas keep their stages in the source system, so the
  // lifecycle tab is only offered for platform-managed objects.
  const lifecycleTabHidden = !!definition.schema?.storageBinding
  const visibleSchemaTabs = lifecycleTabHidden ? schemaTabs.filter((item) => item.id !== 'lifecycle') : schemaTabs
  useEffect(() => {
    if (lifecycleTabHidden && tab === 'lifecycle') setTab('basics')
  }, [lifecycleTabHidden, tab])
  const suspended = record?.status === 'disabled'
  const locked = !!(record?.hasCurrent || record?.status === 'active' || suspended)
  const canDeleteManaged = suspended && definition.schema?.storageLayout === 'single' && (definition.schema.storageBinding
    ? mappedDeleteAvailable
    : definition.schema.mode === 'schema_2_db' && managedDeleteAvailable)
  const canDeleteEnum = disabledEnumDeleteAvailable && suspended && record?.definition.kind === 'enum'
  const retainedCallable = record?.definition.action ?? record?.definition.function
  const canDeleteCallable = disabledCallableDeleteAvailable && suspended && !!retainedCallable &&
    (!retainedCallable.sideEffects || retainedCallable.sideEffects === 'none') && !retainedCallable.dataWrite?.length
  const deleteKind = canDeleteManaged ? '模型' : canDeleteEnum ? '枚举' : canDeleteCallable ? kindLabels[definition.kind] : undefined
  const canDeleteDefinition = !!onDeleted && (!!deleteKind || (draftDeleteAvailable && record?.status === 'draft' && !locked))
  const uncertain = !!publication && !['succeeded', 'rejected'].includes(publication.status)
  const disabled = !!busy || uncertain || suspended
  const callable = definition.action ?? definition.function
  const managedSchema = definition.schema?.mode === 'schema_2_db' && definition.schema.storageLayout === 'single' && !definition.schema.storageBinding
  const mappedSchema = definition.schema?.storageLayout === 'single' && !!definition.schema.storageBinding
  const lifecycleAllowed = lifecycleAvailable && (managedSchema || mappedSchema || record?.definition.kind === 'enum' || !!callable && (!callable.sideEffects || callable.sideEffects === 'none')) && (record?.status === 'active' || suspended)
  const directoryCategoryId = definition.categoryId
  const sourceCategory = categories.find((item) => item.id === categoryId)
  const category = categories.find((item) => item.id === directoryCategoryId && item.tenantId === sourceCategory?.tenantId)
  const directoryChanged = directoryCategoryId !== categoryId
  const bindingReady = !!category && !!definition.groupId && ((!!record && !directoryChanged) || !groupsLoading && groups.some((group) => group.id === definition.groupId && group.categoryId === directoryCategoryId))
  const authorizedEntries = entries.filter((entry) => !definition.tenantId || entry.definition.tenantId === definition.tenantId)
  const currentSchemas = activeEntries(authorizedEntries, 'schema')
  const currentEnums = (currentEntries ?? authorizedEntries).filter((entry) => entry.status === 'active' && entry.definition.kind === 'enum' && (!definition.tenantId || entry.definition.tenantId === definition.tenantId))
  const currentFunctions = (currentEntries ?? authorizedEntries).filter((entry) => entry.status === 'active' && entry.definition.kind === 'function' && (!definition.tenantId || entry.definition.tenantId === definition.tenantId))
  const allowAppend = locked && !!currentRecord && definition.schema?.storageLayout === 'single' && definition.schema.mode === 'schema_2_db' && (!!definition.schema.storageBinding || !!schemaMigrations?.addFields)
  const allowIndexAppend = allowAppend && (!!definition.schema?.storageBinding || !!schemaMigrations?.addIndexes)
  const currentPropertyIds = currentRecord?.definition.schema?.properties.map((property) => property.id)
  const currentIndexIds = currentRecord?.definition.schema?.indexes?.map((index) => index.id) ?? []
  const enumCodeLocked = (position: number) => locked && (!currentRecord || !!enumProtectedCodes[position])
  const currentLink = (currentEntries ?? entries).find((entry) => entry.definition.id === definition.id && entry.status === 'active')
  const currentSchema = definition.schema ? currentLink ?? (record?.status === 'active' ? record : undefined) : undefined
  const linkStorageLocked = locked && !!(currentLink?.definition.link?.storageBinding ?? (record?.status === 'active' ? record.definition.link?.storageBinding : !currentEntries ? initialRecord?.definition.link?.storageBinding : undefined))
  const pythonSource = (definition.action ?? definition.function)?.runtime?.source
  const visibleDiagnostics = runtimeDiagnostics?.source === pythonSource ? runtimeDiagnostics?.items : undefined

  useEffect(() => {
    const codes = new Set(currentRecord?.definition.enum?.values.map((value) => value.code))
    setEnumProtectedCodes(definition.enum?.values.map((value) => codes.has(value.code) ? value.code : undefined) ?? [])
    // Rebind immutable row identities only when the authoritative current definition changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentRecord])

  useEffect(() => {
    setSchemaMigrations(undefined); setCapabilityError('')
    setManagedDeleteAvailable(false); setMappedDeleteAvailable(false)
    setDraftDeleteAvailable(false)
    setDisabledEnumDeleteAvailable(false)
    setLifecycleAvailable(false)
    setLifecyclePreviewAvailable(false)
    setRecordReadAvailable(false)
    if (!active || typeof api.metadataCapabilities !== 'function') return
    const controller = new AbortController()
    api.metadataCapabilities(controller.signal).then((capabilities) => {
      if (controller.signal.aborted) return
      setDraftDeleteAvailable(capabilities.unpublishedDraftDelete === true)
      setManagedDeleteAvailable(capabilities.disabledManagedStorageDelete === true)
      setMappedDeleteAvailable(capabilities.disabledMappedSchemaDelete === true)
      setDisabledEnumDeleteAvailable(capabilities.disabledEnumDelete === true)
      setDisabledCallableDeleteAvailable(capabilities.disabledCallableDelete === true)
      setSchemaMigrations(capabilities.schemaMigrations)
      setLifecycleAvailable(definition.kind === 'enum' ? capabilities.enumLifecycle === true
        : managedSchema ? capabilities.managedSchemaLifecycle === true : mappedSchema ? capabilities.mappedSchemaLifecycle === true : capabilities.callableLifecycle === 'pure-only')
      setLifecyclePreviewAvailable(capabilities.callableLifecyclePreview === true)
      setRecordReadAvailable(capabilities.actionRecordRead === true)
    }).catch((cause: unknown) => { if (!controller.signal.aborted && definition.schema) setCapabilityError(cause instanceof Error ? cause.message : '迁移能力读取失败。') })
    return () => controller.abort()
  }, [api, active, locked, definition.kind, definition.schema?.storageBinding, managedSchema, mappedSchema])

  useEffect(() => {
    if (!active || !locked || currentRecord || !definition.id) return
    const controller = new AbortController()
    setBaselineError('')
    api.getCurrent(definition.id, controller.signal).then((current) => {
      if (controller.signal.aborted) return
      if (current.status !== 'active' || current.definition.id !== definition.id || current.definition.tenantId !== definition.tenantId) throw new Error('当前生效定义不匹配，请刷新目录。')
      setCurrentRecord(current)
    }).catch((cause: unknown) => { if (!controller.signal.aborted) setBaselineError(cause instanceof Error ? cause.message : '当前生效结构读取失败。') })
    return () => controller.abort()
  }, [api, active, locked, currentRecord, definition.id, definition.tenantId])

  useEffect(() => {
    diagnosticEpoch.current++
    setRuntimeDiagnostics(undefined); setDiagnosticFocus(undefined)
  }, [definition, record?.etag, active])

  async function receiveDraftDiagnostics(invocation?: Invocation) {
    const epoch = ++diagnosticEpoch.current
    setRuntimeDiagnostics(undefined); setDiagnosticFocus(undefined)
    if (!invocation?.diagnostics?.length || !pythonSource || invocation.mode !== 'draft') return
    try {
      const hash = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(pythonSource))
      const hex = Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, '0')).join('')
      if (epoch === diagnosticEpoch.current && hex === invocation.codeDigest) setRuntimeDiagnostics({ source: pythonSource, items: invocation.diagnostics })
    } catch { /* The textual result still carries line numbers if source matching is unavailable. */ }
  }

  function panelProps(id: EditorTab) {
    return definition.schema ? { role: 'tabpanel', id: `${tabId}-panel-${id}`, 'aria-labelledby': `${tabId}-tab-${id}`, hidden: tab !== id, tabIndex: 0 } : {}
  }
  function navigateTabs(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const count = visibleSchemaTabs.length
    const next = event.key === 'ArrowRight' ? (index + 1) % count
      : event.key === 'ArrowLeft' ? (index + count - 1) % count
      : event.key === 'Home' ? 0 : event.key === 'End' ? count - 1 : -1
    if (next < 0) return
    event.preventDefault()
    setTab(visibleSchemaTabs[next].id)
    document.getElementById(`${tabId}-tab-${visibleSchemaTabs[next].id}`)?.focus()
  }

  useEffect(() => {
    if (!active) return
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirty && !busy) return
      event.preventDefault()
      event.returnValue = ''
    }
    const guard = () => {
      if (busy) return false
      if (!dirty || uncertain) return true
      if (!window.confirm('还有未保存的模型修改，离开后将丢失。确定离开？')) return false
      setDefinition(cloneDefinition(record?.definition ?? initialDefinition))
      return true
    }
    onNavigationGuard?.(guard)
    window.addEventListener('beforeunload', beforeUnload)
    return () => { onNavigationGuard?.(); window.removeEventListener('beforeunload', beforeUnload) }
  }, [active, dirty, busy, uncertain, record, initialDefinition, onNavigationGuard])

  useEffect(() => {
    if (!active) { setGroupsLoading(false); return }
    if (!directoryCategoryId) { setGroups([]); setGroupsLoading(false); setGroupError(''); return }
    const controller = new AbortController()
    setGroups([]); setGroupsLoading(true)
    setGroupError('')
    api.listGroups(directoryCategoryId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setGroups(items.filter((group) => group.categoryId === directoryCategoryId))
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setGroupError(cause instanceof Error ? cause.message : '分组读取失败')
    }).finally(() => { if (!controller.signal.aborted) setGroupsLoading(false) })
    return () => controller.abort()
  }, [api, directoryCategoryId, active])

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
    const invalid = validateDefinition(definition) ?? (allowAppend && !definition.schema?.storageBinding && definition.schema?.properties.some((property) => !currentPropertyIds?.includes(property.id) && !property.nullable && (property.default === undefined || property.default === null)) ? '新增非空属性必须设置合法的非空默认值，以回填已有记录。' : undefined) ?? (definition.link ? validateLinkStorage(definition.link, currentSchemas) : undefined)
    if (invalid) {
      setError(invalid)
      if (definition.schema) {
        const basicError = invalid === '请填写业务名称。' || /分类|分组|建模方向|主数据源|主表|数据库为准/.test(invalid)
        const lifecycleError = /记录流程|阶段|流转/.test(invalid)
        setTab(/索引/.test(invalid) ? 'indexes' : lifecycleError ? 'lifecycle' : basicError ? 'basics' : 'properties')
      }
      return
    }
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
      setCurrentRecord(active)
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
      latest.hasCurrent = locked || latest.status === 'active' || latest.status === 'disabled'
      if (latest.hasCurrent) setCurrentRecord(latest.status !== 'draft' ? latest : await api.getCurrent(record.definition.id))
      setRecord(latest); setDefinition(cloneDefinition(latest.definition)); setConflict(false); setPreview(undefined); setMessage('已载入服务器最新内容。')
      try { await onSaved(latest) } catch { /* Editor already has the authoritative record. */ }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }

  async function checkLifecycle() {
    if (!record || !lifecycleAllowed || dirty || busy || uncertain) return
    if (!lifecyclePreviewAvailable) {
      setConfirmLifecycle(true)
      return
    }
    setBusy('lifecycle'); setError(''); setMessage('')
    try {
      const result = await api.previewCallableLifecycle(record.definition.id, suspended ? 'restore' : 'disable', record.etag)
      if (result.definitionId !== record.definition.id || result.targetStatus !== (suspended ? 'active' : 'disabled')) {
        throw new Error('定义状态预览与请求不一致，请重新读取。')
      }
      setConfirmLifecycle(true)
    } catch (cause) { report(cause) } finally { setBusy('') }
  }

  async function changeLifecycle() {
    if (!record || !lifecycleAllowed || dirty || busy || uncertain) return
    setBusy('lifecycle'); setError(''); setMessage(''); setConfirmLifecycle(false)
    try {
      const updated = await api.setCallableLifecycle(record.definition.id, suspended ? 'restore' : 'disable', record.etag)
      updated.hasCurrent = true
      setRecord(updated); setCurrentRecord(updated); setDefinition(cloneDefinition(updated.definition))
      setMessage(suspended ? '定义已恢复。' : '定义已停用。')
      try { await onSaved(updated) } catch { setMessage('状态已变更，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }

  async function deleteDefinition(confirm: boolean) {
    if (!record || !canDeleteDefinition || dirty || busy || uncertain || (confirm && deleteETag !== record.etag)) return
    setBusy('delete'); setError(''); setMessage(''); setDeleteETag(undefined)
    try {
      if (confirm) {
        await api.deleteUnpublishedDraft(record.definition.id, record.etag)
        onDeleted?.()
      } else {
        await api.previewDraftDeletion(record.definition.id, record.etag)
        setDeleteETag(record.etag)
      }
    } catch (cause) { report(cause) } finally { setBusy('') }
  }

  function updateIndex(position: number, patch: Partial<Index>) {
    update({ schema: { ...definition.schema!, indexes: (definition.schema!.indexes ?? []).map((index, offset) => offset === position ? { ...index, ...patch } : index) } })
  }

  return <section className="model-editor-page" aria-label="模型编辑">
    <div className="model-editor">
      <div className="model-editor-meta"><button type="button" className="button subtle model-back-button" onClick={requestClose} disabled={!!busy}><ArrowLeft size={16} />返回模型列表</button>{definition.name && <strong className="model-editor-name">{definition.name}</strong>}<span className={`badge model-kind-${definition.kind}`}>{kindLabels[definition.kind]}</span><span className="badge">{suspended ? '已停用' : record?.status === 'active' ? '当前生效' : '草稿'}</span>{dirty && <span className="model-unsaved">有未保存修改</span>}
        {canDeleteDefinition && deleteETag !== record?.etag && <button type="button" className="button subtle" disabled={!!busy || dirty || uncertain} onClick={() => void deleteDefinition(false)}>删除{deleteKind ?? '草稿'}</button>}
        {lifecycleAllowed && !confirmLifecycle && <button type="button" className="button subtle" disabled={!!busy || dirty || uncertain} onClick={() => void checkLifecycle()}>{suspended ? '恢复定义' : '停用定义'}</button>}
      </div>
      {canDeleteDefinition && deleteETag === record?.etag && <div className="notice notice-error" role="status">
        <p>{deleteKind ? `确认删除此已停用的${deleteKind}？删除后无法恢复。` : '确认删除这份未发布的草稿？删除后无法恢复。'}</p>
        <div className="model-inline-actions">
          <button type="button" className="button subtle" autoFocus onClick={() => setDeleteETag(undefined)}>取消</button>
          <button type="button" className="button primary" disabled={!!busy || dirty || uncertain} onClick={() => void deleteDefinition(true)}>确认删除</button>
        </div>
      </div>}
      {confirmLifecycle && lifecycleAllowed && <div className="notice">
        {lifecyclePreviewAvailable && <p>预览检查通过，确认时将重新检查。</p>}
        <p>{definition.kind === 'enum'
          ? suspended ? '恢复后，模型和参数可重新引用此枚举。' : '停用后，新定义将无法引用此枚举。已有引用会阻止停用。'
          : managedSchema || mappedSchema ? suspended ? '恢复前会复核物理表和引用。' : '停用前会检查对象存量、定义引用、保存查询和策略。'
          : suspended ? '恢复后，此定义可按现有权限调用。' : '停用后，此定义将暂停调用，已保存的内容仍可查看。'}</p>
        <div className="model-inline-actions"><button type="button" className="button primary" disabled={!!busy || dirty || uncertain} onClick={() => void changeLifecycle()}>{suspended ? '确认恢复' : '确认停用'}</button><button type="button" className="button subtle" onClick={() => setConfirmLifecycle(false)}>取消</button></div>
      </div>}
      {locked && !suspended && <div className="notice model-lock-notice">{allowAppend ? definition.schema?.storageBinding ? '此定义已生效。现有结构与来源绑定保持锁定，可新增允许空值的属性和索引，预览并发布后生效。' : '此定义已生效。可追加允许空值或有合法默认值的属性、新增普通或唯一索引；现有字段、索引与来源绑定保持锁定。发布前请核对计划，唯一索引须检查既有数据。' : definition.enum ? '此枚举已生效。可增加业务值、修改标签或停用值；已有编码保留，停用值仍可解释旧数据，不用于新写入。' : definition.function ? '此函数已生效。可更新执行语言、源码、入口和超时，保存并发布后生效；参数类型、约束和副作用保持锁定。' : definition.action ? definition.action.runtime ? '此动作已生效。可更新独立源码的执行语言、代码、入口和超时，保存并发布后生效；参数类型、约束、副作用和实现方式保持锁定。' : '此动作已生效。可修改展示信息和关联对象；参数、绑定实现和副作用保持锁定。' : definition.link && !linkStorageLocked ? '此关系定义已生效。可补充关系存储映射，保存并发布后启用关联查询；关系两端、基数及删除策略保持锁定。' : '此定义已生效。可修改名称、描述和显示标签；数据结构与绑定保持锁定。'}</div>}
      {baselineError && <div className="notice notice-error" role="alert">{baselineError} 读取当前结构后才能追加字段、索引或调整枚举值。</div>}
      {capabilityError && <div className="notice notice-error" role="alert">{capabilityError} 读取迁移能力后才能追加平台字段或索引。</div>}
      {uncertain && <div className="notice notice-error" role="status"><strong>发布结果待确认</strong><p>编辑已暂停。使用同一份已保存内容继续恢复；返回列表后可从模型页的待恢复记录重新进入。</p><span className="muted">阶段：{publication?.status === 'activation_pending' ? '已提交，等待激活' : publication?.status === 'prepare_failed' ? '准备未完成' : '等待确认'}</span></div>}
      {error && <div className="notice notice-error" role="alert"><AlertCircle size={16} /><span>{error}</span></div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      {conflict && <div className="notice model-conflict"><p>服务器内容已改变，本地编辑仍然保留。载入最新内容会放弃当前未保存的修改。</p><button type="button" className="button subtle" disabled={!!busy} onClick={() => setConfirmReload(true)}><RefreshCw size={15} />载入最新内容</button></div>}
      {confirmReload && <div className="notice notice-error"><p>确定放弃本地编辑并载入服务器最新内容？</p><div className="model-inline-actions"><button type="button" className="button primary" onClick={() => void reload()}>确认载入</button><button type="button" className="button subtle" onClick={() => setConfirmReload(false)}>保留本地编辑</button></div></div>}
      {confirmClose && <div className="notice notice-error" role="alert"><p>还有未保存的修改。返回列表后这些修改将丢失。</p><div className="model-inline-actions"><button type="button" className="button subtle" onClick={() => setConfirmClose(false)}>继续编辑</button><button type="button" className="button" onClick={onClose}>放弃修改并返回</button></div></div>}
      {definition.schema && <div className="model-editor-tabs" role="tablist" aria-label="模型配置">{visibleSchemaTabs.map((item, index) => <button type="button" role="tab" key={item.id} id={`${tabId}-tab-${item.id}`} aria-controls={`${tabId}-panel-${item.id}`} aria-selected={tab === item.id} tabIndex={tab === item.id ? 0 : -1} onClick={() => setTab(item.id)} onKeyDown={(event) => navigateTabs(event, index)}>{item.label}{(item.id === 'properties' || item.id === 'indexes') && <span aria-hidden="true">{item.id === 'properties' ? definition.schema!.properties.length : definition.schema!.indexes?.length ?? 0}</span>}</button>)}</div>}
      <fieldset disabled={disabled} className="model-fieldset">
        <section className="model-editor-panel" {...panelProps('basics')}>
        <div className="form-grid model-basics">
          <label>业务名称<input className="field" autoFocus value={definition.name} maxLength={128} onChange={(event) => update({ name: event.target.value })} placeholder="例如：生产设备" /></label>
          <label>类型<select className="field" disabled={!!record} value={definition.kind} onChange={(event) => { setDefinition(newDefinition(event.target.value as Kind, categoryId, definition.groupId)); setTab('basics') }}>{Object.entries(kindLabels).map(([kind, label]) => <option key={kind} value={kind}>{label}</option>)}</select></label>
          <label>分类{record ? <select className="field" value={directoryCategoryId} onChange={(event) => update({ categoryId: event.target.value, groupId: '' })}>{categories.filter((item) => item.tenantId === sourceCategory?.tenantId).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select> : <input className="field" readOnly value={category?.name ?? '未选择分类'} />}</label>
          <label>分组<select className="field" disabled={groupsLoading || !category} value={definition.groupId} onChange={(event) => update({ groupId: event.target.value })}><option value="">{groupsLoading ? '正在读取分组…' : '选择分组'}</option>{record && definition.groupId && !groups.some((group) => group.id === definition.groupId) && <option value={definition.groupId}>当前分组</option>}{groups.filter((group) => group.categoryId === directoryCategoryId).map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}</select></label>
          <label className="model-span-full">描述<textarea className="field" rows={2} value={definition.description ?? ''} onChange={(event) => update({ description: event.target.value })} placeholder="说明这一业务定义的用途与边界" /></label>
        </div>
        {groupError && <div className="notice notice-error">{groupError}</div>}
        {!record && (!category || !groupsLoading && !groupError && !groups.length) && <div className="notice"><p>{category ? '当前分类还没有分组，请前往「分类与分组」创建分组后再新建定义。' : '请先在顶部选择当前分类，或前往「分类与分组」创建分类和分组。'}</p><button type="button" className="button subtle" onClick={onOrganizations}>前往分类与分组<ArrowRight size={15} /></button></div>}
        {definition.schema && <SchemaClassificationEditor schema={definition.schema} entries={currentEntries ?? entries} categoryId={directoryCategoryId} disabled={disabled} onChange={(schema) => update({ schema })} />}
        {definition.schema && <SchemaStorageEditor section="storage" api={api} schema={definition.schema} locked={locked} active={active} onSources={onSources} onChange={(schema) => update({ schema })} />}
        </section>
        {definition.schema && <>
          <section className="model-editor-panel" {...panelProps('properties')}>
          <SchemaPropertiesEditor api={api} schema={definition.schema} locked={locked} allowAppend={allowAppend} currentPropertyIds={currentPropertyIds} enums={currentEnums} disabled={disabled} active={active && tab === 'properties'} currentId={locked ? definition.id : undefined} currentETag={currentRecord?.etag ?? currentSchema?.etag} draft={record?.status !== 'active' || dirty} maxProperties={MAX_SCHEMA_PROPERTIES} onChange={(schema) => update({ schema })} />
          </section>
          <section className="model-editor-panel" {...panelProps('indexes')}><div className="model-section-heading"><div><h3>索引</h3><p className="muted">最多 {MAX_SCHEMA_INDEXES} 个普通或唯一索引，每个索引最多组合两个属性。长文本和超长文本不支持索引。</p></div>{(!locked || allowIndexAppend) && <button type="button" className="button subtle" disabled={!definition.schema.properties.some((property) => property.id && property.dataType !== 'text' && property.dataType !== 'longtext') || (definition.schema.indexes?.length ?? 0) >= MAX_SCHEMA_INDEXES} onClick={() => update({ schema: { ...definition.schema!, indexes: [...definition.schema!.indexes ?? [], { id: '', name: '', unique: false, fieldIds: [] }] } })}><Plus size={15} />添加索引</button>}</div>
            {definition.schema.properties.some((property) => !property.id) && <div className="notice model-index-hint">新增属性需先保存草稿，再在此选择属性建立索引。</div>}
            {(definition.schema.indexes ?? []).length === 0 && <div className="model-inline-empty">尚未设置索引。</div>}
            {(definition.schema.indexes ?? []).map((index, position) => { const indexLocked = locked && (!allowIndexAppend || currentIndexIds.includes(index.id)); return <div className="model-index-row" key={index.id || `new-index-${position}`}>
              <label>索引名称<input className="field" value={index.name} aria-label={`索引${position + 1}名称`} onChange={(event) => updateIndex(position, { name: event.target.value })} placeholder="例如：设备编码唯一" /></label>
              <label>类型<select className="field" disabled={indexLocked} value={index.unique ? 'unique' : 'normal'} onChange={(event) => updateIndex(position, { unique: event.target.value === 'unique' })}><option value="normal">普通索引</option><option value="unique">唯一索引</option></select></label>
              <label>第一个属性<select className="field" disabled={indexLocked} aria-label={`索引${position + 1}第一个属性`} value={index.fieldIds[0] ?? ''} onChange={(event) => updateIndex(position, { fieldIds: [event.target.value, ...index.fieldIds.slice(1)].filter(Boolean) })}><option value="">选择属性</option>{definition.schema!.properties.filter((property) => property.id && property.dataType !== 'text' && property.dataType !== 'longtext').map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}</select></label>
              <label>第二个属性<select className="field" disabled={indexLocked || !index.fieldIds[0]} aria-label={`索引${position + 1}第二个属性`} value={index.fieldIds[1] ?? ''} onChange={(event) => updateIndex(position, { fieldIds: [index.fieldIds[0], event.target.value].filter(Boolean) })}><option value="">不组合</option>{definition.schema!.properties.filter((property) => property.id && property.id !== index.fieldIds[0] && property.dataType !== 'text' && property.dataType !== 'longtext').map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}</select></label>
              {!indexLocked && <button type="button" className="button subtle model-icon-button" aria-label={`删除索引${position + 1}`} onClick={() => update({ schema: { ...definition.schema!, indexes: definition.schema!.indexes!.filter((_, offset) => offset !== position) } })}><Trash2 size={16} /></button>}
            </div>})}
          </section>
          <section className="model-editor-panel" {...panelProps('lifecycle')}>
            <SchemaLifecycleEditor schema={definition.schema} disabled={disabled} onChange={(schema) => update({ schema })} />
          </section>
          <section className="model-editor-panel" {...panelProps('links')}><RelatedDefinitions kind="link" definition={definition} entries={authorizedEntries} currentEntries={currentEntries} categories={categories} /></section>
          <section className="model-editor-panel" {...panelProps('actions')}>
            <ModelWriteOperations api={api} schemaId={definition.id} active={active && tab === 'actions' && !uncertain} hasCurrent={locked} />
            <RelatedDefinitions kind="action" definition={definition} entries={authorizedEntries} currentEntries={currentEntries} categories={categories} />
          </section>
        </>}
        {definition.enum && <section>
          <div className="model-section-heading"><div><h3>枚举业务值</h3><p className="muted">业务编码保持稳定，显示标签用于业务界面。停用值保留旧数据含义。</p></div><button type="button" className="button subtle" disabled={locked && !currentRecord} onClick={() => { setEnumProtectedCodes((codes) => [...codes, undefined]); update({ enum: { values: [...definition.enum!.values, { code: '', label: '', deprecated: false }] } }) }}><Plus size={15} />添加值</button></div>
          <div className="table-wrap"><table><thead><tr><th>业务编码</th><th>显示标签</th><th>状态</th><th>操作</th></tr></thead><tbody>{definition.enum.values.map((value, position) => <tr key={position}>
            <td><input className="field" disabled={enumCodeLocked(position)} value={value.code} aria-label={`枚举值${position + 1}编码`} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, code: event.target.value } : entry) } })} placeholder="例如：RUNNING" /></td>
            <td><input className="field" value={value.label} aria-label={`枚举值${position + 1}标签`} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, label: event.target.value } : entry) } })} placeholder="例如：运行中" /></td>
            <td><label className="model-checkbox"><input type="checkbox" disabled={locked && !currentRecord} checked={!!value.deprecated} onChange={(event) => update({ enum: { values: definition.enum!.values.map((entry, offset) => offset === position ? { ...entry, deprecated: event.target.checked } : entry) } })} />停用值</label></td>
            <td>{!enumCodeLocked(position) && <button type="button" className="button subtle model-icon-button" aria-label={`删除枚举值${position + 1}`} onClick={() => { setEnumProtectedCodes((codes) => codes.filter((_, offset) => offset !== position)); update({ enum: { values: definition.enum!.values.filter((_, offset) => offset !== position) } }) }}><Trash2 size={16} /></button>}</td>
          </tr>)}</tbody></table></div>
        </section>}
        {definition.link && <section><div className="model-section-heading"><div><h3>关系两端</h3><p className="muted">仅选择已经生效的对象模型，支持同一模型之间的关系。</p></div></div><div className="form-grid">
          <label>来源模型<select className="field" disabled={locked} value={definition.link.sourceSchemaId} onChange={(event) => update({ link: { ...definition.link!, sourceSchemaId: event.target.value } })}><option value="">选择来源模型</option>{definition.link.sourceSchemaId && !currentSchemas.some((entry) => entry.definition.id === definition.link!.sourceSchemaId) && <option value={definition.link.sourceSchemaId}>当前来源模型（无目录权限）</option>}{currentSchemas.map(({ definition: schema }) => <option key={schema.id} value={schema.id}>{categories.find((category) => category.id === schema.categoryId)?.name} / {schema.name}</option>)}</select></label>
          <label>目标模型<select className="field" disabled={locked} value={definition.link.targetSchemaId} onChange={(event) => update({ link: { ...definition.link!, targetSchemaId: event.target.value } })}><option value="">选择目标模型</option>{definition.link.targetSchemaId && !currentSchemas.some((entry) => entry.definition.id === definition.link!.targetSchemaId) && <option value={definition.link.targetSchemaId}>当前目标模型（无目录权限）</option>}{currentSchemas.map(({ definition: schema }) => <option key={schema.id} value={schema.id}>{categories.find((category) => category.id === schema.categoryId)?.name} / {schema.name}</option>)}</select></label>
          <label>正向业务名称<input className="field" value={definition.link.forwardName} onChange={(event) => update({ link: { ...definition.link!, forwardName: event.target.value } })} placeholder="例如：所属车间" /></label>
          <label>反向业务名称<input className="field" value={definition.link.reverseName} onChange={(event) => update({ link: { ...definition.link!, reverseName: event.target.value } })} placeholder="例如：包含设备" /></label>
          <label>关系基数<select className="field" disabled={locked} value={definition.link.cardinality} onChange={(event) => update({ link: { ...definition.link!, cardinality: event.target.value as Link['cardinality'] } })}><option value="1:1">一对一</option><option value="1:N">一对多</option><option value="N:1">多对一</option><option value="N:M">多对多</option></select></label>
          <label>删除策略<select className="field" disabled value="restrict"><option value="restrict">有关联时限制删除</option></select></label>
        </div><LinkStorageEditor api={api} link={definition.link} schemas={currentSchemas} locked={linkStorageLocked} active={active} onSources={onSources} onChange={(link) => update({ link })} /></section>}
        {(definition.action || definition.function) && (() => {
          const key = definition.action ? 'action' : 'function'
          const callable = definition[key]!
          return <>
            {definition.action && <ActionTargetsEditor selected={callable.targetSchemaIds ?? []} schemas={currentSchemas} categories={categories} onChange={(targetSchemaIds) => update({ action: { ...callable, targetSchemaIds: targetSchemaIds.length ? targetSchemaIds : undefined } })} />}
            <CallableImplementationEditor callable={callable} kind={key} locked={locked} disabled={disabled} functions={currentFunctions} schemas={currentSchemas} onChange={(value) => update({ [key]: value })} diagnostics={visibleDiagnostics} diagnosticFocus={diagnosticFocus} api={api} definitionId={definition.id} />
            <PropertyEditor title="输入参数" properties={callable.input ?? []} enums={currentEnums} locked={locked} onChange={(input) => update({ [key]: { ...callable, input } })} />
            {definition.action && (recordReadAvailable || callable.recordRead) && <ActionRecordReadEditor action={callable} schemas={currentSchemas} locked={locked} onChange={(recordRead) => update({
              action: { ...callable, recordRead, ...(recordRead && callable.sideEffects === 'dataWrite' ? {
                dataWrite: [{ schemaId: recordRead.schemaId, operations: ['update'] }],
              } : {}) },
            })} />}
            <PropertyEditor title="输出参数" properties={callable.output ?? []} enums={currentEnums} locked={locked} onChange={(output) => update({ [key]: { ...callable, output } })} />
            <CallableTestCasesEditor key={`${definition.tenantId}:${definition.id}`} callable={callable} disabled={disabled} onChange={(testCases) => update({ [key]: { ...callable, testCases: testCases.length ? testCases : undefined } })} />
          </>
        })()}
        {definition.action && !definition.action.runtime && !locked && currentFunctions.some((entry) => entry.definition.id === definition.action!.functionId) && <div className="model-inline-actions"><button type="button" className="button" onClick={() => {
          const implementation = currentFunctions.find((entry) => entry.definition.id === definition.action!.functionId)!.definition.function!
          update({ action: { ...definition.action!, sideEffects: implementation.sideEffects, dataWrite: structuredClone(implementation.dataWrite), input: structuredClone(implementation.input ?? []).map((property) => ({ ...property, id: '' })), output: structuredClone(implementation.output ?? []).map((property) => ({ ...property, id: '' })) } })
        }}>从实现函数复制参数声明</button><span className="muted">替换本动作的输入、输出、副作用及业务写入范围。</span></div>}
      </fieldset>
      {!suspended && !definition.action?.recordRead && (definition.action || definition.function) && <CallableRunner key={`draft:${definition.tenantId}:${definition.id}:${record?.etag}`} api={api} definitionId={definition.id} tenantId={definition.tenantId} mode="draft" draftEtag={record?.status === 'draft' ? record.etag : undefined} dataWrite={(definition.action ?? definition.function)?.sideEffects === 'dataWrite'} blocked={dirty || disabled || conflict} active={active && !uncertain} onDiagnostics={(result) => void receiveDraftDiagnostics(result)} onDiagnosticFocus={visibleDiagnostics?.length ? (item) => setDiagnosticFocus({ ...item }) : undefined} />}
      {locked && !suspended && (definition.action || definition.function) && <CallableRunner key={`${definition.tenantId}:${definition.id}:${currentLink?.etag ?? record?.etag}`} api={api} definitionId={definition.id} tenantId={definition.tenantId} active={active && !uncertain} />}
      {definition.schema && <section className="model-editor-panel" {...panelProps('data')}>
        <ModelDataPreview key={definition.id || 'unpublished'} api={api} schemaId={definition.id} active={active && tab === 'data' && !uncertain} hasCurrent={locked} draft={record?.status !== 'active' || dirty} />
      </section>}
      {tab !== 'data' && <>
      <DefinitionDependencies api={api} record={record} dirty={dirty} active={active && !uncertain} />
      {preview && <section className="model-preview"><div className="model-section-heading"><div><h3><FileCode2 size={17} />存储计划预览</h3><p className="muted">预览不会执行 SQL，也不会占用发布状态。</p></div></div>{preview.statements?.length ? <pre>{preview.statements.join('\n\n')}</pre> : <p className="model-inline-empty">此变更没有需要执行的存储语句。</p>}</section>}
      <div className="model-editor-footer"><span className="muted">{suspended ? '恢复定义后可继续编辑。' : dirty ? '先保存草稿，再预览或发布。' : record?.status === 'active' ? '当前内容已生效，可再次确认或恢复发布。' : '草稿已保存，发布后对业务生效。'}</span><div className="model-inline-actions"><button type="button" className="button" disabled={disabled || !record || dirty} onClick={() => void showPreview()}>{busy === 'preview' ? <LoaderCircle className="model-spin" size={16} /> : <FileCode2 size={16} />}预览计划</button>{!uncertain && <button type="button" className="button" disabled={disabled || !dirty || conflict || !bindingReady} onClick={() => void save()}>{busy === 'save' ? <LoaderCircle className="model-spin" size={16} /> : <Save size={16} />}保存草稿</button>}<button type="button" className="button primary" disabled={!!busy || suspended || !record || dirty || conflict} onClick={() => void publish()}>{busy === 'publish' ? <LoaderCircle className="model-spin" size={16} /> : uncertain ? <RefreshCw size={16} /> : <Send size={16} />}{uncertain ? '继续恢复发布' : record?.status === 'active' ? '确认生效状态' : '发布定义'}<ArrowRight size={15} /></button></div></div>
      </>}
    </div>
  </section>
}
