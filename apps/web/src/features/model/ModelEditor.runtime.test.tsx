import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ApiClient } from '../../lib/api'
import type { DefinitionRecord } from '../../lib/types'
import { ModelEditor } from './ModelEditor'

afterEach(cleanup)

function fixture(record: DefinitionRecord, entries: DefinitionRecord[] = [record]) {
  const api = { listGroups: vi.fn().mockResolvedValue([{ id: 'group', tenantId: 'tenant', categoryId: 'category', name: '计算' }]), saveDraft: vi.fn().mockImplementation(async (_id, definition) => ({ ...record, status: 'draft', definition, etag: 'saved' })) }
  const props = { api: api as unknown as ApiClient, categoryId: 'category', categories: [{ id: 'category', tenantId: 'tenant', name: '知识库' }], entries, initialRecord: record, initialKind: record.definition.kind, onClose: vi.fn(), onOrganizations: vi.fn(), onSaved: vi.fn().mockResolvedValue(undefined), onPending: vi.fn(), onPublished: vi.fn().mockResolvedValue(undefined) }
  return { api, props }
}

const fn: DefinitionRecord = { status: 'active', etag: 'fn-etag', definition: { id: 'fn', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'function', name: '回传原文', function: { sideEffects: 'none', runtime: { language: 'python', source: 'def main(inputs):\n    return {"结果": inputs["原文"]}', entrypoint: 'main', timeoutSeconds: 5 }, input: [{ id: 'fn-input', name: '原文', dataType: 'string', required: true }], output: [{ id: 'fn-output', name: '结果', dataType: 'string' }] } } }

describe('模型编辑的执行配置', () => {
  it('已生效函数保持参数锁定并允许通过草稿更新Python源码', async () => {
    const { api, props } = fixture(fn)
    render(<ModelEditor {...props} />)
    expect((screen.getByLabelText('输入参数第1项名称') as HTMLInputElement).disabled).toBe(false)
    expect((screen.getByLabelText('输入参数第1项类型') as HTMLSelectElement).disabled).toBe(true)
    const source = screen.getByLabelText('Python 源码') as HTMLTextAreaElement
    expect(source.disabled).toBe(false)
    fireEvent.change(source, { target: { value: 'def main(inputs):\n    return {"结果": inputs["原文"].upper()}' } })
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('fn', expect.objectContaining({ function: expect.objectContaining({ runtime: expect.objectContaining({ source: expect.stringContaining('.upper()') }) }) }), 'fn-etag'))
    expect(screen.getByRole('button', { name: '打开运行面板' })).toBeTruthy()
  })

  it('动作可复制函数中文签名并由服务端分配独立参数身份', async () => {
    const action: DefinitionRecord = { status: 'draft', etag: 'action-etag', definition: { id: 'action', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'action', name: '原文回传动作', action: { functionId: 'fn', sideEffects: 'dataWrite', input: [], output: [] } } }
    const { api, props } = fixture(action, [action, fn])
    render(<ModelEditor {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '从实现函数复制参数声明' }))
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('action', expect.objectContaining({ action: expect.objectContaining({ functionId: 'fn', sideEffects: 'none', input: [{ id: '', name: '原文', dataType: 'string', required: true }], output: [{ id: '', name: '结果', dataType: 'string' }] }) }), 'action-etag'))
    expect(screen.queryByRole('button', { name: '打开运行面板' })).toBeNull()
  })
})
