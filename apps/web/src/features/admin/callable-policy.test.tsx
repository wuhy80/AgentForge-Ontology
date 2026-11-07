import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import type { DefinitionRecord } from '../../lib/types'
import { RuleDialog } from './RuleDialog'

afterEach(cleanup)

const definitions: DefinitionRecord[] = [
  { status: 'active', etag: 'schema', definition: { id: 'schema', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '笔记', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } },
  { status: 'active', etag: 'fn', definition: { id: 'fn', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'function', name: '摘要计算', function: { input: [], output: [], sideEffects: 'none' } } },
  { status: 'active', etag: 'action', definition: { id: 'action', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'action', name: '生成摘要', action: { input: [], output: [], sideEffects: 'none', functionId: 'fn' } } },
]

describe('调用权限表单', () => {
  it('调用仅选择对应类型，保存整个资源授权，不携带实例字段或行条件', () => {
    const onSave = vi.fn()
    render(<RuleDialog roles={[{ id: 'role', name: '计算用户', description: '', etag: 'role' }]} definitions={definitions} categories={[{ id: 'category', tenantId: 'tenant', name: '知识库' }]} onClose={vi.fn()} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('业务操作'), { target: { value: 'function.invoke' } })
    let resources = screen.getByLabelText('资源范围')
    expect(within(resources).getByRole('option', { name: '知识库 / 摘要计算' })).toBeTruthy()
    expect(within(resources).queryByRole('option', { name: '知识库 / 生成摘要' })).toBeNull()
    expect(within(resources).queryByRole('option', { name: '知识库 / 笔记' })).toBeNull()
    expect(screen.queryByText('可读字段')).toBeNull()
    fireEvent.change(resources, { target: { value: 'fn' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'function.invoke', resourceId: 'fn', readFields: [], writeFields: [] }))
    expect(onSave.mock.calls[0][0]).not.toHaveProperty('rowFilter')
    fireEvent.change(screen.getByLabelText('业务操作'), { target: { value: 'action.invoke' } })
    resources = screen.getByLabelText('资源范围')
    expect(within(resources).getByRole('option', { name: '知识库 / 生成摘要' })).toBeTruthy()
    expect(within(resources).queryByRole('option', { name: '知识库 / 摘要计算' })).toBeNull()
    expect(screen.getByText(/还需拥有其绑定函数的运行权限/)).toBeTruthy()
  })
})
