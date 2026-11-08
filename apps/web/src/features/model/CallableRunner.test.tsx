import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Invocation } from '../../lib/types'
import { CallableRunner } from './CallableRunner'

afterEach(cleanup)

function fixture() {
  const record: DefinitionRecord = { status: 'active', etag: 'current-etag', definition: {
    id: 'fn', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'function', name: '精确计算',
    function: { sideEffects: 'none', runtime: { language: 'python', source: 'def main(inputs):\n    return {"结果": inputs["数值"]}\n', entrypoint: 'main', timeoutSeconds: 5 },
      input: [{ id: 'input-id', name: '数值', dataType: 'int64', required: true }], output: [{ id: 'output-id', name: '结果', dataType: 'int64' }] },
  } }
  const result: Invocation = { id: 'invocation', definitionId: 'fn', definitionName: '精确计算', kind: 'function', status: 'succeeded', output: { 'output-id': '9007199254740993' }, createdAt: '2026-11-07T00:00:00Z', codeDigest: 'digest' }
  const api = { getCurrent: vi.fn().mockResolvedValue(record), executionCapabilities: vi.fn().mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true }), invoke: vi.fn().mockResolvedValue(result), getInvocation: vi.fn().mockResolvedValue(result) }
  const props = { api: api as unknown as ApiClient, definitionId: 'fn', tenantId: 'tenant' }
  return { api, props, record, result }
}
async function open() { await userEvent.click(screen.getByRole('button', { name: '打开运行面板' })); return screen.findByLabelText('数值int64') }

