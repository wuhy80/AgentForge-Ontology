import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, Invocation } from '../../lib/types'
import { CallableRunner } from './CallableRunner'

afterEach(cleanup)

function fixture() {
  const draft: DefinitionRecord = { status: 'draft', etag: 'saved-draft', definition: {
    id: 'fn', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'function', name: '精确计算',
    function: { sideEffects: 'none', runtime: { language: 'python', source: 'def main(inputs):\n    return {"结果": inputs["数值"]}\n', entrypoint: 'main', timeoutSeconds: 5 },
      input: [{ id: 'input-id', name: '数值', dataType: 'int64', required: true }], output: [{ id: 'output-id', name: '结果', dataType: 'int64' }],
      testCases: [{ id: 'case', name: '超大整数', input: { 'input-id': '9007199254740993' } }] },
  } }
  const result: Invocation = { id: 'invocation', definitionId: 'fn', definitionName: '精确计算', kind: 'function', status: 'succeeded', output: { 'output-id': '9007199254740993' }, createdAt: '2026-11-08T00:00:00Z', codeDigest: 'digest', mode: 'draft', draftEtag: 'saved-draft' }
  const api = { getDraft: vi.fn().mockResolvedValue(draft), getCurrent: vi.fn(), executionCapabilities: vi.fn().mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true, draftInvocation: true }), invokeDraft: vi.fn().mockResolvedValue(result), invoke: vi.fn(), getInvocation: vi.fn().mockResolvedValue(result) }
  const props = { api: api as unknown as ApiClient, definitionId: 'fn', tenantId: 'tenant', mode: 'draft' as const, draftEtag: 'saved-draft' }
  return { api, props, draft, result }
}

async function loadSample() {
  await userEvent.click(screen.getByRole('button', { name: '打开草稿试运行' }))
  await userEvent.selectOptions(await screen.findByLabelText('载入测试样例'), 'case')
}

describe('已保存草稿试运行', () => {
  it('按草稿ETag、稳定参数ID和完整精度运行保存样例，独立于current路径', async () => {
    const { api, props } = fixture()
    render(<CallableRunner {...props} />)
    await loadSample()
    expect((screen.getByLabelText('数值int64') as HTMLInputElement).value).toBe('9007199254740993')
    await userEvent.click(screen.getByRole('button', { name: '试运行草稿' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.invokeDraft).toHaveBeenCalledWith('fn', { 'input-id': '9007199254740993' }, 'saved-draft', expect.any(String), expect.any(AbortSignal))
    expect(api.invoke).not.toHaveBeenCalled(); expect(api.getCurrent).not.toHaveBeenCalled()
    expect(screen.getByRole('definition').textContent).toBe('9007199254740993')
  })

  it('未保存修改禁止试跑，修改后迟到结果被丢弃', async () => {
    const { api, props, result } = fixture()
    let resolve: (value: Invocation) => void = () => undefined
    api.invokeDraft.mockImplementation(() => new Promise<Invocation>((done) => { resolve = done }))
    const view = render(<CallableRunner {...props} blocked />)
    expect((screen.getByRole('button', { name: '打开草稿试运行' }) as HTMLButtonElement).disabled).toBe(true)
    expect(api.getDraft).not.toHaveBeenCalled()
    view.rerender(<CallableRunner {...props} />)
    await loadSample()
    await userEvent.click(screen.getByRole('button', { name: '试运行草稿' }))
    await waitFor(() => expect(api.invokeDraft).toHaveBeenCalledTimes(1))
    view.rerender(<CallableRunner {...props} blocked />)
    resolve(result)
    await waitFor(() => expect(screen.queryByRole('region', { name: '草稿试运行结果' })).toBeNull())
    expect(screen.queryByLabelText('数值int64')).toBeNull()
  })

  it('草稿冲突或服务未提供草稿能力时明确拒绝', async () => {
    const { api, props, draft } = fixture()
    api.getDraft.mockResolvedValue({ ...draft, etag: 'new-draft' })
    render(<CallableRunner {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '打开草稿试运行' }))
    expect((await screen.findByRole('alert')).textContent).toContain('草稿已改变')
    expect(api.invokeDraft).not.toHaveBeenCalled()
    api.getDraft.mockResolvedValue(draft)
    api.executionCapabilities.mockResolvedValue({ invocation: true, runtimes: ['python'], synchronous: true })
    await userEvent.click(screen.getByRole('button', { name: '重新载入运行参数' }))
    expect((await screen.findByRole('alert')).textContent).toContain('尚未开放草稿试运行')
  })

  it('丢失响应重用幂等键，返回行号诊断可定位，撤权后清空结果', async () => {
    const { api, props, result } = fixture()
    const diagnostics = [{ code: 'python_runtime_error', message: 'Python 执行失败，请检查此行代码', line: 2 }]
    const failed = { ...result, status: 'failed' as const, output: undefined, error: { code: 'python_failed', message: '执行失败' }, diagnostics }
    api.invokeDraft.mockRejectedValueOnce(new ApiError(0, 'network_error', '响应未确认')).mockResolvedValue(failed)
    const onDiagnostics = vi.fn(), onDiagnosticFocus = vi.fn()
    render(<CallableRunner {...props} onDiagnostics={onDiagnostics} onDiagnosticFocus={onDiagnosticFocus} />)
    await loadSample()
    await userEvent.click(screen.getByRole('button', { name: '试运行草稿' }))
    await userEvent.click(await screen.findByRole('button', { name: '重试并核对本次运行' }))
    expect(await screen.findByText('运行失败')).toBeTruthy()
    expect(api.invokeDraft.mock.calls[0][3]).toBe(api.invokeDraft.mock.calls[1][3])
    expect(onDiagnostics).toHaveBeenLastCalledWith(failed)
    await userEvent.click(screen.getByRole('button', { name: /第 2 行/ }))
    expect(onDiagnosticFocus).toHaveBeenCalledWith(diagnostics[0])
    api.getInvocation.mockRejectedValue(new ApiError(403, 'forbidden', '无权读取草稿'))
    await userEvent.click(screen.getByRole('button', { name: '核对运行结果' }))
    expect((await screen.findByRole('alert')).textContent).toContain('无权读取草稿')
    expect(screen.queryByRole('region', { name: '草稿试运行结果' })).toBeNull()
    expect(screen.queryByLabelText('数值int64')).toBeNull()
    expect(onDiagnostics).toHaveBeenLastCalledWith()
  })

  it('拒绝同定义的current结果及不匹配草稿ETag，修改样例参数需新请求', async () => {
    const { api, props, result } = fixture()
    api.invokeDraft.mockResolvedValueOnce({ ...result, mode: 'current', draftEtag: undefined })
    render(<CallableRunner {...props} />)
    await loadSample()
    await userEvent.click(screen.getByRole('button', { name: '试运行草稿' }))
    expect((await screen.findByRole('alert')).textContent).toContain('运行模式不一致')
    fireEvent.change(screen.getByLabelText('数值int64'), { target: { value: '8' } })
    await userEvent.click(screen.getByRole('button', { name: '试运行草稿' }))
    expect(await screen.findByText('运行成功')).toBeTruthy()
    expect(api.invokeDraft.mock.calls[1][3]).not.toBe(api.invokeDraft.mock.calls[0][3])
  })
})
