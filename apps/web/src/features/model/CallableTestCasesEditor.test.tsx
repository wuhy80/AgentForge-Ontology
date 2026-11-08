import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Callable } from '../../lib/types'
import { CallableTestCasesEditor } from './CallableTestCasesEditor'

afterEach(cleanup)
const callable: Callable = { sideEffects: 'none', input: [
  { id: 'integer', name: '整数', dataType: 'int64', required: true },
  { id: 'decimal', name: '小数', dataType: 'decimal', precision: 20, scale: 4, required: true },
  { id: 'nullable', name: '备注', dataType: 'string', nullable: true },
] }

describe('测试样例随草稿保存', () => {
  it('保存部分输入且不丢失整数和小数精度，未填与NULL区别保留', async () => {
    const onChange = vi.fn()
    render(<CallableTestCasesEditor callable={callable} disabled={false} onChange={onChange} />)
    await userEvent.click(screen.getByText('添加测试样例'))
    await userEvent.type(screen.getByLabelText('样例名称'), '完整精度')
    await userEvent.selectOptions(screen.getByLabelText('整数的传入方式'), 'value')
    await userEvent.type(screen.getByLabelText('整数int64'), '9007199254740993')
    await userEvent.selectOptions(screen.getByLabelText('小数的传入方式'), 'value')
    await userEvent.type(screen.getByLabelText('小数decimal'), '1234567890.1234')
    await userEvent.selectOptions(screen.getByLabelText('备注的传入方式'), 'null')
    await userEvent.click(screen.getByRole('button', { name: '加入草稿样例' }))
    expect(onChange).toHaveBeenCalledWith([{ id: expect.any(String), name: '完整精度', input: { integer: '9007199254740993', decimal: '1234567890.1234', nullable: null } }])
    expect(localStorage.length).toBe(0)
  })

  it('已保存样例按稳定ID编辑，改参数中文名仍能读回，删除只更改草稿数据', async () => {
    const onChange = vi.fn()
    const testCases = [{ id: 'case', name: '已有样例', input: { integer: '9007199254740993' } }]
    render(<CallableTestCasesEditor callable={{ ...callable, input: [{ ...callable.input![0], name: '新名称' }], testCases }} disabled={false} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: '编辑样例 已有样例' }))
    expect((screen.getByLabelText('新名称int64') as HTMLInputElement).value).toBe('9007199254740993')
    fireEvent.change(screen.getByLabelText('新名称int64'), { target: { value: '8' } })
    await userEvent.click(screen.getByRole('button', { name: '更新样例到草稿' }))
    expect(onChange).toHaveBeenLastCalledWith([{ id: 'case', name: '已有样例', input: { integer: '8' } }])
    await userEvent.click(screen.getByRole('button', { name: '删除样例 已有样例' }))
    expect(onChange).toHaveBeenLastCalledWith([])
  })

  it('新参数尚无稳定ID时禁用样例保存，避免多参数绑定到空键', () => {
    render(<CallableTestCasesEditor callable={{ ...callable, input: [{ ...callable.input![0], id: '' }] }} disabled={false} onChange={vi.fn()} />)
    expect(screen.getByText(/请先保存参数草稿/)).toBeTruthy()
    expect((screen.getByRole('button', { name: '加入草稿样例', hidden: true }) as HTMLButtonElement).closest('fieldset')?.disabled).toBe(true)
  })
})