describe('当前定义运行面板', () => {
  it('事务提交中不显示输出或成功，可查询并按原参数及幂等键恢复提交', async () => {
    const { api, props, record, result } = fixture()
    record.definition.function!.sideEffects = 'dataWrite'
    record.definition.function!.dataWrite = [{ schemaId: 'schema', operations: ['update'] }]
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true, dataWriteTransactions: true } as never)
    api.invoke.mockResolvedValueOnce({ ...result, status: 'committing', transaction: { status: 'pending', committed: false, operationCount: 1 } } as Invocation)
      .mockResolvedValueOnce({ ...result, transaction: { status: 'committed', committed: true, operationCount: 1 } } as Invocation)
    api.getInvocation.mockRejectedValueOnce(new ApiError(409, 'definition_changed', '原事务结果须先核对；请勿更换请求标识重复执行写入'))
    render(<CallableRunner {...props} />)
    const input = await open()
    await userEvent.type(input, '7')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    await screen.findByText('正在确认事务提交')
    expect(screen.queryByText('9007199254740993')).toBeNull()
    expect(screen.queryByText('运行成功')).toBeNull()
    expect((input as HTMLInputElement).disabled || (input.closest('fieldset') as HTMLFieldSetElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '关闭运行' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '核对运行结果' }))
    await waitFor(() => expect(api.getInvocation).toHaveBeenCalledTimes(1))
    expect((await screen.findByRole('alert')).textContent).toContain('原事务结果须先核对')
    expect(screen.getByText('正在确认事务提交')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '重新载入运行参数' })).toBeNull()
    expect((input.closest('fieldset') as HTMLFieldSetElement).disabled).toBe(true)
    expect((input as HTMLInputElement).value).toBe('7')
    await userEvent.click(screen.getByRole('button', { name: '恢复本次提交' }))
    await screen.findByText('运行成功')
    expect(api.invoke.mock.calls[0].slice(0, 3)).toEqual(api.invoke.mock.calls[1].slice(0, 3))
    expect(api.getCurrent).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('definition').textContent).toBe('9007199254740993')
  })

  it('已派发写入收到403仍保留未确认状态，恢复继续复用原参数和请求标识', async () => {
    const { api, props, record, result } = fixture()
    record.definition.function!.sideEffects = 'dataWrite'
    record.definition.function!.dataWrite = [{ schemaId: 'schema', operations: ['update'] }]
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true, dataWriteTransactions: true } as never)
    api.invoke.mockRejectedValueOnce(new ApiError(403, 'forbidden', '当前无权恢复原事务'))
      .mockResolvedValueOnce({ ...result, status: 'failed', error: { code: 'forbidden', message: '本次事务未提交' }, transaction: { status: 'failed', committed: false, operationCount: 1 } } as Invocation)
    render(<CallableRunner {...props} />)
    const input = await open()
    await userEvent.type(input, '7')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect((await screen.findByRole('alert')).textContent).toContain('当前无权恢复原事务')
    expect((input.closest('fieldset') as HTMLFieldSetElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '关闭运行' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByRole('button', { name: '重新载入运行参数' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '恢复本次提交' }))
    await screen.findByText('运行失败')
    expect(api.invoke.mock.calls[0].slice(0, 3)).toEqual(api.invoke.mock.calls[1].slice(0, 3))
    expect(api.getCurrent).toHaveBeenCalledTimes(2)
    expect((input.closest('fieldset') as HTMLFieldSetElement).disabled).toBe(false)
    expect((screen.getByRole('button', { name: '关闭运行' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByRole('definition')).toBeNull()
  })

  it('旧执行服务不支持事务或MCP拒绝时明确提示且不显示旧输出', async () => {
    const { api, props, record } = fixture()
    record.definition.function!.sideEffects = 'dataWrite'
    record.definition.function!.dataWrite = [{ schemaId: 'schema', operations: ['create'] }]
    render(<CallableRunner {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '打开运行面板' }))
    expect((await screen.findByRole('alert')).textContent).toContain('尚未开放事务写入')
    expect(api.invoke).not.toHaveBeenCalled()
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, dataWriteTransactions: true } as never)
    await userEvent.click(screen.getByRole('button', { name: '重新载入运行参数' }))
    await userEvent.type(await screen.findByLabelText('数值int64'), '7')
    api.invoke.mockRejectedValue(new ApiError(403, 'data_write_session_required', 'MCP和外部令牌不允许数据写入调用'))
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect((await screen.findByRole('alert')).textContent).toContain('MCP和外部令牌不允许')
    expect((screen.getByLabelText('数值int64').closest('fieldset') as HTMLFieldSetElement).disabled).toBe(true)
    expect(screen.queryByRole('definition')).toBeNull()
  })

  it('独立源码动作按自身稳定参数运行，保留绑定动作的权限说明', async () => {
    const { api, props, record, result } = fixture()
    const action: DefinitionRecord = { ...record, definition: { ...record.definition, kind: 'action', function: undefined, action: record.definition.function } }
    api.getCurrent.mockResolvedValue(action)
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true })
    api.invoke.mockResolvedValue({ ...result, kind: 'action' })
    render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '7')
    expect(screen.getByText(/需要当前动作的运行权限/)).toBeTruthy()
    expect(screen.queryByText(/绑定函数的运行权限/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.invoke).toHaveBeenCalledWith('fn', { 'input-id': '7' }, expect.any(String), expect.any(AbortSignal))
  })

  it('执行服务尚不支持独立动作时明确拒绝，不提交运行请求', async () => {
    const { api, props, record } = fixture()
    api.getCurrent.mockResolvedValue({ ...record, definition: { ...record.definition, kind: 'action', function: undefined, action: record.definition.function } })
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: false })
    render(<CallableRunner {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '打开运行面板' }))
    expect((await screen.findByRole('alert')).textContent).toContain('尚未开放动作独立源码')
    expect(api.invoke).not.toHaveBeenCalled()
  })

  it('只在打开时检查能力，按服务器当前签名与稳定ID无损提交参数，结束后再次运行使用新幂等键', async () => {
    const { api, props } = fixture()
    render(<CallableRunner {...props} />)
    expect(api.getCurrent).not.toHaveBeenCalled()
    const input = await open()
    await userEvent.type(input, '9007199254740993')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(screen.getByRole('definition').textContent).toBe('9007199254740993')
    expect(api.invoke).toHaveBeenCalledWith('fn', { 'input-id': '9007199254740993' }, expect.any(String), expect.any(AbortSignal))
    const firstKey = api.invoke.mock.calls[0][2]
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    await waitFor(() => expect(api.invoke).toHaveBeenCalledTimes(2))
    expect(api.invoke.mock.calls[1][2]).not.toBe(firstKey)
  })

  it('丢失响应后以同一幂等键核对，修改参数才发起新调用', async () => {
    const { api, props, result } = fixture()
    api.invoke.mockRejectedValueOnce(new ApiError(0, 'network_error', '运行响应未确认')).mockResolvedValue(result)
    render(<CallableRunner {...props} />)
    const input = await open()
    await userEvent.type(input, '7')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    await userEvent.click(await screen.findByRole('button', { name: '重试并核对本次运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.invoke.mock.calls[1][2]).toBe(api.invoke.mock.calls[0][2])
    fireEvent.change(input, { target: { value: '8' } })
    expect(screen.queryByRole('region', { name: '运行结果' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    await waitFor(() => expect(api.invoke).toHaveBeenCalledTimes(3))
    expect(api.invoke.mock.calls[2][2]).not.toBe(api.invoke.mock.calls[0][2])
    expect(api.invoke.mock.calls[2][1]).toEqual({ 'input-id': '8' })
  })

  it('运行前重新读取当前态，拒绝定义变更及未生效定义', async () => {
    const { api, props, record } = fixture()
    api.getCurrent.mockResolvedValueOnce(record).mockResolvedValue({ ...record, etag: 'new-etag' })
    render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '1')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect((await screen.findByRole('alert')).textContent).toContain('当前定义已更新')
    expect(api.invoke).not.toHaveBeenCalled()
    expect(screen.queryByLabelText('数值int64')).toBeNull()
    api.getCurrent.mockResolvedValue({ ...record, status: 'draft' })
    await userEvent.click(screen.getByRole('button', { name: '重新载入运行参数' }))
    expect((await screen.findByRole('alert')).textContent).toContain('当前生效定义已改变')
  })

  it('核对旧结果遭拒后清空输出和参数，不保留已撤销访问的数据', async () => {
    const { api, props } = fixture()
    render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '1')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    api.getInvocation.mockRejectedValue(new ApiError(403, 'forbidden', '当前无权读取结果'))
    await userEvent.click(screen.getByRole('button', { name: '核对运行结果' }))
    expect((await screen.findByRole('alert')).textContent).toContain('当前无权读取结果')
    expect(screen.queryByText('9007199254740993')).toBeNull()
    expect(screen.queryByLabelText('数值int64')).toBeNull()
  })

  it('运行中响应可查询，同定义关闭或切换租户后迟到结果不进入页面', async () => {
    const { api, props, result } = fixture()
    api.invoke.mockResolvedValueOnce({ ...result, status: 'running', output: undefined })
    const view = render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '1')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('正在运行')).toBeTruthy()
    let resolve: (value: Invocation) => void = () => undefined
    api.getInvocation.mockImplementation(() => new Promise<Invocation>((done) => { resolve = done }))
    await userEvent.click(screen.getByRole('button', { name: '核对运行结果' }))
    view.rerender(<CallableRunner {...props} tenantId="other-tenant" />)
    resolve(result)
    await waitFor(() => expect(screen.getByRole('button', { name: '打开运行面板' })).toBeTruthy())
    expect(screen.queryByText('9007199254740993')).toBeNull()
  })

  it('执行服务不可用仅显示运行面板错误', async () => {
    const { api, props } = fixture()
    api.executionCapabilities.mockResolvedValue({ invocation: false, runtimes: ['python'], synchronous: true })
    render(<CallableRunner {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '打开运行面板' }))
    expect((await screen.findByRole('alert')).textContent).toContain('执行服务尚未就绪')
    expect(api.invoke).not.toHaveBeenCalled()
  })

  it.each(['javascript', 'go'] as const)('%s Function 使用对应运行器能力，缺少该语言时明确拒绝', async (language) => {
    const { api, props, record } = fixture()
    record.definition.function!.runtime!.language = language
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: [language], synchronous: true, actionInlineSource: true })
    const rendered = render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '9007199254740993')
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.invoke).toHaveBeenCalledWith('fn', { 'input-id': '9007199254740993' }, expect.any(String), expect.any(AbortSignal))
    rendered.unmount()
    api.invoke.mockClear()
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true })
    render(<CallableRunner {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '打开运行面板' }))
    expect((await screen.findByRole('alert')).textContent).toContain(`${language === 'go' ? 'Go' : 'JavaScript'} 执行服务尚未就绪`)
    expect(api.invoke).not.toHaveBeenCalled()
  })

  it('绑定 Action 由后端解析实际运行器，不额外读取 Function 定义或要求其读取权限', async () => {
    const { api, props, record, result } = fixture()
    api.getCurrent.mockResolvedValue({ ...record, definition: { ...record.definition, kind: 'action', function: undefined, action: { ...record.definition.function!, runtime: undefined, functionId: 'bound-function' } } })
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['go'], synchronous: true, actionInlineSource: false })
    api.invoke.mockResolvedValue({ ...result, kind: 'action' })
    render(<CallableRunner {...props} />)
    await userEvent.type(await open(), '7')
    expect(api.getCurrent).toHaveBeenCalledTimes(1)
    expect(api.getCurrent).toHaveBeenLastCalledWith('fn', expect.any(AbortSignal))
    expect(screen.getByText(/绑定函数的运行权限/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '运行' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.getCurrent).toHaveBeenCalledTimes(2)
    expect(api.getCurrent.mock.calls.every(([id]) => id === 'fn')).toBe(true)
    expect(api.invoke).toHaveBeenCalledOnce()
  })
})
