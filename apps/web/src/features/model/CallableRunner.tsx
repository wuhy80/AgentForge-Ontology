import { useEffect, useRef, useState } from 'react'
import { LoaderCircle, Play, RefreshCw, X } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Invocation, SourceDiagnostic, Values } from '../../lib/types'
import { buildWriteValues, errorMessage, initialDrafts, propertyValueText, writeIntentSignature, type FieldDrafts } from '../query/query'
import { CallableInputs, testCaseDrafts } from './CallableInputs'
import { languageLabels } from '../../lib/runtime-language'

const statuses: Record<Invocation['status'], string> = { running: '正在运行', committing: '正在确认事务提交', succeeded: '运行成功', failed: '运行失败', timed_out: '执行超时', canceled: '执行已取消', interrupted: '执行中断' }

export function CallableRunner({ api, definitionId, tenantId, active = true, mode = 'current', draftEtag, blocked = false, dataWrite = false, onDiagnostics, onDiagnosticFocus }: {
  api: ApiClient; definitionId: string; tenantId: string; active?: boolean; mode?: 'current' | 'draft'; draftEtag?: string; blocked?: boolean
  dataWrite?: boolean
  onDiagnostics?: (invocation?: Invocation) => void; onDiagnosticFocus?: (diagnostic: SourceDiagnostic) => void
}) {
  const [opened, setOpened] = useState(false)
  const [current, setCurrent] = useState<DefinitionRecord>()
  const [drafts, setDrafts] = useState<FieldDrafts>({})
  const [invocation, setInvocation] = useState<Invocation>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(false)
  const pending = useRef<{ signature: string; key: string; input: Values; dispatched?: boolean } | undefined>(undefined)
  const request = useRef<AbortController | undefined>(undefined)
  const callable = current?.definition.action ?? current?.definition.function
  const isDraft = mode === 'draft'
  const hasWrites = callable?.sideEffects === 'dataWrite'
  const awaitingCommit = hasWrites && (retry || invocation?.status === 'committing' || invocation?.status === 'running')
  const diagnosticsHandler = useRef(onDiagnostics)
  diagnosticsHandler.current = onDiagnostics

  useEffect(() => {
    request.current?.abort(); request.current = undefined; pending.current = undefined
    setOpened(false); setCurrent(undefined); setDrafts({}); setInvocation(undefined); setBusy(false); setError(''); setRetry(false)
    diagnosticsHandler.current?.()
    return () => request.current?.abort()
  }, [api, definitionId, tenantId, active, mode, draftEtag, blocked, dataWrite])

  function close() {
    if (awaitingCommit || hasWrites && busy) return
    request.current?.abort(); request.current = undefined; pending.current = undefined
    setOpened(false); setCurrent(undefined); setDrafts({}); setInvocation(undefined); setBusy(false); setError(''); setRetry(false)
    onDiagnostics?.()
  }
  async function open() {
    if (blocked || isDraft && (!draftEtag || dataWrite)) return
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setOpened(true); setBusy(true); setError(''); setCurrent(undefined); setInvocation(undefined); pending.current = undefined
    try {
      const [record, capabilities] = await Promise.all([isDraft ? api.getDraft(definitionId, controller.signal) : api.getCurrent(definitionId, controller.signal), api.executionCapabilities(controller.signal)])
      if (controller.signal.aborted) return
      if (record.definition.id !== definitionId || record.definition.tenantId !== tenantId || record.status !== (isDraft ? 'draft' : 'active') || isDraft && record.etag !== draftEtag || !['action', 'function'].includes(record.definition.kind)) throw new Error(isDraft ? '已保存草稿已改变，请载入服务器最新内容。' : '当前生效定义已改变，请刷新模型目录。')
      const target = record.definition.action ?? record.definition.function!
      if (isDraft && target.sideEffects === 'dataWrite') throw new Error('数据写入草稿不允许试运行；请发布后通过当前用户会话正式调用。')
      if (!['none', 'dataWrite'].includes(target.sideEffects) || record.definition.function && !target.runtime || record.definition.action && !target.runtime && !target.functionId || target.runtime && (!['python', 'javascript', 'go'].includes(target.runtime.language) || !!target.functionId || !!target.artifactRef)) throw new Error('此定义尚未配置可运行的源码实现；外部系统写入暂不执行。')
      if (target.sideEffects === 'dataWrite' && !capabilities.dataWriteTransactions) throw new Error('执行服务尚未开放事务写入，请检查执行与数据服务配置。')
      // A bound Action does not require definition.read on its Function. Its
      // implementation and runtime availability are checked by Execution.
      if (!capabilities.invocation || target.runtime && !capabilities.runtimes.includes(target.runtime.language)) throw new Error(`${target.runtime ? languageLabels[target.runtime.language] + ' ' : ''}执行服务尚未就绪，请检查本地执行服务。`)
      if (isDraft && !capabilities.draftInvocation) throw new Error('执行服务尚未开放草稿试运行，请更新本地执行服务。')
      if (record.definition.action && target.runtime && !capabilities.actionInlineSource) throw new Error('执行服务尚未开放动作独立源码，请更新本地执行服务。')
      setCurrent(record); setDrafts(initialDrafts(target.input ?? []))
    } catch (cause) {
      if (!controller.signal.aborted) { setCurrent(undefined); setInvocation(undefined); setError(errorMessage(cause)) }
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  function change(id: string, patch: Partial<FieldDrafts[string]>) {
    if (hasWrites && pending.current?.dispatched) return
    setDrafts((values) => ({ ...values, [id]: { ...values[id], ...patch } })); setInvocation(undefined); setError(''); setRetry(false); pending.current = undefined
    onDiagnostics?.()
  }
  function receive(result: Invocation) {
    if (result.definitionId !== definitionId || !Object.hasOwn(statuses, result.status)) throw new Error('执行结果与当前定义不一致，请重新读取。')
    if (isDraft ? result.mode !== 'draft' || result.draftEtag !== draftEtag : result.mode === 'draft') throw new Error('执行结果与当前运行模式不一致，请重新读取。')
    if (hasWrites && result.status === 'succeeded' && result.transaction && !result.transaction.committed) throw new Error('事务尚未确认提交，请保留原调用并核对结果。')
    setInvocation(result); setRetry(false)
    onDiagnostics?.(result)
    if (result.status !== 'running' && result.status !== 'committing') pending.current = undefined
  }
  function fail(cause: unknown) {
    onDiagnostics?.()
    if (hasWrites && pending.current?.dispatched) {
      // An HTTP error does not establish the outcome of a dispatched write.
      // Keep its identity and input until Execution returns an authoritative end state.
      setInvocation((value) => value ? { ...value, output: undefined, diagnostics: undefined, error: undefined } : undefined)
      setRetry(true)
      setError(errorMessage(cause))
      return
    }
    setInvocation(undefined)
    const uncertain = !(cause instanceof ApiError) || cause.status === 0 || cause.status >= 500
    setRetry(uncertain && !!pending.current)
    if (!uncertain) pending.current = undefined
    if (cause instanceof ApiError && [401, 403, 404, 409, 412].includes(cause.status)) setCurrent(undefined)
    setError(errorMessage(cause))
  }
  async function run(resume = false) {
    if (!current || !callable || busy || blocked) return
    if (hasWrites && pending.current?.dispatched && !resume) return
    let input: Values
    try { input = resume && pending.current ? pending.current.input : buildWriteValues(callable.input ?? [], drafts) } catch (cause) { setError(errorMessage(cause)); return }
    const signature = `${current.etag}:${writeIntentSignature(input)}`
    if (resume && !pending.current) return
    if (pending.current?.signature !== signature) pending.current = { signature, key: crypto.randomUUID(), input }
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setBusy(true); setError(''); if (!resume) setInvocation(undefined)
    onDiagnostics?.()
    try {
      if (!resume) {
        const record = isDraft ? await api.getDraft(definitionId, controller.signal) : await api.getCurrent(definitionId, controller.signal)
        if (controller.signal.aborted) return
        if (record.etag !== current.etag || record.definition.tenantId !== tenantId || record.status !== (isDraft ? 'draft' : 'active')) throw new ApiError(409, 'definition_changed', isDraft ? '草稿已更新，请载入服务器最新内容。' : '当前定义已更新，请重新载入运行参数。')
      }
      pending.current!.dispatched = true
      const result = isDraft ? await api.invokeDraft(definitionId, input, current.etag, pending.current!.key, controller.signal) : await api.invoke(definitionId, input, pending.current!.key, controller.signal)
      if (!controller.signal.aborted) receive(result)
    } catch (cause) { if (!controller.signal.aborted) fail(cause) }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }
  async function refresh() {
    if (!invocation || busy || blocked) return
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setBusy(true); setError(''); if (!hasWrites && invocation.status !== 'committing') setInvocation(undefined)
    try { const result = await api.getInvocation(invocation.id, controller.signal); if (!controller.signal.aborted) receive(result) }
    catch (cause) { if (!controller.signal.aborted) fail(cause) }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }

  if (!active) return null
  return <section className="model-callable-runner" aria-label={isDraft ? '草稿试运行' : '运行当前定义'}>
    <div className="model-section-heading"><div><h3>{isDraft ? '草稿试运行' : '运行当前定义'}</h3><p className="muted">{isDraft ? '使用服务器上已保存的草稿，单独记录试运行结果；当前生效实现继续正常调用。需要编辑权限及运行权限。' : '使用服务器当前生效的代码和参数声明。未发布的草稿不会参与运行。'}</p></div>{opened ? <button type="button" className="button subtle" disabled={!!awaitingCommit || !!hasWrites && busy} onClick={close}><X size={16} />关闭运行</button> : <button type="button" className="button" disabled={blocked || isDraft && (!draftEtag || dataWrite)} onClick={() => void open()}><Play size={16} />{isDraft ? '打开草稿试运行' : '打开运行面板'}</button>}</div>
    {isDraft && dataWrite ? <p className="muted">数据写入草稿不允许试运行；请发布后通过当前用户会话正式调用。</p> : isDraft && (blocked || !draftEtag) && <p className="muted">请先保存草稿，再试运行。</p>}
    {opened && <>
      {error && <div className="notice notice-error" role="alert">{error}{retry && <p>运行响应未确认。保持参数不变并重试可核对同一次调用。</p>}</div>}
      {busy && <p role="status"><LoaderCircle className="model-spin" size={16} />正在确认或执行，请稍候…</p>}
      {!current && !busy && <button type="button" className="button" onClick={() => void open()}><RefreshCw size={16} />重新载入运行参数</button>}
      {current && callable && <>
        <p className="muted">{isDraft ? '已保存草稿' : '当前生效'}：{current.definition.name}。{current.definition.action ? callable.functionId ? '运行动作还需拥有其绑定函数的运行权限。' : '需要当前动作的运行权限。' : '需要当前函数的运行权限。'}</p>
        {hasWrites && <p className="notice">本次正式调用可以提交已声明范围的业务数据变更，执行者仍须拥有实际行列权限。MCP 和外部令牌不能运行此写入定义。</p>}
        <fieldset className="model-fieldset" disabled={busy || invocation?.status === 'running' || !!awaitingCommit}>
          {!!callable.testCases?.length && <label>载入测试样例<select className="field" value="" onChange={(event) => {
            const sample = callable.testCases?.find((item) => item.id === event.target.value)
            if (sample) { setDrafts(testCaseDrafts(callable.input ?? [], sample.input)); setInvocation(undefined); setError(''); setRetry(false); pending.current = undefined; onDiagnostics?.() }
          }}><option value="">选择已保存的样例</option>{callable.testCases.map((sample) => <option value={sample.id} key={sample.id}>{sample.name}</option>)}</select></label>}
          <CallableInputs properties={callable.input ?? []} drafts={drafts} onChange={change} />
          <button type="button" className="button primary" disabled={busy} onClick={() => void run()}><Play size={16} />{retry ? '重试并核对本次运行' : isDraft ? '试运行草稿' : '运行'}</button>
        </fieldset>
        {awaitingCommit && <div className="notice"><p>提交结果尚未确认，请保持此面板。可以核对运行结果，或用原参数和幂等键恢复本次提交；服务端不会重新运行脚本。</p>{(invocation?.status === 'committing' || retry) && <button type="button" className="button" disabled={busy} onClick={() => void run(true)}>恢复本次提交</button>}</div>}
        {invocation && <div className="model-invocation-result" role="region" aria-label={isDraft ? '草稿试运行结果' : '运行结果'}><div className="model-section-heading"><h4>{statuses[invocation.status]}</h4><button type="button" className="button subtle" disabled={busy} onClick={() => void refresh()}><RefreshCw size={15} />核对运行结果</button></div><p className="muted">{isDraft && <span className="badge">草稿试运行</span>}调用标识：{invocation.id}</p>{invocation.error && <p role="alert">{invocation.error.message}</p>}{invocation.diagnostics?.map((item, index) => <p key={index}>{onDiagnosticFocus ? <button type="button" className="button subtle" data-diagnostic onClick={() => onDiagnosticFocus(item)}>第 {item.line} 行{item.column ? `，第 ${item.column} 列` : ''}：{item.message}</button> : `第 ${item.line} 行${item.column ? `，第 ${item.column} 列` : ''}：${item.message}`}</p>)}{invocation.status === 'succeeded' && <dl>{(callable.output ?? []).map((property) => <div key={property.id}><dt>{property.name}</dt><dd>{Object.hasOwn(invocation.output ?? {}, property.id) ? propertyValueText(property, invocation.output![property.id]) : '未返回'}</dd></div>)}</dl>}{invocation.status === 'succeeded' && !(callable.output?.length) && <p>运行完成，无输出参数。</p>}</div>}
        {invocation?.transaction && <p className="muted">{invocation.transaction.committed ? `已提交 ${invocation.transaction.operationCount} 条业务写入。` : invocation.status === 'committing' ? '业务事务仍在核对，尚未确认提交。' : '本次业务事务未提交。'}</p>}
      </>}
    </>}
  </section>
}
