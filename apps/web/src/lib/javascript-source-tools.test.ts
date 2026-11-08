import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

class TestWorker {
  static instances: TestWorker[] = []
  onmessage?: (event: { data: unknown }) => void
  onerror?: (event: { preventDefault: () => void }) => void
  onmessageerror?: () => void
  messages: { id: number; operation: string; source: string }[] = []
  terminate = vi.fn()
  failPost = false
  constructor() { TestWorker.instances.push(this) }
  postMessage(message: { id: number; operation: string; source: string }) { if (this.failPost) throw new Error('thread closed'); this.messages.push(message) }
  reply(index: number, result: unknown) { this.onmessage?.({ data: { id: this.messages[index].id, result } }) }
}

beforeEach(() => { vi.resetModules(); TestWorker.instances = []; vi.stubGlobal('Worker', TestWorker) })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('JavaScript 源码工具线程', () => {
  it('按需启动并复用线程，检查和格式化乱序响应正确对应原调用', async () => {
    const tools = await import('./javascript-source-tools')
    expect(TestWorker.instances).toHaveLength(0)
    const check = tools.checkJavascriptSource('function main(inputs) {}')
    const format = tools.formatJavascriptSource('const x=1')
    const worker = TestWorker.instances[0]
    expect(TestWorker.instances).toHaveLength(1)
    expect(worker.messages.map(({ operation }) => operation)).toEqual(['check', 'format'])
    worker.reply(1, 'const x = 1;\n')
    worker.reply(0, [])
    await expect(check).resolves.toEqual([])
    await expect(format).resolves.toBe('const x = 1;\n')
  })

  it('UTF-8 超预算不启动线程，语法错误拒绝格式化并保留线程供后续检查', async () => {
    const tools = await import('./javascript-source-tools')
    await expect(tools.checkJavascriptSource('中'.repeat(22000))).rejects.toThrow('64 KiB')
    expect(TestWorker.instances).toHaveLength(0)
    const formatting = tools.formatJavascriptSource('const =')
    const failed = expect(formatting).rejects.toThrow('语法错误')
    const worker = TestWorker.instances[0]
    worker.onmessage?.({ data: { id: worker.messages[0].id, error: '源码存在语法错误' } })
    await failed
    const retry = tools.checkJavascriptSource('const x = 1')
    worker.reply(1, [])
    await expect(retry).resolves.toEqual([])
    expect(worker.terminate).not.toHaveBeenCalled()
  })

  it('失联后终止全部等待并可重试，旧线程迟到错误不影响新线程', async () => {
    vi.useFakeTimers()
    const tools = await import('./javascript-source-tools')
    const first = expect(tools.checkJavascriptSource('const x=1')).rejects.toThrow('暂不可用')
    const second = expect(tools.formatJavascriptSource('const y=2')).rejects.toThrow('暂不可用')
    await vi.advanceTimersByTimeAsync(20000)
    await Promise.all([first, second])
    const oldWorker = TestWorker.instances[0]
    expect(oldWorker.terminate).toHaveBeenCalledOnce()
    const retry = tools.checkJavascriptSource('const x=1')
    const newWorker = TestWorker.instances[1]
    oldWorker.onerror?.({ preventDefault: vi.fn() })
    expect(newWorker.terminate).not.toHaveBeenCalled()
    newWorker.reply(0, [])
    await expect(retry).resolves.toEqual([])
  })

  it('消息发送失败立即清理线程及其他等待，允许新线程重试', async () => {
    const tools = await import('./javascript-source-tools')
    const first = expect(tools.checkJavascriptSource('const x=1')).rejects.toThrow('暂不可用')
    TestWorker.instances[0].failPost = true
    await expect(tools.formatJavascriptSource('const y=2')).rejects.toThrow('暂不可用')
    await first
    expect(TestWorker.instances[0].terminate).toHaveBeenCalledOnce()
    const retry = tools.checkJavascriptSource('const x=1')
    TestWorker.instances[1].reply(0, [])
    await expect(retry).resolves.toEqual([])
  })
})
