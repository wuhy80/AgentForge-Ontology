import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

class TestWorker {
  static instances: TestWorker[] = []
  onmessage?: (event: { data: unknown }) => void
  onerror?: (event: { preventDefault: () => void }) => void
  onmessageerror?: () => void
  messages: { id: number; operation: string; source: string }[] = []
  terminate = vi.fn()
  constructor() { TestWorker.instances.push(this) }
  postMessage(message: { id: number; operation: string; source: string }) { this.messages.push(message) }
  reply(index: number, result: unknown) { this.onmessage?.({ data: { id: this.messages[index].id, result } }) }
}

beforeEach(() => { vi.resetModules(); TestWorker.instances = []; vi.stubGlobal('Worker', TestWorker) })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('Python 源码工具线程', () => {
  it('按需启动线程，检查与格式化复用线程且独立返回', async () => {
    const tools = await import('./python-source-tools')
    expect(TestWorker.instances).toHaveLength(0)
    const check = tools.checkPythonSource('pass')
    const format = tools.formatPythonSource('x=1')
    const worker = TestWorker.instances[0]
    expect(TestWorker.instances).toHaveLength(1)
    expect(worker.messages.map(({ operation }) => operation)).toEqual(['check', 'format'])
    worker.reply(1, 'x = 1\n')
    worker.reply(0, [])
    await expect(check).resolves.toEqual([])
    await expect(format).resolves.toBe('x = 1\n')
  })

  it('UTF-8 源码超预算时不启动线程，错误后允许重试', async () => {
    const tools = await import('./python-source-tools')
    await expect(tools.checkPythonSource('中'.repeat(22000))).rejects.toThrow('64 KiB')
    expect(TestWorker.instances).toHaveLength(0)
    const pending = tools.checkPythonSource('pass')
    const failed = expect(pending).rejects.toThrow('暂不可用')
    TestWorker.instances[0].onerror?.({ preventDefault: vi.fn() })
    await failed
    expect(TestWorker.instances[0].terminate).toHaveBeenCalledOnce()
    const retry = tools.checkPythonSource('pass')
    expect(TestWorker.instances).toHaveLength(2)
    TestWorker.instances[1].reply(0, [])
    await expect(retry).resolves.toEqual([])
  })

  it('失去响应的线程在预算到期后终止并拒绝所有等待，重试使用新线程', async () => {
    vi.useFakeTimers()
    const tools = await import('./python-source-tools')
    const first = expect(tools.checkPythonSource('pass')).rejects.toThrow('暂不可用')
    const second = expect(tools.formatPythonSource('x=1')).rejects.toThrow('暂不可用')
    await vi.advanceTimersByTimeAsync(20000)
    await Promise.all([first, second])
    expect(TestWorker.instances[0].terminate).toHaveBeenCalledOnce()
    const retry = tools.checkPythonSource('pass')
    TestWorker.instances[1].reply(0, [])
    await expect(retry).resolves.toEqual([])
  })
})
