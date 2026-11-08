import { afterEach, describe, expect, it, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ApiClient } from '../../lib/api'
import type { DefinitionRecord } from '../../lib/types'
import { ModelEditor } from './ModelEditor'
import { initialRuntime } from '../../lib/runtime-language'

afterEach(cleanup)

function fixture(record: DefinitionRecord, entries: DefinitionRecord[] = [record]) {
  const api = { listGroups: vi.fn().mockResolvedValue([{ id: 'group', tenantId: 'tenant', categoryId: 'category', name: '计算' }]), saveDraft: vi.fn().mockImplementation(async (_id, definition) => ({ ...record, status: 'draft', definition, etag: 'saved' })) }
  const props = { api: api as unknown as ApiClient, categoryId: 'category', categories: [{ id: 'category', tenantId: 'tenant', name: '知识库' }], entries, initialRecord: record, initialKind: record.definition.kind, onClose: vi.fn(), onOrganizations: vi.fn(), onSaved: vi.fn().mockResolvedValue(undefined), onPending: vi.fn(), onPublished: vi.fn().mockResolvedValue(undefined) }
  return { api, props }
}

const fn: DefinitionRecord = { status: 'active', etag: 'fn-etag', definition: { id: 'fn', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'function', name: '回传原文', function: { sideEffects: 'none', runtime: { language: 'python', source: 'def main(inputs):\n    return {"结果": inputs["原文"]}', entrypoint: 'main', timeoutSeconds: 5 }, input: [{ id: 'fn-input', name: '原文', dataType: 'string', required: true }], output: [{ id: 'fn-output', name: '结果', dataType: 'string' }] } } }

function editSource(source: string) {
  const editor = screen.getByRole('textbox', { name: 'Python 源码' })
  const view = EditorView.findFromDOM(editor)!
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: source } }))
  return editor
}

describe('模型编辑的执行配置', () => {
  it('配置事务写入对象及基本操作保存，切回纯计算清除范围，草稿写入禁用', async () => {
    const draft = structuredClone(fn)
    draft.status = 'draft'; draft.definition.function!.runtime = initialRuntime('python')
    const schema: DefinitionRecord = { status: 'active', etag: 'schema-etag', definition: { id: 'schema', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '订单', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } }
    const { api, props } = fixture(draft, [draft, schema])
    render(<ModelEditor {...props} />)
    await userEvent.selectOptions(screen.getByLabelText('副作用'), 'dataWrite')
    await userEvent.click(screen.getByRole('button', { name: '添加写入对象' }))
    await userEvent.selectOptions(screen.getByLabelText('写入对象1'), 'schema')
    await userEvent.click(screen.getByRole('checkbox', { name: '创建' }))
    expect((screen.getByRole('button', { name: '打开草稿试运行' }) as HTMLButtonElement).disabled).toBe(true)
    expect(EditorView.findFromDOM(screen.getByRole('textbox', { name: 'Python 源码' }))!.state.doc.toString()).toContain('main(inputs, ctx)')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('fn', expect.objectContaining({ function: expect.objectContaining({ sideEffects: 'dataWrite', dataWrite: [{ schemaId: 'schema', operations: ['create'] }] }) }), 'fn-etag'))
    await userEvent.selectOptions(screen.getByLabelText('副作用'), 'none')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenLastCalledWith('fn', expect.objectContaining({ function: expect.objectContaining({ sideEffects: 'none', dataWrite: undefined }) }), 'saved'))
  })
  it('已生效函数保持参数锁定并允许通过草稿更新Python源码', async () => {
    const { api, props } = fixture(fn)
    render(<ModelEditor {...props} />)
    expect((screen.getByLabelText('输入参数第1项名称') as HTMLInputElement).disabled).toBe(false)
    expect((screen.getByLabelText('输入参数第1项类型') as HTMLSelectElement).disabled).toBe(true)
    const source = editSource('def main(inputs):\n    return {"结果": inputs["原文"].upper()}')
    expect(source.getAttribute('contenteditable')).toBe('true')
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

  it('已生效动作拥有独立源码区，更新代码不会改动函数或解锁签名', async () => {
    const action: DefinitionRecord = { ...fn, definition: { ...fn.definition, id: 'action', kind: 'action', name: '独立原文动作', function: undefined, action: structuredClone(fn.definition.function) } }
    const { api, props } = fixture(action, [action, fn])
    render(<ModelEditor {...props} />)
    expect(screen.getByRole('region', { name: '动作源码' })).toBeTruthy()
    expect(screen.queryByRole('region', { name: '函数源码' })).toBeNull()
    expect((screen.getByLabelText('启用 Python 实现') as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByLabelText('输入参数第1项类型') as HTMLSelectElement).disabled).toBe(true)
    editSource('def main(inputs):\n    return {"结果": inputs["原文"].lower()}')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('action', expect.objectContaining({ action: expect.objectContaining({ runtime: expect.objectContaining({ source: expect.stringContaining('.lower()') }) }), function: undefined }), 'fn-etag'))
    expect(fn.definition.function!.runtime!.source).not.toContain('.lower()')
  })

  it('草稿动作可选择独立源码，并只保存本动作的实现', async () => {
    const action: DefinitionRecord = { status: 'draft', etag: 'action-etag', definition: { ...fn.definition, id: 'action', kind: 'action', function: undefined, action: { ...structuredClone(fn.definition.function!), runtime: undefined, functionId: 'fn' } } }
    const { api, props } = fixture(action, [action, fn])
    render(<ModelEditor {...props} />)
    await userEvent.click(screen.getByLabelText('启用 Python 实现'))
    expect((screen.getByLabelText('实现函数') as HTMLSelectElement).disabled).toBe(true)
    expect(screen.queryByRole('button', { name: '从实现函数复制参数声明' })).toBeNull()
    editSource('def main(inputs):\n    return {"结果": inputs["原文"].strip()}')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('action', expect.objectContaining({ action: expect.objectContaining({ functionId: undefined, runtime: expect.objectContaining({ source: expect.stringContaining('.strip()') }) }) }), 'action-etag'))
  })
})
