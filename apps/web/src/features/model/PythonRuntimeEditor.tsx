import { useEffect, useRef, useState } from 'react'
import type { Callable, ScriptLanguage, SourceDiagnostic } from '../../lib/types'
import type { ApiClient } from '../../lib/api'
import { PythonSourceEditor } from '../../components/PythonSourceEditor'
import { parameterReferenceLines } from '../../lib/python-parameter-references'
import { dataWriteSDKSignatures, dataWriteTemplates, initialRuntime, languageLabels, runtimeTemplates } from '../../lib/runtime-language'

export function PythonRuntimeEditor({ callable, locked, disabled = false, kind = 'function', onChange, diagnostics, diagnosticFocus, api, definitionId }: { callable: Callable; locked: boolean; disabled?: boolean; kind?: 'function' | 'action'; onChange: (callable: Callable) => void; diagnostics?: SourceDiagnostic[]; diagnosticFocus?: SourceDiagnostic; api?: ApiClient; definitionId?: string }) {
  const runtime = callable.runtime
  const [selectedLanguage, setSelectedLanguage] = useState<ScriptLanguage>(runtime?.language ?? 'python')
  const [pendingLanguage, setPendingLanguage] = useState<ScriptLanguage>()
  const language = runtime?.language ?? selectedLanguage
  const languageLabel = languageLabels[language]
  const toolRequests = useRef(new Set<AbortController>())
  useEffect(() => {
    const requests = toolRequests.current
    return () => { for (const controller of requests) controller.abort(); requests.clear() }
  }, [api, definitionId, language])
  const dataWrite = callable.sideEffects === 'dataWrite'
  const templates = dataWrite ? dataWriteTemplates : runtimeTemplates
  const canEnable = callable.sideEffects !== 'externalWrite' && (!locked || !callable.artifactRef && !callable.functionId)
  const title = kind === 'action' ? '动作源码' : '函数源码'
  const previousNames = useRef(new Map<string, string>())
  const renamedNames = useRef(new Map<string, Set<string>>())
  const [referenceHints, setReferenceHints] = useState<string[]>([])
  const parameterSignature = JSON.stringify([callable.input ?? [], callable.output ?? []].map((parameters) => parameters.map(({ id, name }) => ({ id, name }))))
  useEffect(() => {
    const parameters = JSON.parse(parameterSignature) as { id: string; name: string }[][]
    const currentNames = new Map<string, string>()
    const hints: string[] = []
    parameters.forEach((properties, index) => properties.forEach(({ id, name }) => {
      if (!id) return
      const key = `${index}:${id}`
      currentNames.set(key, name)
      const before = previousNames.current.get(key)
      if (before !== undefined && before !== name) {
        const history = renamedNames.current.get(key) ?? new Set<string>()
        history.add(before)
        renamedNames.current.set(key, history)
      }
      for (const oldName of renamedNames.current.get(key) ?? []) {
        if (oldName === name) continue
        const lines = parameterReferenceLines(runtime?.source ?? '', oldName, language)
        if (lines.length) hints.push(`${index === 0 ? '输入' : '输出'}参数「${oldName}」已改为「${name}」，第 ${lines.join('、')} 行仍可能使用旧名称，请核对源码引用。`)
      }
    }))
    for (const key of renamedNames.current.keys()) if (!currentNames.has(key)) renamedNames.current.delete(key)
    previousNames.current = currentNames
    setReferenceHints(hints)
  }, [parameterSignature, runtime?.source, language])
  async function sourceTool(source: string, operation: 'check' | 'format') {
    if (!api || !definitionId || !runtime) throw new Error('请先保存草稿，再使用 Go 检查与格式化。')
    const controller = new AbortController()
    toolRequests.current.add(controller)
    try { return await api.sourceTools(definitionId, { ...runtime, source }, operation, controller.signal) }
    finally { toolRequests.current.delete(controller) }
  }
  function applyLanguage(next: ScriptLanguage) {
    setSelectedLanguage(next); setPendingLanguage(undefined)
    if (runtime) onChange({ ...callable, runtime: initialRuntime(next, dataWrite) })
  }
  function changeLanguage(next: ScriptLanguage) {
    if (disabled || next === language) return
    if (runtime && (runtime.source !== templates[language] || runtime.entrypoint !== initialRuntime(language).entrypoint)) setPendingLanguage(next)
    else applyLanguage(next)
  }
  function update(patch: Partial<NonNullable<Callable['runtime']>>) { onChange({ ...callable, runtime: { ...runtime!, ...patch } }) }
  return <section className="model-python-editor" aria-label={title}>
    <div className="model-section-heading"><div><h3>{title}</h3><p className="muted">{dataWrite ? '编写暂存业务写入计划的源码。请先声明写入范围，发布后通过当前用户会话正式调用；草稿不执行数据写入。' : '编写无副作用的 Python、JavaScript 或 Go 计算。保存草稿后可试运行，发布后用于正式调用。'}</p></div></div>
    <label>执行语言<select className="field" disabled={disabled || !runtime && !canEnable} value={language} onChange={(event) => changeLanguage(event.target.value as ScriptLanguage)}><option value="python">Python 3.12</option><option value="javascript">JavaScript</option><option value="go">Go</option></select></label>
    {pendingLanguage && <div className="notice" role="alert"><p>切换语言将用 {languageLabels[pendingLanguage]} 模板替换当前源码、入口和超时配置。请先保留需要的代码。</p><div className="model-inline-actions"><button type="button" className="button" disabled={disabled} onClick={() => applyLanguage(pendingLanguage)}>确认切换语言</button><button type="button" className="button subtle" onClick={() => setPendingLanguage(undefined)}>保留当前源码</button></div></div>}
    <label className="model-checkbox"><input type="checkbox" checked={!!runtime} disabled={disabled || (runtime ? locked && kind === 'action' : !canEnable)} onChange={(event) => onChange(event.target.checked ? { ...callable, sideEffects: dataWrite ? 'dataWrite' : 'none', artifactRef: undefined, functionId: undefined, runtime: initialRuntime(language, dataWrite) } : { ...callable, runtime: undefined })} />启用 {languageLabel} 实现</label>
    {kind === 'action' && !runtime && <p className="muted">启用后使用本动作的独立源码；也可以通过「实现函数」复用已发布的函数。已生效的绑定动作保持原实现方式。</p>}
    {runtime && <>
      <div className="form-grid"><label>执行超时（秒）<input className="field" disabled={disabled} type="number" min={1} max={30} step={1} value={runtime.timeoutSeconds} onChange={(event) => update({ timeoutSeconds: Number(event.target.value) })} /></label><label className="model-span-full">入口函数<input className="field" disabled={disabled} value={runtime.entrypoint} onChange={(event) => update({ entrypoint: event.target.value })} spellCheck={false} /></label></div>
      <PythonSourceEditor key={language} language={language} value={runtime.source} onChange={(source) => update({ source })} disabled={disabled} inputNames={(callable.input ?? []).map((property) => property.name)} outputNames={(callable.output ?? []).map((property) => property.name)} diagnostics={diagnostics} diagnosticFocus={diagnosticFocus} sourceTools={language === 'go' && api && definitionId ? { check: (source) => sourceTool(source, 'check'), format: (source) => sourceTool(source, 'format') } : undefined} />
      {referenceHints.length > 0 && <ul className="python-parameter-hints" aria-label="参数改名引用提醒">{referenceHints.map((hint) => <li key={hint}>{hint}</li>)}</ul>}
      <p className="muted">{language === 'go' ? `源码须声明 package main，入口签名为 func Main(inputs map[string]any${dataWrite ? ', ctx *OntologyContext' : ''}) (map[string]any, error)，可改入口名称；返回结果与 error，成功时 error 为 nil。` : language === 'javascript' ? `入口为普通同步函数 function main(inputs${dataWrite ? ', ctx' : ''})，返回普通对象；不支持模块导入、npm 依赖或 Promise 结果。` : `入口接收 inputs 字典${dataWrite ? '及 ctx 上下文' : ''}，返回字典。`}按下方声明的中文参数名读取和返回，例如：inputs["原文"]。参数改名时须同步修改源码。整数和精确小数通过字符串传入与返回。</p>
      {dataWrite && <div className="notice"><p>{language === 'go' ? 'ctx.Data 提供 Create、Update、Delete、Upsert、BulkCreate、BulkUpdate、BulkDelete、BulkUpsert。' : 'ctx.data 提供 create、update、delete、upsert、bulk_create、bulk_update、bulk_delete、bulk_upsert。'}这些方法只暂存计划，不读取、不立即写入、不返回新 ID；任何计划错误都使整次调用失败，即使源码捕获异常。</p><p>schemaId、字段和实例使用稳定 ID；clientKey 由调用者提供，If-Match 使用带双引号的强 ETag。最多展开 100 条操作，全部通过授权和输出校验后统一提交。</p></div>}
      {dataWrite && <details><summary>写入方法签名</summary><pre>{dataWriteSDKSignatures[language]}</pre><p className="muted">values 使用字段稳定 ID；match 为 id 或 indexId 加 values；items 是含 clientKey、values / id / ifMatch / match 的条目数组。Go 的 match 使用 OntologyMatch，items 使用 []OntologyWriteItem；策略空字符串等同 check。覆盖仍要求运行者具备额外权限。</p></details>}
      <p className="muted">{language === 'go' ? '可使用 Go 标准库；编译与执行共用超时，建议保留 30 秒。' : language === 'python' ? '可使用 Python 标准库。' : '可使用 JavaScript 基础内建对象，如 Math、JSON。'}不支持安装依赖、访问网络或写入外部系统。{!dataWrite && '纯计算不提供数据接口。'}源码最多 64 KiB。</p>
    </>}
  </section>
}
