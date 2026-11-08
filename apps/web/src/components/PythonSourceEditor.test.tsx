import { useState } from 'react'
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CompletionContext, acceptCompletion, currentCompletions } from '@codemirror/autocomplete'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { python } from '@codemirror/lang-python'
import { javascript } from '@codemirror/lang-javascript'
import { go } from '@codemirror/lang-go'
import { undo } from '@codemirror/commands'
import type { ScriptLanguage, SourceToolResult } from '../lib/types'
import { initialRuntime } from '../lib/runtime-language'
import { PythonSourceEditor, pythonParameterCompletions, scriptParameterCompletions } from './PythonSourceEditor'

const tools = vi.hoisted(() => ({ checkPythonSource: vi.fn(), formatPythonSource: vi.fn() }))
const javascriptTools = vi.hoisted(() => ({ checkJavascriptSource: vi.fn(), formatJavascriptSource: vi.fn() }))
vi.mock('../lib/python-source-tools', () => tools)
vi.mock('../lib/javascript-source-tools', () => javascriptTools)
beforeEach(() => { tools.checkPythonSource.mockReset().mockResolvedValue([]); tools.formatPythonSource.mockReset(); javascriptTools.checkJavascriptSource.mockReset().mockResolvedValue([]); javascriptTools.formatJavascriptSource.mockReset() })

const rect = { x: 0, y: 0, width: 100, height: 20, top: 0, right: 100, bottom: 20, left: 0, toJSON: () => ({}) }
const originalRects = Object.getOwnPropertyDescriptor(Range.prototype, 'getClientRects')
const originalRect = Object.getOwnPropertyDescriptor(Range.prototype, 'getBoundingClientRect')

beforeAll(() => {
  Object.defineProperty(Range.prototype, 'getClientRects', { configurable: true, value: () => [rect] })
  Object.defineProperty(Range.prototype, 'getBoundingClientRect', { configurable: true, value: () => rect })
})

