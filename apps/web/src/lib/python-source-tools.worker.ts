import init, { PositionEncoding, Workspace } from '@astral-sh/ruff-wasm-web'
import wasmURL from '@astral-sh/ruff-wasm-web/ruff_wasm_bg.wasm?url'
import { analyzePython, pythonToolSettings } from './python-analysis'

let workspace: Promise<Workspace> | undefined
function load() {
  if (!workspace) workspace = init({ module_or_path: wasmURL })
    .then(() => new Workspace(pythonToolSettings, PositionEncoding.Utf32))
    .catch((error: unknown) => { workspace = undefined; throw error })
  return workspace
}

self.onmessage = async (event: MessageEvent<{ id: number; operation: 'check' | 'format'; source: string }>) => {
  const { id, operation, source } = event.data
  try {
    const tools = await load()
    const diagnostics = analyzePython(tools, source)
    if (operation === 'format' && diagnostics.length) {
      self.postMessage({ id, error: '源码存在语法错误，请先修正标红位置再格式化。' })
      return
    }
    self.postMessage({ id, result: operation === 'format' ? tools.format(source) : diagnostics })
  } catch {
    self.postMessage({ id, error: 'Python 工具加载或处理失败，请重试。' })
  }
}
