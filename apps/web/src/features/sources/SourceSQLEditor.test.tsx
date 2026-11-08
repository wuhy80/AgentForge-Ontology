import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { EditorView } from '@codemirror/view'
import { undo } from '@codemirror/commands'
import { SourceSQLEditor } from './SourceSQLEditor'

function editor() {
  const view = EditorView.findFromDOM(screen.getByRole('textbox', { name: 'SQL 语句' }))
  if (!view) throw new Error('SQL 编辑器未创建')
  return view
}

describe('数据源 SQL 编辑器', () => {
  it('提供真实高亮、行号和只读查询提示，受控输入重渲染保留实例与选区', () => {
    const changed = vi.fn()
    function Controlled({ title }: { title: string }) {
      const [value, setValue] = useState('SELECT amount\nFROM orders LIMIT 10;')
      return <><span>{title}</span><SourceSQLEditor value={value} onChange={(text) => { setValue(text); changed(text) }} onRun={vi.fn()} /></>
    }
    const rendered = render(<Controlled title="初始" />), view = editor()
    expect(view.dom.querySelector('.cm-lineNumbers')).toBeTruthy()
    expect(view.contentDOM.querySelector('.cm-line span')).toBeTruthy()
    expect(view.contentDOM.getAttribute('aria-multiline')).toBe('true')
    act(() => view.dispatch({ changes: { from: 7, to: 13, insert: 'memo' }, selection: { anchor: 4 } }))
    rendered.rerender(<Controlled title="重渲染" />)
    expect(editor()).toBe(view)
    expect(view.state.selection.main.anchor).toBe(4)
    expect(view.state.doc.toString()).toContain('SELECT memo')
    expect(changed).toHaveBeenCalledOnce()
  })

  it('ModEnter 使用最新回调，IME期间不运行且延迟外部替换，结束仅同步最新文本', async () => {
    const first = vi.fn(), second = vi.fn(), changed = vi.fn()
    const rendered = render(<SourceSQLEditor value="SELECT 1" onChange={changed} onRun={first} />)
    const view = editor()
    rendered.rerender(<SourceSQLEditor value="SELECT 1" onChange={changed} onRun={second} />)
    fireEvent.keyDown(view.contentDOM, { key: 'Enter', code: 'Enter', ctrlKey: true })
    expect(second).toHaveBeenCalledOnce(); expect(first).not.toHaveBeenCalled()
    fireEvent.compositionStart(view.contentDOM)
    rendered.rerender(<SourceSQLEditor value="SELECT old" onChange={changed} onRun={second} />)
    rendered.rerender(<SourceSQLEditor value="SELECT latest" onChange={changed} onRun={second} />)
    fireEvent.keyDown(view.contentDOM, { key: 'Enter', code: 'Enter', ctrlKey: true })
    expect(second).toHaveBeenCalledOnce()
    expect(view.state.doc.toString()).toBe('SELECT 1')
    fireEvent.compositionEnd(view.contentDOM)
    await waitFor(() => expect(view.state.doc.toString()).toBe('SELECT latest'))
    expect(changed).not.toHaveBeenCalled()
  })

  it('正常中文组词保留用户输入，不回滚到旧props', async () => {
    const changed = vi.fn()
    render(<SourceSQLEditor value="SELECT " onChange={changed} onRun={vi.fn()} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM)
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: '订单金额' }, userEvent: 'input.type.compose' }))
    fireEvent.compositionEnd(view.contentDOM)
    await act(async () => { await Promise.resolve() })
    expect(view.state.doc.toString()).toBe('SELECT 订单金额')
    expect(changed).toHaveBeenCalledExactlyOnceWith('SELECT 订单金额')
  })

  it('Tab 使用两空格缩进且可撤销', () => {
    render(<SourceSQLEditor value="SELECT 订单金额" onChange={vi.fn()} onRun={vi.fn()} />)
    const view = editor()
    act(() => { view.focus(); view.dispatch({ selection: { anchor: 0 } }) })
    fireEvent.keyDown(view.contentDOM, { key: 'Tab', code: 'Tab' })
    expect(view.state.doc.toString()).toBe('  SELECT 订单金额')
    act(() => { undo(view) })
    expect(view.state.doc.toString()).toBe('SELECT 订单金额')
  })

  it('disabled及父fieldset只读禁用快捷运行，组词时禁用应用最新外部SQL', async () => {
    const run = vi.fn(), changed = vi.fn()
    const rendered = render(<fieldset><SourceSQLEditor value="SELECT original" onChange={changed} onRun={run} /></fieldset>)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM)
    rendered.rerender(<fieldset disabled><SourceSQLEditor value="SELECT updated" onChange={changed} onRun={run} /></fieldset>)
    await waitFor(() => expect(view.state.readOnly).toBe(true))
    expect(view.state.doc.toString()).toBe('SELECT updated')
    expect(view.contentDOM.getAttribute('aria-disabled')).toBe('true')
    fireEvent.keyDown(view.contentDOM, { key: 'Enter', code: 'Enter', ctrlKey: true })
    expect(run).not.toHaveBeenCalled(); expect(changed).not.toHaveBeenCalled()
    rendered.rerender(<fieldset><SourceSQLEditor value="SELECT final" disabled onChange={changed} onRun={run} /></fieldset>)
    expect(view.state.readOnly).toBe(true)
    expect(view.state.doc.toString()).toBe('SELECT final')
    rendered.rerender(<fieldset><SourceSQLEditor value="SELECT final" onChange={changed} onRun={run} /></fieldset>)
    await waitFor(() => expect(view.state.readOnly).toBe(false))
  })
})
