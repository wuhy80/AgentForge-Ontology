import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiClient } from '../../lib/api'
import type { DefinitionRecord, Expression, PermissionRule, PermissionValueRef, PolicyRecord, Property, Role, User } from '../../lib/types'
import { compilePermissionFilter, expressionSummary, filterFromExpression } from './policy'
import { PolicyPanel } from './PolicyPanel'
import { RuleDialog } from './RuleDialog'

afterEach(cleanup)

const owner: Property = { id: 'owner', name: '负责人', dataType: 'uuid' }
const department: Property = { id: 'department', name: '部门', dataType: 'string' }
const amount: Property = { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6 }
const employee: DefinitionRecord = { status: 'active', etag: 'employee-current', definition: {
  id: 'employee', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '员工资料', schema: {
    mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'user-id', name: '对应用户', dataType: 'uuid' }, { id: 'login', name: '登录账号', dataType: 'string' }, { ...department, id: 'employee-dept' }, { ...amount, id: 'limit' }],
    indexes: [{ id: 'by-user', name: '用户唯一', unique: true, fieldIds: ['user-id'] }],
  },
} }
const target: DefinitionRecord = { status: 'active', etag: 'target-current', definition: { id: 'target', tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: '业务记录', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [owner, department, amount] } } }
const definitions = [target, employee]
const role: Role = { id: 'role', name: '业务用户', description: '', etag: 'role-current' }
const user: User = { id: 'reader', tenantId: 'tenant', username: 'reader', displayName: '业务同事', enabled: true, administrator: false, roleIds: [role.id], etag: 'user-current' }
const baseRule: PermissionRule = { id: 'rule', roleId: role.id, effect: 'allow', operation: 'instance.query', resourceId: target.definition.id, readFields: [owner.id], writeFields: [] }
const objectRef: PermissionValueRef = { source: 'object', schemaId: employee.definition.id, fieldId: 'employee-dept', matchFieldId: 'user-id', matchUserAttribute: 'id' }

describe('权限上下文条件编译', () => {
  it('保留用户引用、对象绑定、NOT与固定值，不将引用转为空字符串', () => {
    const expression: Expression = { op: 'and', args: [
      { op: 'eq', field: owner.id, valueRef: { source: 'user', attribute: 'id' } },
      { op: 'not', args: [{ op: 'or', args: [{ op: 'eq', field: department.id, valueRef: objectRef }, { op: 'eq', field: department.id, value: '外部' }] }] },
    ] }
    expect(compilePermissionFilter(filterFromExpression(expression), target.definition.schema!.properties, definitions)).toEqual(expression)
  })

  it.each([
    [{ source: 'user', attribute: 'untrusted' }, 'eq'],
    [{ source: 'user', attribute: 'id', schemaId: 'employee' }, 'eq'],
    [{ source: 'user', attribute: 'id' }, 'in'],
    [{ source: 'object', schemaId: 'employee', fieldId: 'employee-dept', instanceId: 'one', matchFieldId: 'user-id', matchUserAttribute: 'id' }, 'eq'],
    [{ source: 'object', schemaId: 'employee', fieldId: 'employee-dept', matchFieldId: 'user-id', matchUserAttribute: 'department' }, 'eq'],
    [{ source: 'object', schemaId: 'employee', fieldId: 'employee-dept', instanceId: 'one', mode: 'set' }, 'in'],
    [{ source: 'object', schemaId: 'employee', fieldId: 'employee-dept', instanceId: 'one', path: ['arbitrary'] }, 'eq'],
  ])('拒绝不支持的引用结构，保留原规则供取消编辑', (ref, op) => {
    expect(() => filterFromExpression({ op, field: department.id, valueRef: ref as PermissionValueRef })).toThrow()
  })

  it('拒绝同时声明固定值和动态引用，拒绝将未知来源作为字面值', () => {
    expect(() => filterFromExpression({ op: 'eq', field: owner.id, value: 'forged', valueRef: { source: 'user', attribute: 'id' } })).toThrow(/不能同时存在/)
    expect(() => filterFromExpression({ op: 'eq', field: owner.id, valueRef: { source: 'request' } as unknown as PermissionValueRef })).toThrow()
  })

  it('普通字符串中的变量文本保持字面值，不隐式解释成引用', () => {
    const expression: Expression = { op: 'eq', field: department.id, value: '${currentUser.department}' }
    expect(compilePermissionFilter(filterFromExpression(expression), target.definition.schema!.properties, definitions)).toEqual({ op: 'and', args: [expression] })
  })

  it('拒绝非唯一单值匹配、映射来源和类型或精度改变', () => {
    const compile = (field: string, valueRef: PermissionValueRef, models = definitions) => compilePermissionFilter(filterFromExpression({ op: 'eq', field, valueRef }), target.definition.schema!.properties, models)
    expect(() => compile(department.id, { ...objectRef, matchFieldId: 'login', matchUserAttribute: 'username' })).toThrow(/唯一索引/)
    expect(() => compile(amount.id, objectRef)).toThrow(/类型不匹配/)
    const changed = structuredClone(employee)
    changed.definition.schema!.properties.find((item) => item.id === 'limit')!.scale = 2
    expect(() => compile(amount.id, { ...objectRef, fieldId: 'limit' }, [target, changed])).toThrow(/类型不匹配/)
    const mapped = structuredClone(employee)
    mapped.definition.schema!.storageBinding = { dataSourceId: 'source', table: 'staff', idColumn: 'id' }
    expect(() => compile(department.id, objectRef, [target, mapped])).toThrow(/不可用/)
  })

  it('集合匹配不要求唯一键，引用摘要只显示模型与属性名称', () => {
    const expression: Expression = { op: 'in', field: department.id, valueRef: { ...objectRef, mode: 'set', matchFieldId: 'login', matchUserAttribute: 'username' } }
    expect(compilePermissionFilter(filterFromExpression(expression), target.definition.schema!.properties, definitions)).toEqual({ op: 'and', args: [expression] })
    const summary = expressionSummary(expression, target.definition.schema!.properties, definitions)
    expect(summary).toBe('部门 属于 员工资料 · 部门（登录账号匹配当前用户账号）集合')
    expect(summary).not.toContain('employee-dept')
    expect(expressionSummary(expression, target.definition.schema!.properties)).toContain('不可见模型 · 不可见属性')
  })
})

