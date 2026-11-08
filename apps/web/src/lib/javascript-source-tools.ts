import type { SourceDiagnostic } from './types'

let worker: Worker | undefined
let nextID = 0
const pending = new Map<number, { resolve: (value: unknown) => void; reject: (reason: Error) => void; timeout: ReturnType<typeof setTimeout> }>()
function stopWorker() {
  worker?.terminate(); worker = undefined
  for (const task of pending.values()) { clearTimeout(task.timeout); task.reject(new Error('JavaScript 工具暂不可用，请重试。')) }
  pending.clear()
}
function request(operation: 'check' | 'format', source: string): Promise<unknown> {
  if (new TextEncoder().encode(source).byteLength > 65536) return Promise.reject(new Error('源码超过 64 KiB，请缩短源码后重试。'))
  try {
    if (!worker) {
      worker = new Worker(new URL('./javascript-source-tools.worker.ts', import.meta.url), { type: 'module' })
      const createdWorker = worker
      createdWorker.onmessage = (event: MessageEvent<{ id: number; result?: unknown; error?: string }>) => {
        if (worker !== createdWorker) return
        const task = pending.get(event.data.id)
        if (!task) return
        pending.delete(event.data.id); clearTimeout(task.timeout)
        if (event.data.error) task.reject(new Error(event.data.error)); else task.resolve(event.data.result)
      }
      createdWorker.onerror = (event) => { event.preventDefault(); if (worker === createdWorker) stopWorker() }
      createdWorker.onmessageerror = () => { if (worker === createdWorker) stopWorker() }
    }
    const activeWorker = worker
    return new Promise((resolve, reject) => {
      const id = ++nextID
      pending.set(id, { resolve, reject, timeout: setTimeout(stopWorker, 20000) })
      try { activeWorker.postMessage({ id, operation, source }) } catch { stopWorker() }
    })
  } catch { stopWorker(); return Promise.reject(new Error('JavaScript 工具暂不可用，请重试。')) }
}
export function checkJavascriptSource(source: string): Promise<SourceDiagnostic[]> { return request('check', source) as Promise<SourceDiagnostic[]> }
export function formatJavascriptSource(source: string): Promise<string> { return request('format', source) as Promise<string> }
