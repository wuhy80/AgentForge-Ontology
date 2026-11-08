import type { Callable, DefinitionRecord, SourceDiagnostic } from '../../lib/types'
import type { ApiClient } from '../../lib/api'
import { PythonRuntimeEditor } from './PythonRuntimeEditor'
import { DataWriteCapabilitiesEditor } from './DataWriteCapabilitiesEditor'
import { dataWriteTemplates, initialRuntime, runtimeTemplates } from '../../lib/runtime-language'

export function CallableImplementationEditor({ callable, kind, locked, disabled, functions, schemas = [], onChange, diagnostics, diagnosticFocus, api, definitionId }: {
  callable: Callable
  kind: 'function' | 'action'
  locked: boolean
  disabled: boolean
  functions: DefinitionRecord[]
  schemas?: DefinitionRecord[]
  onChange: (callable: Callable) => void
  diagnostics?: SourceDiagnostic[]
  diagnosticFocus?: SourceDiagnostic
  api?: ApiClient
  definitionId?: string
}) {
  return <>
    <section>
      <div className="model-section-heading"><div><h3>实现声明</h3><p className="muted">函数和动作均可编写独立 Python、JavaScript 或 Go 源码，动作也可绑定函数。支持纯计算和声明范围的事务写入，产物引用仅作登记。</p></div></div>
      <div className="form-grid">
        <label>产物引用<input className="field" disabled={locked || !!callable.runtime} value={callable.artifactRef ?? ''} onChange={(event) => onChange({ ...callable, artifactRef: event.target.value || undefined })} placeholder="例如：registry.example.com/business/handler" /></label>
        <label>副作用<select className="field" disabled={locked || disabled} value={callable.sideEffects} onChange={(event) => {
          const next = event.target.value as Callable['sideEffects'], runtime = callable.runtime
          const template = runtime && [runtimeTemplates[runtime.language], dataWriteTemplates[runtime.language]].includes(runtime.source)
          onChange({ ...callable, sideEffects: next, dataWrite: next === 'dataWrite' ? callable.dataWrite ?? [] : undefined,
            runtime: runtime && template ? initialRuntime(runtime.language, next === 'dataWrite') : runtime })
        }}><option value="none">无副作用</option><option value="dataWrite">写入业务数据</option><option value="externalWrite" disabled={!!callable.runtime}>修改外部系统（尚未执行）</option></select></label>
        {kind === 'action' && <label className="model-span-full">实现函数<select className="field" disabled={locked || !!callable.runtime} value={callable.functionId ?? ''} onChange={(event) => onChange({ ...callable, functionId: event.target.value || undefined })}>
          <option value="">{callable.runtime ? '使用本动作的独立源码' : '暂不绑定函数'}</option>
          {callable.functionId && !functions.some((entry) => entry.definition.id === callable.functionId) && <option value={callable.functionId}>当前实现函数（无目录权限）</option>}
          {functions.map(({ definition: fn }) => <option key={fn.id} value={fn.id}>{fn.name}</option>)}
        </select></label>}
      </div>
      {kind === 'action' && callable.functionId && <p className="muted">绑定动作的输入、输出中文名称、类型、副作用及业务写入范围须与函数完全一致。</p>}
    </section>
    <DataWriteCapabilitiesEditor callable={callable} schemas={schemas} disabled={locked || disabled} onChange={onChange} />
    <PythonRuntimeEditor callable={callable} kind={kind} locked={locked} disabled={disabled} onChange={onChange} diagnostics={diagnostics} diagnosticFocus={diagnosticFocus} api={api} definitionId={definitionId} />
  </>
}
