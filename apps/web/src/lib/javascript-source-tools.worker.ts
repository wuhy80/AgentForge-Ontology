import { analyzeJavascript } from './javascript-analysis'

self.onmessage = async (event: MessageEvent<{ id: number; operation: 'check' | 'format'; source: string }>) => {
  const { id, operation, source } = event.data
  try {
    const result = await analyzeJavascript(source)
    if (operation === 'format' && result.diagnostics.length) self.postMessage({ id, error: '源码存在语法错误，请先修正标红位置再格式化。' })
    else self.postMessage({ id, result: operation === 'format' ? result.source : result.diagnostics })
  } catch { self.postMessage({ id, error: 'JavaScript 工具处理失败，请重试。' }) }
}
