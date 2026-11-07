import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, PermissionRule, PolicyRecord, Role, Session, User } from '../../lib/types'
import { AdminPage } from './AdminPage'
import { UserDialog } from './IdentityDialogs'
import { PolicyPanel } from './PolicyPanel'
import { RuleDialog } from './RuleDialog'

const role: Role = { id: 'role-a', name: '采购员', description: '', etag: 'role-etag' }
const user: User = { id: 'user-a', tenantId: 'tenant', username: 'buyer', displayName: '采购同事', enabled: true, administrator: false, roleIds: [role.id], etag: 'user-etag' }
const session: Session = { user: { ...user, id: 'admin', username: 'admin', displayName: '管理员', administrator: true }, expiresAt: 1800000000, csrfToken: 'csrf' }
const schema: DefinitionRecord = { etag: 'schema-etag', status: 'active', definition: { id: 'schema-a', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '采购订单', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6 }, { id: 'dept', name: '部门', dataType: 'string' }] } } }
const rule: PermissionRule = { id: 'rule-a', roleId: role.id, effect: 'allow', operation: 'instance.query', resourceId: schema.definition.id, readFields: ['amount'], writeFields: [] }
const policy: PolicyRecord = { current: { rules: [] }, etag: 'policy-one', state: 'state-one' }
const noop = () => {}

