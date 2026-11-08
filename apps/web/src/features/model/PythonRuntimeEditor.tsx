import type { Callable, ScriptRuntime } from '../../lib/types'
import { PythonSourceEditor } from '../../components/PythonSourceEditor'

const initialRuntime = (): ScriptRuntime => ({ language: 'python', source: 'def main(inputs):\n    return {}\n', entrypoint: 'main', timeoutSeconds: 5 })

export function PythonRuntimeEditor({ callable, locked, disabled = false, kind = 'function', onChange }: { callable: Callable; locked: boolean; disabled?: boolean; kind?: 'function' | 'action'; onChange: (callable: Callable) => void }) {
  const runtime = callable.runtime
  const canEnable = !locked || callable.sideEffects === 'none' && !callable.artifactRef && !callable.functionId
  const title = kind === 'action' ? '动作源码' : '函数源码'
  function update(patch: Partial<ScriptRuntime>) { onChange({ ...callable, runtime: { ...runtime!, ...patch } }) }
  return <section className="model-python-editor" aria-label={title}>
    <div className="model-section-heading"><div><h3>{title}</h3><p className="muted">编写无副作用的 Python 计算。保存并发布后，运行当前生效的代码。</p></div></div>
    <label className="model-checkbox"><input type="checkbox" checked={!!runtime} disabled={disabled || (runtime ? locked && kind === 'action' : !canEnable)} onChange={(event) => onChange(event.target.checked ? { ...callable, sideEffects: 'none', artifactRef: undefined, functionId: undefined, runtime: initialRuntime() } : { ...callable, runtime: undefined })} />启用 Python 实现</label>
    {kind === 'action' && !runtime && <p className="muted">启用后使用本动作的独立源码；也可以通过「实现函数」复用已发布的函数。已生效的绑定动作保持原实现方式。</p>}
    {runtime && <>
      <div className="form-grid"><label>执行语言<input className="field" readOnly value="Python 3.12" /></label><label>执行超时（秒）<input className="field" disabled={disabled} type="number" min={1} max={30} step={1} value={runtime.timeoutSeconds} onChange={(event) => update({ timeoutSeconds: Number(event.target.value) })} /></label><label className="model-span-full">入口函数<input className="field" disabled={disabled} value={runtime.entrypoint} onChange={(event) => update({ entrypoint: event.target.value })} spellCheck={false} /></label></div>
      <PythonSourceEditor value={runtime.source} onChange={(source) => update({ source })} disabled={disabled} inputNames={(callable.input ?? []).map((property) => property.name)} outputNames={(callable.output ?? []).map((property) => property.name)} />
      <p className="muted">入口接收 inputs 字典，按下方声明的中文参数名读取，返回以中文输出参数名为键的字典。例如：inputs["原文"]。参数改名时须同步修改源码。整数和精确小数通过字符串传入与返回。</p>
      <p className="muted">可使用 Python 标准库；当前不支持安装依赖、访问网络、调用平台数据接口或写入外部系统。源码最多 64 KiB。</p>
    </>}
  </section>
}
