import { useState } from 'react'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CompletionContext, acceptCompletion, currentCompletions } from '@codemirror/autocomplete'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { python } from '@codemirror/lang-python'
import { undo } from '@codemirror/commands'
import { PythonSourceEditor, pythonParameterCompletions } from './PythonSourceEditor'

const rect = { x: 0, y: 0, width: 100, height: 20, top: 0, right: 100, bottom: 20, left: 0, toJSON: () => ({}) }
const originalRects = Object.getOwnPropertyDescriptor(Range.prototype, 'getClientRects')
const originalRect = Object.getOwnPropertyDescriptor(Range.prototype, 'getBoundingClientRect')

beforeAll(() => {
  Object.defineProperty(Range.prototype, 'getClientRects', { configurable: true, value: () => [rect] })
  Object.defineProperty(Range.prototype, 'getBoundingClientRect', { configurable: true, value: () => rect })
})
afterAll(() => {
  if (originalRects) Object.defineProperty(Range.prototype, 'getClientRects', originalRects)
  else Reflect.deleteProperty(Range.prototype, 'getClientRects')
  if (originalRect) Object.defineProperty(Range.prototype, 'getBoundingClientRect', originalRect)
  else Reflect.deleteProperty(Range.prototype, 'getBoundingClientRect')
})

function editor(label = 'Python 源码') {
  const content = screen.getByRole('textbox', { name: label })
  const view = EditorView.findFromDOM(content)
  if (!view) throw new Error('源码编辑器未创建')
  return view
}

function complete(text: string, inputs = ['正文', '含"引号'], outputs = ['字符数']) {
  const state = EditorState.create({ doc: text, extensions: [python()] })
  return pythonParameterCompletions(new CompletionContext(state, text.length, true), inputs, outputs)
}

