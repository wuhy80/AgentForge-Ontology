import { useEffect, useId, useRef, useState } from 'react'
import { LoaderCircle, Play, RefreshCw, X } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Invocation, Values } from '../../lib/types'
import { ValueInput } from '../query/InstanceDialog'
import { buildWriteValues, errorMessage, initialDrafts, valueText, writeIntentSignature, type FieldDrafts } from '../query/query'

const statuses: Record<Invocation['status'], string> = { running: '正在运行', succeeded: '运行成功', failed: '运行失败', timed_out: '执行超时', canceled: '执行已取消', interrupted: '执行中断' }

export function CallableRunner({ api, definitionId, tenantId, active = true }: { api: ApiClient; definitionId: string; tenantId: string; active?: boolean }) {
  const [opened, setOpened] = useState(false)
  const [current, setCurrent] = useState<DefinitionRecord>()
  const [drafts, setDrafts] = useState<FieldDrafts>({})
  const [invocation, setInvocation] = useState<Invocation>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(false)
  const pending = useRef<{ signature: string; key: string } | undefined>(undefined)
  const request = useRef<AbortController | undefined>(undefined)
  const componentId = useId()
  const callable = current?.definition.action ?? current?.definition.function

  useEffect(() => {
    request.current?.abort(); request.current = undefined; pending.current = undefined
    setOpened(false); setCurrent(undefined); setDrafts({}); setInvocation(undefined); setBusy(false); setError(''); setRetry(false)
    return () => request.current?.abort()
  }, [api, definitionId, tenantId, active])

  function close() {
    request.current?.abort(); request.current = undefined; pending.current = undefined
    setOpened(false); setCurrent(undefined); setDrafts({}); setInvocation(undefined); setBusy(false); setError(''); setRetry(false)
  }
  async function open() {
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setOpened(true); setBusy(true); setError(''); setCurrent(undefined); setInvocation(undefined); pending.current = undefined
    try {
      const [record, capabilities] = await Promise.all([api.getCurrent(definitionId, controller.signal), api.executionCapabilities(controller.signal)])
      if (controller.signal.aborted) return
      if (record.definition.id !== definitionId || record.definition.tenantId !== tenantId || record.status !== 'active' || !['action', 'function'].includes(record.definition.kind)) throw new Error('当前生效定义已改变，请刷新模型目录。')
      const target = record.definition.action ?? record.definition.function!
      if (target.sideEffects !== 'none' || record.definition.function && !target.runtime || record.definition.action && !target.runtime && !target.functionId || target.runtime && (target.runtime.language !== 'python' || !!target.functionId || !!target.artifactRef)) throw new Error('此定义尚未配置可运行的无副作用 Python 实现。')
      if (!capabilities.invocation || !capabilities.runtimes.includes('python')) throw new Error('Python 执行服务尚未就绪，请检查本地执行服务。')
      if (record.definition.action && target.runtime && !capabilities.actionInlineSource) throw new Error('执行服务尚未开放动作独立源码，请更新本地执行服务。')
      setCurrent(record); setDrafts(initialDrafts(target.input ?? []))
    } catch (cause) {
      if (!controller.signal.aborted) { setCurrent(undefined); setInvocation(undefined); setError(errorMessage(cause)) }
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  function change(id: string, patch: Partial<FieldDrafts[string]>) {
    setDrafts((values) => ({ ...values, [id]: { ...values[id], ...patch } })); setInvocation(undefined); setError(''); setRetry(false); pending.current = undefined
  }
  function receive(result: Invocation) {
    if (result.definitionId !== definitionId || !Object.hasOwn(statuses, result.status)) throw new Error('执行结果与当前定义不一致，请重新读取。')
    setInvocation(result); setRetry(false)
    if (result.status !== 'running') pending.current = undefined
  }
  function fail(cause: unknown) {
    setInvocation(undefined)
    const uncertain = !(cause instanceof ApiError) || cause.status === 0 || cause.status >= 500
    setRetry(uncertain && !!pending.current)
    if (!uncertain) pending.current = undefined
    if (cause instanceof ApiError && [401, 403, 404, 409, 412].includes(cause.status)) setCurrent(undefined)
    setError(errorMessage(cause))
  }
  async function run() {
    if (!current || !callable || busy) return
    let input: Values
    try { input = buildWriteValues(callable.input ?? [], drafts) } catch (cause) { setError(errorMessage(cause)); return }
    const signature = `${current.etag}:${writeIntentSignature(input)}`
    if (pending.current?.signature !== signature) pending.current = { signature, key: crypto.randomUUID() }
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setBusy(true); setError(''); setInvocation(undefined)
    try {
      const record = await api.getCurrent(definitionId, controller.signal)
      if (controller.signal.aborted) return
      if (record.etag !== current.etag || record.definition.tenantId !== tenantId) throw new ApiError(409, 'definition_changed', '当前定义已更新，请重新载入运行参数。')
      const result = await api.invoke(definitionId, input, pending.current!.key, controller.signal)
      if (!controller.signal.aborted) receive(result)
    } catch (cause) { if (!controller.signal.aborted) fail(cause) }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }
  async function refresh() {
    if (!invocation || busy) return
    const controller = new AbortController()
    request.current?.abort(); request.current = controller
    setBusy(true); setError(''); setInvocation(undefined)
    try { const result = await api.getInvocation(invocation.id, controller.signal); if (!controller.signal.aborted) receive(result) }
    catch (cause) { if (!controller.signal.aborted) fail(cause) }
    finally { if (!controller.signal.aborted) setBusy(false) }
  }

  if (!active) return null
  return <section className="model-callable-runner" aria-label="运行当前定义">
    <div className="model-section-heading"><div><h3>运行当前定义</h3><p className="muted">使用服务器当前生效的代码和参数声明。未发布的草稿不会参与运行。</p></div>{opened ? <button type="button" className="button subtle" onClick={close}><X size={16} />关闭运行</button> : <button type="button" className="button" onClick={() => void open()}><Play size={16} />打开运行面板</button>}</div>
    {opened && <>
      {error && <div className="notice notice-error" role="alert">{error}{retry && <p>运行响应未确认。保持参数不变并重试可核对同一次调用。</p>}</div>}
      {busy && <p role="status"><LoaderCircle className="model-spin" size={16} />正在确认或执行，请稍候…</p>}
      {!current && !busy && <button type="button" className="button" onClick={() => void open()}><RefreshCw size={16} />重新载入运行参数</button>}
      {current && callable && <>
        <p className="muted">当前生效：{current.definition.name}。{current.definition.action ? callable.functionId ? '运行动作还需拥有其绑定函数的运行权限。' : '需要当前动作的运行权限。' : '需要当前函数的运行权限。'}</p>
        <fieldset className="model-fieldset" disabled={busy || invocation?.status === 'running'}>
          <div className="model-invocation-inputs">{(callable.input ?? []).map((property, index) => {
            const draft = drafts[property.id] ?? { mode: 'omit', text: '' }
            const inputId = `${componentId}-input-${index}`
            return <div className="model-invocation-input" key={property.id}><label htmlFor={inputId}>{property.name}<small className="muted">{property.dataType}</small></label><select className="field" aria-label={`${property.name}的传入方式`} value={draft.mode} onChange={(event) => change(property.id, { mode: event.target.value as FieldDrafts[string]['mode'] })}><option value="value">填写值</option><option value="omit">不传入 / 使用默认值</option>{property.nullable && <option value="null">显式 NULL</option>}</select><ValueInput property={property} id={inputId} disabled={draft.mode !== 'value'} value={draft.text} onChange={(text) => change(property.id, { text })} />{draft.mode === 'omit' && Object.hasOwn(property, 'default') && <small className="muted">默认值：{valueText(property.default)}</small>}</div>
          })}</div>
          {!(callable.input?.length) && <p className="muted">此定义没有输入参数。</p>}
          <button type="button" className="button primary" disabled={busy} onClick={() => void run()}><Play size={16} />{retry ? '重试并核对本次运行' : '运行'}</button>
        </fieldset>
        {invocation && <div className="model-invocation-result" role="region" aria-label="运行结果"><div className="model-section-heading"><h4>{statuses[invocation.status]}</h4><button type="button" className="button subtle" disabled={busy} onClick={() => void refresh()}><RefreshCw size={15} />核对运行结果</button></div><p className="muted">调用标识：{invocation.id}</p>{invocation.error && <p role="alert">{invocation.error.message}</p>}{invocation.status === 'succeeded' && <dl>{(callable.output ?? []).map((property) => <div key={property.id}><dt>{property.name}</dt><dd>{Object.hasOwn(invocation.output ?? {}, property.id) ? valueText(invocation.output![property.id]) : '未返回'}</dd></div>)}</dl>}{invocation.status === 'succeeded' && !(callable.output?.length) && <p>运行完成，无输出参数。</p>}</div>}
      </>}
    </>}
  </section>
}