describe('权限上下文编辑', () => {
  function editor(rule = baseRule) {
    const onSave = vi.fn()
    render(<RuleDialog rule={rule} roles={[role]} definitions={definitions} categories={[]} onClose={vi.fn()} onSave={onSave} />)
    return onSave
  }

  it('保存当前用户条件，并在集合与空值操作间切换时清除过期引用', () => {
    const onSave = editor()
    fireEvent.click(screen.getByRole('button', { name: '添加行条件' }))
    fireEvent.change(screen.getByLabelText('行条件取值来源'), { target: { value: 'user' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).toHaveBeenLastCalledWith(expect.objectContaining({ rowFilter: { op: 'and', args: [{ op: 'eq', field: owner.id, valueRef: { source: 'user', attribute: 'id' } }] } }))
    fireEvent.change(screen.getByLabelText('行条件运算符'), { target: { value: 'in' } })
    expect((screen.getByLabelText('当前用户信息') as HTMLSelectElement).value).toBe('roleIds')
    fireEvent.change(screen.getByLabelText('当前用户信息'), { target: { value: 'userGroupIds' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.lastCall![0].rowFilter.args[0].valueRef.attribute).toBe('userGroupIds')
    fireEvent.change(screen.getByLabelText('行条件运算符'), { target: { value: 'isNull' } })
    expect(screen.queryByLabelText('当前用户信息')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.lastCall![0].rowFilter.args[0]).toEqual({ op: 'isNull', field: owner.id })
  })

  it('按当前用户匹配资料取值，并保留已有原行和新行引用', () => {
    const onSave = editor({ ...baseRule, operation: 'instance.patch', rowFilter: { op: 'eq', field: owner.id, valueRef: { source: 'user', attribute: 'id' } }, newRowFilter: { op: 'eq', field: department.id, valueRef: objectRef } })
    expect((screen.getByLabelText('取值对象模型') as HTMLSelectElement).value).toBe(employee.definition.id)
    expect((screen.getByLabelText('用户匹配属性') as HTMLSelectElement).value).toBe('user-id')
    expect(within(screen.getByLabelText('用户匹配属性')).queryByRole('option', { name: '登录账号' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
      rowFilter: { op: 'and', args: [{ op: 'eq', field: owner.id, valueRef: { source: 'user', attribute: 'id' } }] },
      newRowFilter: { op: 'and', args: [{ op: 'eq', field: department.id, valueRef: objectRef }] },
    }))
  })

  it('配置固定实例与集合取值，不把完整实例 ID 混入集合绑定', () => {
    const onSave = editor()
    fireEvent.click(screen.getByRole('button', { name: '添加行条件' }))
    fireEvent.change(screen.getByLabelText('行条件字段'), { target: { value: department.id } })
    fireEvent.change(screen.getByLabelText('行条件取值来源'), { target: { value: 'object' } })
    fireEvent.change(screen.getByLabelText('取值对象模型'), { target: { value: employee.definition.id } })
    fireEvent.change(screen.getByLabelText('取值对象属性'), { target: { value: 'employee-dept' } })
    fireEvent.change(screen.getByLabelText('取值实例 ID'), { target: { value: '11111111-1111-4111-8111-111111111111' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.lastCall![0].rowFilter.args[0].valueRef).toEqual({ source: 'object', schemaId: employee.definition.id, fieldId: 'employee-dept', instanceId: '11111111-1111-4111-8111-111111111111' })
    fireEvent.change(screen.getByLabelText('行条件运算符'), { target: { value: 'in' } })
    expect(screen.queryByLabelText('取值实例 ID')).toBeNull()
    fireEvent.change(screen.getByLabelText('对象取值方式'), { target: { value: 'username' } })
    fireEvent.change(screen.getByLabelText('用户匹配属性'), { target: { value: 'login' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave.mock.lastCall![0].rowFilter.args[0].valueRef).toEqual({ source: 'object', schemaId: employee.definition.id, fieldId: 'employee-dept', mode: 'set', matchUserAttribute: 'username', matchFieldId: 'login' })
  })

  it('未知引用禁止保存，只有显式清空才能重配', () => {
    const onSave = editor({ ...baseRule, rowFilter: { op: 'eq', field: owner.id, valueRef: { source: 'request' } as unknown as PermissionValueRef } })
    expect(screen.getByText(/上下文引用不受支持/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '清空行条件并重新配置' }))
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).toHaveBeenCalledWith(expect.not.objectContaining({ rowFilter: expect.anything() }))
  })

  it('字段类型变化保留对象来源并要求重新选属性，不静默删掉行限制', () => {
    const onSave = editor({ ...baseRule, rowFilter: { op: 'eq', field: department.id, valueRef: objectRef } })
    fireEvent.change(screen.getByLabelText('行条件字段'), { target: { value: amount.id } })
    expect((screen.getByLabelText('行条件取值来源') as HTMLSelectElement).value).toBe('object')
    expect((screen.getByLabelText('取值对象属性') as HTMLSelectElement).value).toBe('')
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toContain('对象上下文引用不受支持')
  })

  it('目标字段全部被移除后禁止保存已有条件，避免变成无条件允许', () => {
    const onSave = vi.fn(), emptied = structuredClone(target)
    emptied.definition.schema!.properties = []
    render(<RuleDialog rule={{ ...baseRule, rowFilter: { op: 'eq', field: department.id, valueRef: objectRef } }} roles={[role]} definitions={[emptied, employee]} categories={[]} onClose={vi.fn()} onSave={onSave} />)
    fireEvent.click(screen.getByRole('button', { name: '保存规则到编辑区' }))
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByRole('alert').textContent).toContain('行条件字段或操作符已失效')
  })
})

describe('动态模拟结果保护', () => {
  it('受保护结果不渲染解析后的行条件原值，策略变化后清理旧结果', async () => {
    const simulatePolicy = vi.fn().mockResolvedValue({ decision: { allow: true, state: 'current', readFields: [owner.id], writeFields: [], rowFilter: { op: 'eq', field: owner.id, value: 'PRIVATE_CONTEXT_VALUE' }, newRowFilter: { op: 'eq', field: owner.id, value: 'PRIVATE_NEW_CONTEXT_VALUE' } }, reason: '已匹配授权', contextProtected: true })
    const record: PolicyRecord = { current: { rules: [baseRule] }, etag: 'one', state: 'one' }
    const props = { api: { simulatePolicy } as unknown as ApiClient, record, roles: [role], users: [user], definitions, categories: [], onRecord: vi.fn(), onReload: vi.fn(), onForbidden: vi.fn() }
    const view = render(<PolicyPanel {...props} />)
    fireEvent.change(screen.getByLabelText('模拟用户'), { target: { value: user.id } })
    fireEvent.change(screen.getByLabelText('模拟资源'), { target: { value: target.definition.id } })
    fireEvent.click(screen.getByRole('button', { name: '模拟授权' }))
    await screen.findByText('允许此操作')
    expect(screen.getAllByText('含动态上下文，实际取值不回显')).toHaveLength(2)
    expect(screen.queryByText(/PRIVATE_/)).toBeNull()
    view.rerender(<PolicyPanel {...props} record={{ ...record, etag: 'two', state: 'two' }} />)
    await waitFor(() => expect(screen.queryByText('允许此操作')).toBeNull())
  })
})
