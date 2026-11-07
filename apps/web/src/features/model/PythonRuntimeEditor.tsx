import type { Callable, ScriptRuntime } from '../../lib/types'

const initialRuntime = (): ScriptRuntime => ({ language: 'python', source: 'def main(inputs):\n    return {}\n', entrypoint: 'main', timeoutSeconds: 5 })

export function PythonRuntimeEditor({ callable, locked, onChange }: { callable: Callable; locked: boolean; onChange: (callable: Callable) => void }) {
  const runtime = callable.runtime
  const canEnable = !locked || callable.sideEffects === 'none' && !callable.artifactRef
  function update(patch: Partial<ScriptRuntime>) { onChange({ ...callable, runtime: { ...runtime!, ...patch } }) }
  return <section className="model-python-editor">
    <div className="model-section-heading"><div><h3>Python 实现</h3><p className="muted">编写无副作用的计算。保存并发布后，运行当前生效的代码。</p></div></div>
    <label className="model-checkbox"><input type="checkbox" checked={!!runtime} disabled={!runtime && !canEnable} onChange={(event) => onChange(event.target.checked ? { ...callable, sideEffects: 'none', artifactRef: undefined, runtime: initialRuntime() } : { ...callable, runtime: undefined })} />启用 Python 实现</label>
    {runtime && <>
      <div className="form-grid"><label>执行语言<input className="field" readOnly value="Python 3.12" /></label><label>执行超时（秒）<input className="field" type="number" min={1} max={30} step={1} value={runtime.timeoutSeconds} onChange={(event) => update({ timeoutSeconds: Number(event.target.value) })} /></label><label className="model-span-full">入口函数<input className="field" value={runtime.entrypoint} onChange={(event) => update({ entrypoint: event.target.value })} spellCheck={false} /></label></div>
      <label className="model-code-label">Python 源码<textarea className="field model-python-source" rows={14} value={runtime.source} onChange={(event) => update({ source: event.target.value })} autoCapitalize="off" autoCorrect="off" spellCheck={false} /></label>
      <p className="muted">入口接收 inputs 字典，按下方声明的中文参数名读取，返回以中文输出参数名为键的字典。例如：inputs["原文"]。参数改名时须同步修改源码。整数和精确小数通过字符串传入与返回。</p>
      <p className="muted">可使用 Python 标准库；当前不支持安装依赖、访问网络、调用平台数据接口或写入外部系统。源码最多 64 KiB。</p>
    </>}
  </section>
}
