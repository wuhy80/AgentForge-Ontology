import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Property } from '../../lib/types'
import { ValueInput } from './InstanceDialog'
import { GroupEditor } from './QueryFilterEditor'
import { buildWriteValues, compileFilter, newGroup, propertyValueText } from './query'

afterEach(cleanup)
const property: Property = { id: 'state', name: '状态', dataType: 'string', enumId: 'enum', enumEtag: 'current', enumValues: [{ code: 'READY', label: '就绪' }, { code: 'OLD', label: '历史状态', deprecated: true }] }

describe('枚举业务值', () => {
  it('新写选项禁用停用值，查询可选择停用值，未加载选项时禁止自由输入', async () => {
    const onChange = vi.fn()
    const view = render(<ValueInput property={property} id="value" value="" onChange={onChange} />)
    expect(screen.getByRole('option', { name: '历史状态（OLD） · 已停用' }).matches(':disabled')).toBe(true)
    await userEvent.selectOptions(screen.getByRole('combobox'), 'READY')
    expect(onChange).toHaveBeenCalledWith('READY')
    view.rerender(<ValueInput property={property} allowDeprecated id="value" value="OLD" onChange={onChange} />)
    expect(screen.getByRole('option', { name: '历史状态（OLD） · 已停用' }).matches(':disabled')).toBe(false)
    view.rerender(<ValueInput property={{ ...property, enumValues: undefined }} id="value" value="" onChange={onChange} />)
    expect(screen.getByRole('combobox').matches(':disabled')).toBe(true)
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('按稳定code写入并显示中文，保留原停用值不产生补丁，禁止新写未知或停用值', () => {
    expect(buildWriteValues([property], { state: { mode: 'value', text: 'READY' } })).toEqual({ state: 'READY' })
    expect(() => buildWriteValues([property], { state: { mode: 'value', text: 'OLD' } })).toThrow('有效的枚举值')
    expect(() => buildWriteValues([property], { state: { mode: 'value', text: 'UNKNOWN' } })).toThrow('有效的枚举值')
    expect(buildWriteValues([property], { state: { mode: 'value', text: 'OLD' } }, { state: 'OLD' })).toEqual({})
    expect(propertyValueText(property, 'OLD')).toBe('历史状态（OLD） · 已停用')
  })

  it('中文枚举集合选择仍编译为code，并允许查询旧值', async () => {
    const group = newGroup()
    group.children = [{ id: 'condition', kind: 'condition', field: 'state', op: 'in', raw: 'OLD' }]
    const onChange = vi.fn()
    render(<GroupEditor group={group} properties={[property]} onChange={onChange} />)
    const list = screen.getByRole('listbox', { name: '枚举集合值' }) as HTMLSelectElement
    Array.from(list.options).forEach((option) => { option.selected = true })
    fireEvent.change(list)
    const changed = onChange.mock.calls.at(-1)![0]
    expect(compileFilter(changed, [property])).toEqual({ op: 'and', args: [{ op: 'in', field: 'state', value: ['READY', 'OLD'] }] })
  })
})
