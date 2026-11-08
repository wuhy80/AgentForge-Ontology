import type { Callable, DefinitionRecord, SourceDiagnostic } from '../../lib/types'
import { PythonRuntimeEditor } from './PythonRuntimeEditor'

export function CallableImplementationEditor({ callable, kind, locked, disabled, functions, onChange, diagnostics, diagnosticFocus }: {
  callable: Callable
  kind: 'function' | 'action'
  locked: boolean
  disabled: boolean
  functions: DefinitionRecord[]
  onChange: (callable: Callable) => void
  diagnostics?: SourceDiagnostic[]
  diagnosticFocus?: SourceDiagnostic
}) {
  return <>
    <section>
      <div className="model-section-heading"><div><h3>实现声明</h3><p className="muted">函数和动作均可编写独立 Python 源码，动作也可绑定函数。当前开放无副作用计算，产物引用仅作登记。</p></div></div>
      <div className="form-grid">
        <label>产物引用<input className="field" disabled={locked || !!callable.runtime} value={callable.artifactRef ?? ''} onChange={(event) => onChange({ ...callable, artifactRef: event.target.value || undefined })} placeholder="例如：registry.example.com/business/handler" /></label>
        <label>副作用<select className="field" disabled={locked || !!callable.runtime} value={callable.sideEffects} onChange={(event) => onChange({ ...callable, sideEffects: event.target.value as Callable['sideEffects'] })}><option value="none">无副作用</option><option value="dataWrite">写入业务数据</option><option value="externalWrite">修改外部系统</option></select></label>
        {kind === 'action' && <label className="model-span-full">实现函数<select className="field" disabled={locked || !!callable.runtime} value={callable.functionId ?? ''} onChange={(event) => onChange({ ...callable, functionId: event.target.value || undefined })}>
          <option value="">{callable.runtime ? '使用本动作的 Python 源码' : '暂不绑定函数'}</option>
          {callable.functionId && !functions.some((entry) => entry.definition.id === callable.functionId) && <option value={callable.functionId}>当前实现函数（无目录权限）</option>}
          {functions.map(({ definition: fn }) => <option key={fn.id} value={fn.id}>{fn.name}</option>)}
        </select></label>}
      </div>
      {kind === 'action' && callable.functionId && <p className="muted">绑定动作的输入、输出中文名称和类型须与函数一致，并声明「无副作用」。</p>}
    </section>
    <PythonRuntimeEditor callable={callable} kind={kind} locked={locked} disabled={disabled} onChange={onChange} diagnostics={diagnostics} diagnosticFocus={diagnosticFocus} />
  </>
}