describe('identity administration', () => {
  it('preserves user edits on a concurrency conflict and blocks reusing the stale ETag', async () => {
    const updateUser = vi.fn().mockRejectedValue(new ApiError(412, 'precondition_failed', '记录已改变'))
    render(<UserDialog api={{ updateUser } as unknown as ApiClient} user={user} roles={[role]} onClose={noop} onSaved={vi.fn()} onForbidden={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('显示名称'), { target: { value: '新的显示名称' } })
    fireEvent.click(screen.getByLabelText('启用账户'))
    fireEvent.click(screen.getByRole('button', { name: '保存用户' }))
    await screen.findByText(/记录已改变/)
    expect((screen.getByLabelText('显示名称') as HTMLInputElement).value).toBe('新的显示名称')
    expect((screen.getByLabelText('启用账户') as HTMLInputElement).checked).toBe(false)
    expect(updateUser).toHaveBeenCalledWith(user.id, { username: 'buyer', displayName: '新的显示名称', enabled: false, administrator: false, roleIds: ['role-a'] }, 'user-etag')
    expect((screen.getByRole('button', { name: '保存用户' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('显示名称'), { target: { value: '继续修改草稿' } })
    expect(screen.getByText(/记录已改变/)).toBeTruthy()
    expect((screen.getByLabelText('显示名称') as HTMLInputElement).value).toBe('继续修改草稿')
    expect((screen.getByRole('button', { name: '保存用户' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('clears loaded users and roles when a management mutation is forbidden', async () => {
    const api = { listUsers: vi.fn().mockResolvedValue([user]), listRoles: vi.fn().mockResolvedValue([role]), getPolicy: vi.fn().mockResolvedValue(policy), listAudit: vi.fn().mockResolvedValue([]), updateUser: vi.fn().mockRejectedValue(new ApiError(403, 'forbidden', '当前不可管理用户')) } as unknown as ApiClient
    render(<AdminPage api={api} session={session} categories={[]} definitions={[schema]} />)
    await screen.findByText('采购同事')
    fireEvent.click(screen.getByRole('button', { name: '编辑用户' }))
    fireEvent.change(screen.getByLabelText('显示名称'), { target: { value: '新的显示名称' } })
    fireEvent.click(screen.getByRole('button', { name: '保存用户' }))
    await screen.findByText('当前不可管理用户')
    expect(screen.queryByText('采购同事')).toBeNull()
    expect(screen.queryByText('采购员')).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByText('当前身份无权读取管理数据')).toBeTruthy()
  })

  it('does not request management records for a nonadministrator', () => {
    const listUsers = vi.fn()
    render(<AdminPage api={{ listUsers } as unknown as ApiClient} session={{ ...session, user }} categories={[]} definitions={[]} />)
    expect(listUsers).not.toHaveBeenCalled()
    expect(screen.getByText('此页面仅向租户管理员开放')).toBeTruthy()
  })
})

describe('permission policy editor', () => {
  it('opens an editable rule dialog from the policy tab in the full management page', async () => {
    const api = { listUsers: vi.fn().mockResolvedValue([user]), listRoles: vi.fn().mockResolvedValue([role]), getPolicy: vi.fn().mockResolvedValue(policy), listAudit: vi.fn().mockResolvedValue([]) } as unknown as ApiClient
    render(<AdminPage api={api} session={session} categories={[]} definitions={[schema]} />)
    await screen.findByText('采购同事')
    fireEvent.click(screen.getByRole('tab', { name: '权限策略' }))
    fireEvent.click(screen.getByRole('button', { name: '添加规则' }))
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByLabelText('授权角色')).toBeTruthy()
    fireEvent.change(dialog.getByLabelText('资源范围'), { target: { value: schema.definition.id } })
    expect(dialog.getByRole('group', { name: '可读字段' })).toBeTruthy()
  })

  it('emits stable role/schema/field identifiers, keeps decimal precision and strips fields on deny', () => {
    const onSave = vi.fn()
    render(<RuleDialog roles={[role]} definitions={[schema]} categories={[]} onClose={noop} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('资源范围'), { target: { value: schema.definition.id } })
    fireEvent.click(within(screen.getByRole('group', { name: '可读字段' })).getByLabelText('金额'))
    fireEvent.click(screen.getAllByRole('button', { name: '添加行条件' })[0])
    fireEvent.change(screen.getByLabelText('行条件比较值'), { target: { value: '9007199254740993.000001' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.calls[0][0]).toMatchObject({ roleId: role.id, resourceId: schema.definition.id, operation: 'instance.query', effect: 'allow', readFields: ['amount'], rowFilter: { op: 'and', args: [{ op: 'eq', field: 'amount', value: '9007199254740993.000001' }] } })
    fireEvent.change(screen.getByLabelText('规则效果'), { target: { value: 'deny' } })
    expect(screen.queryByRole('group', { name: '可读字段' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.calls[1][0]).toMatchObject({ effect: 'deny', readFields: [], writeFields: [] })
    expect(onSave.mock.calls[1][0].rowFilter).toBeUndefined()
  })

  it('does not silently split an existing set member containing a newline into separate values', () => {
    const onSave = vi.fn()
    render(<RuleDialog rule={{ ...rule, rowFilter: { op: 'in', field: 'dept', value: ['采购\n一部'] } }} roles={[role]} definitions={[schema]} categories={[]} onClose={noop} onSave={onSave} />)
    expect(screen.getByText(/集合中包含换行文本/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '清空行条件并重新配置' }))
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.calls[0][0].rowFilter).toBeUndefined()
  })

  it('requires changed content to save, then publishes only the saved draft using its new ETag', async () => {
    const saved: PolicyRecord = { ...policy, draft: { rules: [] }, current: { rules: [rule] }, etag: 'policy-two' }
    const savePolicy = vi.fn().mockResolvedValue(saved), publishPolicy = vi.fn().mockResolvedValue({ ...policy, etag: 'policy-three' }), onRecord = vi.fn()
    const api = { savePolicy, publishPolicy } as unknown as ApiClient
    const props = { api, roles: [role], users: [user], categories: [], definitions: [schema], onRecord, onReload: vi.fn(), onForbidden: vi.fn() }
    const { rerender } = render(<PolicyPanel key="first" {...props} record={{ ...policy, current: { rules: [rule] } }} />)
    expect((screen.getByRole('button', { name: '保存策略草稿' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '使策略生效' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '移除规则：查询实例' }))
    fireEvent.click(screen.getByRole('button', { name: '保存策略草稿' }))
    await waitFor(() => expect(onRecord).toHaveBeenCalledWith(saved))
    expect(savePolicy).toHaveBeenCalledWith({ rules: [] }, 'policy-one')
    rerender(<PolicyPanel key="saved" {...props} record={saved} />)
    fireEvent.click(screen.getByRole('button', { name: '使策略生效' }))
    await waitFor(() => expect(publishPolicy).toHaveBeenCalledWith('policy-two'))
  })

  it('retains edited rules after a 412 and blocks saving and publication until reloaded', async () => {
    const savePolicy = vi.fn().mockRejectedValue(new ApiError(412, 'precondition_failed', '策略已改变'))
    render(<PolicyPanel api={{ savePolicy } as unknown as ApiClient} record={{ ...policy, current: { rules: [rule] } }} roles={[role]} users={[user]} definitions={[schema]} categories={[]} onRecord={noop} onReload={noop} onForbidden={noop} />)
    fireEvent.click(screen.getByRole('button', { name: '移除规则：查询实例' }))
    fireEvent.click(screen.getByRole('button', { name: '保存策略草稿' }))
    await screen.findByText('策略已改变')
    expect(screen.getByText('从一条角色规则开始')).toBeTruthy()
    expect((screen.getByRole('button', { name: '保存策略草稿' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '使策略生效' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: '重新读取策略（放弃本地修改）' })).toBeTruthy()
  })

  it('ignores an earlier simulation after its selected user or operation changes', async () => {
    let finish!: (result: unknown) => void
    const simulatePolicy = vi.fn().mockReturnValue(new Promise((resolve) => { finish = resolve }))
    render(<PolicyPanel api={{ simulatePolicy } as unknown as ApiClient} record={policy} roles={[role]} users={[user]} definitions={[schema]} categories={[]} onRecord={noop} onReload={noop} onForbidden={noop} />)
    fireEvent.change(screen.getByLabelText('模拟用户'), { target: { value: user.id } })
    fireEvent.click(screen.getByRole('button', { name: '模拟授权' }))
    const signal = simulatePolicy.mock.calls[0][1] as AbortSignal
    fireEvent.change(screen.getByLabelText('模拟操作'), { target: { value: 'instance.create' } })
    expect(signal.aborted).toBe(true)
    await act(async () => finish({ decision: { allow: true, readFields: ['*'], writeFields: [], state: 'state' }, reason: '不应出现的旧结果' }))
    expect(screen.queryByText('不应出现的旧结果')).toBeNull()
  })

  it('clears a previous authorization simulation after a user role or account state changes', async () => {
    const simulatePolicy = vi.fn().mockResolvedValue({ decision: { allow: true, readFields: ['*'], writeFields: [], state: 'state-one' }, reason: '匹配已分配角色' })
    const api = { simulatePolicy } as unknown as ApiClient
    const props = { api, record: policy, roles: [role], definitions: [schema], categories: [], onRecord: noop, onReload: noop, onForbidden: noop }
    const { rerender } = render(<PolicyPanel {...props} users={[user]} />)
    fireEvent.change(screen.getByLabelText('模拟用户'), { target: { value: user.id } })
    fireEvent.click(screen.getByRole('button', { name: '模拟授权' }))
    await screen.findByText('匹配已分配角色')
    rerender(<PolicyPanel {...props} users={[{ ...user, roleIds: [], enabled: false, etag: 'revoked' }]} />)
    expect(screen.queryByText('匹配已分配角色')).toBeNull()
    expect(screen.queryByText('允许此操作')).toBeNull()
  })
})
