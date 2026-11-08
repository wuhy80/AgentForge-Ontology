import type { SourceDiagnostic } from './types'

type Task = { resolve: (value: unknown) => void; reject: (reason: Error) => void; timeout: ReturnType<typeof setTimeout> }
let worker: Worker | undefined
let nextID = 0
const pending = new Map<number, Task>()

function stopWorker() {
  worker?.terminate()
  worker = undefined
  for (const task of pending.values()) {
    clearTimeout(task.timeout)
    task.reject(new Error('Python 工具暂不可用，请重试。'))
  }
  pending.clear()
}

function request(operation: 'check' | 'format', source: string): Promise<unknown> {
  if (new TextEncoder().encode(source).byteLength > 65536) return Promise.reject(new Error('源码超过 64 KiB，请缩短源码后重试。'))
  try {
    if (!worker) {
      // A separate thread keeps WASM initialization and parsing off the editor.
      // Vite emits both the worker and the pinned WASM as local static assets.
      worker = new Worker(new URL('./python-source-tools.worker.ts', import.meta.url), { type: 'module' })
      worker.onmessage = (event: MessageEvent<{ id: number; result?: unknown; error?: string }>) => {
        const task = pending.get(event.data.id)
        if (!task) return
        pending.delete(event.data.id)
        clearTimeout(task.timeout)
        if (event.data.error) task.reject(new Error(event.data.error))
        else task.resolve(event.data.result)
      }
      worker.onerror = (event) => { event.preventDefault(); stopWorker() }
      worker.onmessageerror = stopWorker
    }
    const activeWorker = worker
    return new Promise((resolve, reject) => {
      const id = ++nextID
      pending.set(id, { resolve, reject, timeout: setTimeout(stopWorker, 20000) })
      try { activeWorker.postMessage({ id, operation, source }) } catch { stopWorker() }
    })
  } catch {
    stopWorker()
    return Promise.reject(new Error('Python 工具暂不可用，请重试。'))
  }
}

export function checkPythonSource(source: string): Promise<SourceDiagnostic[]> {
  return request('check', source) as Promise<SourceDiagnostic[]>
}

export function formatPythonSource(source: string): Promise<string> {
  return request('format', source) as Promise<string>
}