describe('Python 源码编辑器', () => {
  it('提供可访问文本框、行号和真实 Python 语法高亮', () => {
    render(<PythonSourceEditor value={'def main(inputs):\n    return {"字符数": str(len(inputs["正文"]))}'} onChange={vi.fn()} />)
    const view = editor()
    expect(view.contentDOM.getAttribute('aria-multiline')).toBe('true')
    expect(view.dom.querySelector('.cm-lineNumbers')).toBeTruthy()
    expect(view.contentDOM.querySelector('.cm-line span')).toBeTruthy()
    expect(view.contentDOM.textContent).toContain('def main(inputs):')
    expect(screen.getByText(/Ctrl\+Space 补全/)).toBeTruthy()
  })

  it('受控输入持续回调，普通重渲染保留编辑器、选区和刚输入的内容', () => {
    const changed = vi.fn()
    function Controlled({ title }: { title: string }) {
      const [value, setValue] = useState('def main(inputs):\n    return {}')
      return <><span>{title}</span><PythonSourceEditor value={value} onChange={(next) => { changed(next); setValue(next) }} /></>
    }
    const rendered = render(<Controlled title="初始" />)
    const view = editor()
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: '\n# 新输入' }, selection: { anchor: 4 } }))
    expect(changed).toHaveBeenCalledOnce()
    expect(view.state.doc.toString()).toContain('# 新输入')
    rendered.rerender(<Controlled title="更新展示" />)
    expect(editor()).toBe(view)
    expect(view.state.selection.main.anchor).toBe(4)
    expect(view.state.doc.toString()).toContain('# 新输入')
    expect(changed).toHaveBeenCalledOnce()
  })

  it('载入外部源码和标签更新不回写 onChange', () => {
    const changed = vi.fn()
    const rendered = render(<PythonSourceEditor value="old" onChange={changed} />)
    const view = editor()
    rendered.rerender(<PythonSourceEditor value="new" onChange={changed} label="动作 Python 源码" />)
    expect(editor('动作 Python 源码')).toBe(view)
    expect(view.state.doc.toString()).toBe('new')
    expect(changed).not.toHaveBeenCalled()
  })

  it('中文组词期间延后外部源码替换，结束后应用最新载入且不伪回调', async () => {
    const changed = vi.fn()
    const rendered = render(<PythonSourceEditor value="# 初始源码" onChange={changed} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM, { data: '中' })
    rendered.rerender(<PythonSourceEditor value="# 第一次载入" onChange={changed} />)
    rendered.rerender(<PythonSourceEditor value="# 最新载入" onChange={changed} />)
    expect(view.state.doc.toString()).toBe('# 初始源码')
    fireEvent.compositionEnd(view.contentDOM, { data: '中文' })
    await waitFor(() => expect(view.state.doc.toString()).toBe('# 最新载入'))
    expect(changed).not.toHaveBeenCalled()
  })

  it('正常中文输入结束后保留新内容，不用尚未回传的旧 props 覆盖', async () => {
    const changed = vi.fn()
    render(<PythonSourceEditor value="# 初始" onChange={changed} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM, { data: '中' })
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: '中文输入' }, userEvent: 'input.type.compose' }))
    fireEvent.compositionEnd(view.contentDOM, { data: '中文输入' })
    await act(async () => { await Promise.resolve() })
    expect(view.state.doc.toString()).toBe('# 初始中文输入')
    expect(changed).toHaveBeenCalledExactlyOnceWith('# 初始中文输入')
  })

  it('组词期间父级确认的新输入取消过期外部载入', async () => {
    const changed = vi.fn()
    const rendered = render(<PythonSourceEditor value="# 初始" onChange={changed} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM, { data: '中' })
    rendered.rerender(<PythonSourceEditor value="# 已过期载入" onChange={changed} />)
    act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: '# 新中文输入' }, userEvent: 'input.type.compose' }))
    rendered.rerender(<PythonSourceEditor value="# 新中文输入" onChange={changed} />)
    fireEvent.compositionEnd(view.contentDOM, { data: '中文输入' })
    await act(async () => { await Promise.resolve() })
    expect(view.state.doc.toString()).toBe('# 新中文输入')
    expect(changed).toHaveBeenCalledExactlyOnceWith('# 新中文输入')
  })

  it('组词未结束时禁用会同步最新载入，恢复与迟到结束事件不会恢复旧源码', async () => {
    const changed = vi.fn()
    const rendered = render(<fieldset><PythonSourceEditor value="# 初始" onChange={changed} /></fieldset>)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM, { data: '中' })
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: '文' }, userEvent: 'input.type.compose' }))
    rendered.rerender(<fieldset disabled><PythonSourceEditor value="# 禁用后载入" onChange={changed} /></fieldset>)
    await waitFor(() => expect(view.state.doc.toString()).toBe('# 禁用后载入'))
    rendered.rerender(<fieldset><PythonSourceEditor value="# 恢复后载入" onChange={changed} /></fieldset>)
    await waitFor(() => expect(view.state.doc.toString()).toBe('# 恢复后载入'))
    fireEvent.compositionEnd(view.contentDOM, { data: '旧的中文' })
    await act(async () => { await Promise.resolve() })
    expect(view.state.doc.toString()).toBe('# 恢复后载入')
    expect(changed).toHaveBeenCalledExactlyOnceWith('# 初始文')
  })

  it('卸载取消组词后续同步，重新挂载不会收到旧编辑器的源码', async () => {
    const changed = vi.fn()
    const rendered = render(<PythonSourceEditor value="# 旧定义" onChange={changed} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM, { data: '中' })
    rendered.rerender(<PythonSourceEditor value="# 旧定义待载入" onChange={changed} />)
    fireEvent.compositionEnd(view.contentDOM, { data: '中文' })
    rendered.unmount()
    render(<PythonSourceEditor value="# 新定义" onChange={changed} />)
    await act(async () => { await Promise.resolve() })
    expect(editor().state.doc.toString()).toBe('# 新定义')
    expect(changed).not.toHaveBeenCalled()
  })

  it('显式禁用及祖先 fieldset 禁用均停止编辑，恢复后可继续编辑', async () => {
    const rendered = render(<fieldset><PythonSourceEditor value="code" onChange={vi.fn()} /></fieldset>)
    expect(editor().state.readOnly).toBe(false)
    rendered.rerender(<fieldset disabled><PythonSourceEditor value="code" onChange={vi.fn()} /></fieldset>)
    await waitFor(() => expect(editor().state.readOnly).toBe(true))
    expect(editor().contentDOM.getAttribute('contenteditable')).toBe('false')
    expect(editor().contentDOM.getAttribute('aria-disabled')).toBe('true')
    rendered.rerender(<fieldset><PythonSourceEditor value="code" disabled onChange={vi.fn()} /></fieldset>)
    expect(editor().state.readOnly).toBe(true)
    rendered.rerender(<fieldset><PythonSourceEditor value="code" onChange={vi.fn()} /></fieldset>)
    await waitFor(() => expect(editor().state.readOnly).toBe(false))
    expect(editor().contentDOM.getAttribute('aria-disabled')).toBe('false')
  })

  it('Tab 使用四空格缩进并可撤销', () => {
    render(<PythonSourceEditor value="return {}" onChange={vi.fn()} />)
    const view = editor()
    act(() => { view.focus(); view.dispatch({ selection: { anchor: 0 } }) })
    fireEvent.keyDown(view.contentDOM, { key: 'Tab', code: 'Tab' })
    expect(view.state.doc.toString()).toBe('    return {}')
    act(() => { undo(view) })
    expect(view.state.doc.toString()).toBe('return {}')
  })

  it('自动补全按钮提供 Python 内建函数与局部变量，补全可写入源码', async () => {
    const changed = vi.fn()
    render(<PythonSourceEditor value={'def main(inputs):\n    length = 1\n    le'} onChange={changed} />)
    const view = editor()
    act(() => view.dispatch({ selection: { anchor: view.state.doc.length } }))
    fireEvent.click(screen.getByRole('button', { name: '自动补全' }))
    await waitFor(() => expect(currentCompletions(view.state).some((item) => item.label === 'len')).toBe(true))
    expect(currentCompletions(view.state).some((item) => item.label === 'length')).toBe(true)
    await waitFor(() => { act(() => { expect(acceptCompletion(view)).toBe(true) }) })
    expect(changed).toHaveBeenCalled()
    expect(view.state.doc.toString()).not.toMatch(/\n    le$/)
  })

  it('补全读取最新参数声明，Ctrl+Space 可手动触发', async () => {
    const rendered = render(<PythonSourceEditor value={'inputs["'} onChange={vi.fn()} inputNames={['旧参数']} />)
    const view = editor()
    rendered.rerender(<PythonSourceEditor value={'inputs["'} onChange={vi.fn()} inputNames={['新参数']} />)
    act(() => { view.focus(); view.dispatch({ selection: { anchor: view.state.doc.length } }) })
    fireEvent.keyDown(view.contentDOM, { key: ' ', code: 'Space', ctrlKey: true })
    await waitFor(() => expect(currentCompletions(view.state).map((item) => item.label)).toContain('新参数'))
    expect(currentCompletions(view.state).map((item) => item.label)).not.toContain('旧参数')
  })

  it('输入键按中文参数名补全，处理引号转义且不在注释或普通字符串中提示', () => {
    const input = complete('value = inputs["正')
    expect(input?.from).toBe('value = inputs["'.length)
    expect(input?.options.find((option) => option.label === '正文')?.apply).toBe('正文')
    expect(input?.options.find((option) => option.label === '含"引号')?.apply).toBe('含\\"引号')
    const single = complete("inputs['", ["含'引号"])
    expect(single?.options[0].apply).toBe("含\\'引号")
    expect(complete('# inputs["')).toBeNull()
    expect(complete('text = "inputs[\\"')).toBeNull()
  })

  it('直接返回字典的首项和后续键补全输出参数，值和其他字典不提示输出键', () => {
    expect(complete('def main(inputs):\n    return {"')?.options.map((item) => item.label)).toEqual(['字符数'])
    expect(complete('return {\n    "')?.options.map((item) => item.label)).toEqual(['字符数'])
    expect(complete('return {"正文": inputs["正文"], "')?.options.map((item) => item.label)).toEqual(['字符数'])
    expect(complete('return {')?.options[0].apply).toBe('"字符数": ')
    expect(complete('return {"字符数": "')).toBeNull()
    expect(complete('settings = {"')).toBeNull()
  })
})