describe('JavaScript 与 Go 源码编辑器', () => {
  function completeScript(language: ScriptLanguage, text: string) {
    const state = EditorState.create({ doc: text, extensions: [language === 'go' ? go() : javascript()] })
    return scriptParameterCompletions(new CompletionContext(state, text.length, true), language, ['正文', '含"引号'], ['结果'])
  }

  it.each(['javascript', 'go'] as const)('%s 提供独立语言高亮、标签和缩进', (language) => {
    render(<PythonSourceEditor language={language} value={initialRuntime(language).source} onChange={vi.fn()} />)
    const view = editor(language === 'go' ? 'Go 源码' : 'JavaScript 源码')
    expect(view.contentDOM.querySelector('.cm-line span')).toBeTruthy()
    act(() => { view.focus(); view.dispatch({ selection: { anchor: 0 } }) })
    fireEvent.keyDown(view.contentDOM, { key: 'Tab', code: 'Tab' })
    expect(view.state.doc.toString()).toBe((language === 'go' ? '\t' : '  ') + initialRuntime(language).source)
  })

  it.each(['javascript', 'go'] as const)('%s 按语法树补全参数键，不在文本、注释或嵌套对象误提示', (language) => {
    const prefix = language === 'go' ? 'package main\nfunc Main(inputs map[string]any) (map[string]any, error) { return map[string]any' : 'function main(inputs) { return '
    expect(completeScript(language, 'inputs["正')?.options.map((item) => item.label)).toEqual(['正文', '含"引号'])
    expect(completeScript(language, 'inputs["')?.options[1].apply).toBe('含\\"引号')
    expect(completeScript(language, prefix + '{"')?.options.map((item) => item.label)).toEqual(['结果'])
    expect(completeScript(language, prefix + '{')?.options[0].apply).toBe('"结果": ')
    expect(completeScript(language, prefix + '{"a": 1, "')?.options[0].label).toBe('结果')
    expect(completeScript(language, prefix + '{"a": "')).toBeNull()
    expect(completeScript(language, '// inputs["')).toBeNull()
    expect(completeScript(language, 'text = "inputs[\'')).toBeNull()
    expect(completeScript(language, prefix + '{"a": {"')).toBeNull()
    expect(completeScript(language, 'settings = {"')).toBeNull()
  })

  it.each(['javascript', 'go'] as const)('%s 补全按钮包含真实局部变量', async (language) => {
    const source = language === 'go' ? 'package main\nfunc Main(inputs map[string]any) (map[string]any, error) {\n localCount := 1\n lo' : 'function main(inputs) {\n const localCount = 1;\n lo'
    render(<PythonSourceEditor language={language} value={source} onChange={vi.fn()} />)
    const view = editor(language === 'go' ? 'Go 源码' : 'JavaScript 源码')
    act(() => view.dispatch({ selection: { anchor: view.state.doc.length } }))
    fireEvent.click(screen.getByRole('button', { name: '自动补全' }))
    await waitFor(() => expect(currentCompletions(view.state).some((item) => item.label === 'localCount')).toBe(true))
  })

  it('JavaScript 检查与格式化仅使用对应的 Worker 工具', async () => {
    javascriptTools.formatJavascriptSource.mockResolvedValue('function main(inputs) {\n  return {};\n}\n')
    render(<PythonSourceEditor language="javascript" value="function main(inputs){return {}}" onChange={vi.fn()} />)
    await waitFor(() => expect(javascriptTools.checkJavascriptSource).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    await waitFor(() => expect(editor('JavaScript 源码').state.doc.toString()).toContain('  return {};'))
    expect(tools.formatPythonSource).not.toHaveBeenCalled()
    expect(tools.checkPythonSource).not.toHaveBeenCalled()
  })

  it('Go 新定义未保存时禁用容器工具并说明原因', () => {
    render(<PythonSourceEditor language="go" value={initialRuntime('go').source} onChange={vi.fn()} />)
    expect((screen.getByRole('button', { name: '检查语法' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '格式化代码' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('请先保存草稿，再使用 Go 检查与格式化。')).toBeTruthy()
  })

  it('Go 容器错误优先于迟到的本地语法提示，格式化错误保留源码', async () => {
    const source = initialRuntime('go').source
    const result = { source, diagnostics: [{ code: 'syntax_error', message: '容器发现语法错误', line: 3, column: 1 }] }
    const sourceTools = { check: vi.fn().mockResolvedValue(result), format: vi.fn().mockResolvedValue(result) }
    const changed = vi.fn()
    render(<PythonSourceEditor language="go" value={source} onChange={changed} sourceTools={sourceTools} />)
    fireEvent.click(screen.getByRole('button', { name: '检查语法' }))
    await screen.findByRole('button', { name: /容器发现语法错误/ })
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 550)) })
    expect(screen.getByRole('button', { name: /容器发现语法错误/ })).toBeTruthy()
    expect(screen.queryByText(/Go 容器语法检查通过/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    await screen.findByText(/源码存在语法错误/)
    expect(editor('Go 源码').state.doc.toString()).toBe(source)
    expect(changed).not.toHaveBeenCalled()
  })

  it('Go 组词和禁用期间不调用容器，修改源码后忽略迟到诊断', async () => {
    let resolve!: (value: SourceToolResult) => void
    const source = initialRuntime('go').source
    const sourceTools = { check: vi.fn().mockImplementation(() => new Promise<SourceToolResult>((done) => { resolve = done })), format: vi.fn() }
    render(<PythonSourceEditor language="go" value={source} onChange={vi.fn()} sourceTools={sourceTools} />)
    const view = editor('Go 源码')
    fireEvent.compositionStart(view.contentDOM)
    fireEvent.click(screen.getByRole('button', { name: '检查语法' }))
    expect(sourceTools.check).not.toHaveBeenCalled()
    fireEvent.compositionEnd(view.contentDOM)
    await act(async () => { await Promise.resolve() })
    fireEvent.click(screen.getByRole('button', { name: '检查语法' }))
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: '// 新源码' } }))
    await act(async () => resolve({ source, diagnostics: [{ code: 'syntax_error', message: '迟到的旧错误', line: 1 }] }))
    expect(screen.queryByText(/迟到的旧错误/)).toBeNull()
    expect(view.state.doc.toString()).toContain('// 新源码')
  })
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

  it('自动检查标红并点击诊断定位，对中文和 emoji 使用正确源码偏移', async () => {
    tools.checkPythonSource.mockResolvedValue([{ code: 'invalid-syntax', message: '应填写冒号', line: 2, column: 13, endLine: 2, endColumn: 14 }])
    const source = 'def main(inputs):\n    x="😀中文";!\n'
    render(<PythonSourceEditor value={source} onChange={vi.fn()} />)
    const view = editor()
    await waitFor(() => expect(screen.getByRole('button', { name: /第 2 行，第 13 列：应填写冒号/ })).toBeTruthy())
    expect(view.dom.querySelector('.cm-lintRange-error')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /第 2 行，第 13 列：应填写冒号/ }))
    expect(view.state.selection.main.anchor).toBe(source.indexOf('!'))
  })

  it('语法检查旧响应不会标注最新源码，失败可重试', async () => {
    let resolve!: (result: unknown) => void
    tools.checkPythonSource.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const rendered = render(<PythonSourceEditor value="invalid" onChange={vi.fn()} />)
    await waitFor(() => expect(tools.checkPythonSource).toHaveBeenCalledWith('invalid'))
    tools.checkPythonSource.mockRejectedValueOnce(new Error('Python 工具暂不可用，请重试。'))
    rendered.rerender(<PythonSourceEditor value="pass" onChange={vi.fn()} />)
    act(() => resolve([{ code: 'invalid-syntax', message: '旧源码错误', line: 1 }]))
    expect(screen.queryByText(/旧源码错误/)).toBeNull()
    await waitFor(() => expect(screen.getByRole('button', { name: '重试语法检查' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '重试语法检查' }))
    await waitFor(() => expect(screen.getByText(/本地语法检查通过/)).toBeTruthy())
  })

  it('格式化只应用当前快照且可一次撤销', async () => {
    const original = 'def main( inputs ):\n return {}'
    const formatted = 'def main(inputs):\n    return {}\n'
    tools.formatPythonSource.mockResolvedValue(formatted)
    function Controlled() {
      const [value, setValue] = useState(original)
      return <PythonSourceEditor value={value} onChange={setValue} />
    }
    render(<Controlled />)
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    await waitFor(() => expect(editor().state.doc.toString()).toBe(formatted))
    expect(screen.getByText('已格式化，可撤销。')).toBeTruthy()
    act(() => { undo(editor()) })
    expect(editor().state.doc.toString()).toBe(original)
  })

  it('等待格式化时继续输入会丢弃迟到结果，组词期间不发起格式化', async () => {
    let resolve!: (result: string) => void
    tools.formatPythonSource.mockImplementation(() => new Promise((done) => { resolve = done }))
    render(<PythonSourceEditor value="pass" onChange={vi.fn()} />)
    const view = editor()
    fireEvent.compositionStart(view.contentDOM)
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    expect(tools.formatPythonSource).not.toHaveBeenCalled()
    fireEvent.compositionEnd(view.contentDOM)
    await act(async () => { await Promise.resolve() })
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    act(() => view.dispatch({ changes: { from: 4, insert: '\n# 新内容' } }))
    await act(async () => resolve('pass\n'))
    expect(view.state.doc.toString()).toBe('pass\n# 新内容')
    expect(screen.getByText(/已忽略本次格式化结果/)).toBeTruthy()
  })

  it('祖先禁用期间的格式化响应不能在恢复后写回，支持运行错误外部定位', async () => {
    let resolve!: (result: string) => void
    tools.formatPythonSource.mockImplementation(() => new Promise((done) => { resolve = done }))
    const changed = vi.fn()
    const source = 'def main(inputs):\n    return 1/0'
    const diagnostic = { code: 'python_failed', message: 'Python 运算失败', line: 2 }
    const rendered = render(<fieldset><PythonSourceEditor value={source} onChange={changed} diagnostics={[diagnostic]} /></fieldset>)
    fireEvent.click(screen.getByRole('button', { name: '格式化代码' }))
    rendered.rerender(<fieldset disabled><PythonSourceEditor value={source} onChange={changed} /></fieldset>)
    await waitFor(() => expect(editor().state.readOnly).toBe(true))
    rendered.rerender(<fieldset><PythonSourceEditor value={source} onChange={changed} diagnosticFocus={diagnostic} /></fieldset>)
    await waitFor(() => expect(editor().state.readOnly).toBe(false))
    await act(async () => resolve('rewritten'))
    expect(changed).not.toHaveBeenCalled()
    expect(editor().state.doc.toString()).toBe(source)
    expect(editor().state.selection.main.anchor).toBe(source.indexOf('    return'))
  })
})
