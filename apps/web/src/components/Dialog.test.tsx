import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { Dialog } from './Dialog'

describe('Dialog keyboard and focus', () => {
  it('keeps keyboard focus inside the dialog and restores the opener on close', () => {
    const opener = document.createElement('button')
    opener.textContent = '打开编辑器'; document.body.append(opener); opener.focus()
    const close = vi.fn()
    const view = render(<Dialog title="新建对象" onClose={close}><label>名称<input /></label><button type="button">保存</button></Dialog>)
    const input = screen.getByLabelText('名称')
    expect(document.activeElement).toBe(input)
    const last = screen.getByRole('button', { name: '保存' })
    last.focus(); fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).toBe(screen.getByRole('button', { name: '关闭对话框' }))
    fireEvent.keyDown(document.activeElement!, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
    fireEvent.keyDown(last, { key: 'Escape' }); expect(close).toHaveBeenCalledOnce()
    view.unmount(); expect(document.activeElement).toBe(opener)
    opener.remove()
  })
})
