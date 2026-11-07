import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { Property } from '../../lib/types'
import { PropertyEditor } from '../model/PropertyEditor'
import { ValueInput } from './InstanceDialog'

const body: Property = { id: 'body', name: '正文', dataType: 'longtext' }

describe('超长文本表单', () => {
  it('提供独立类型并说明字段限制', () => {
    render(<PropertyEditor title="对象属性" properties={[body]} onChange={vi.fn()} locked={false} enums={[]} />)
    expect((screen.getByRole('combobox', { name: '对象属性第1项类型' }) as HTMLSelectElement).value).toBe('longtext')
    expect(screen.getByRole('option', { name: '超长文本（4 MiB）' })).toBeTruthy()
    expect(screen.getByText('最多 4 MiB（UTF-8 字节），不支持索引或排序')).toBeTruthy()
  })

  it('使用多行输入保留超过 text 容量的正文', () => {
    const original = '知识库正文\n'.repeat(10000)
    const onChange = vi.fn()
    render(<ValueInput property={body} value={original} onChange={onChange} id="body" />)
    const input = screen.getByRole('textbox')
    expect(input.tagName).toBe('TEXTAREA')
    expect((input as HTMLTextAreaElement).value).toBe(original)
    fireEvent.change(input, { target: { value: `${original}新段落\n` } })
    expect(onChange).toHaveBeenCalledWith(`${original}新段落\n`)
  })
})
