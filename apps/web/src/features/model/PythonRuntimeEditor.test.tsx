import { useState } from 'react'
import { EditorView } from '@codemirror/view'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Callable } from '../../lib/types'
import { PythonRuntimeEditor } from './PythonRuntimeEditor'
import { newDefinition, validateDefinition } from './model-helpers'

afterEach(cleanup)

describe('Python 实现编辑', () => {
  it('可选启用并编辑源码、入口、超时，已发布纯计算仍可更新代码', async () => {
    const changed = vi.fn()
    function Editor() {
      const [callable, setCallable] = useState<Callable>({ sideEffects: 'none' })
      return <PythonRuntimeEditor callable={callable} locked onChange={(value) => { setCallable(value); changed(value) }} />
    }
    render(<Editor />)
    expect(screen.queryByLabelText('Python 源码')).toBeNull()
    await userEvent.click(screen.getByLabelText('启用 Python 实现'))
    const source = screen.getByRole('textbox', { name: 'Python 源码' })
    expect(source.getAttribute('contenteditable')).toBe('true')
    const view = EditorView.findFromDOM(source)!
    expect(view.state.doc.toString()).toContain('def main(inputs):')
    act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: 'def calculate(inputs):\n    return {"结果": inputs["原文"]}\n' } }))
    fireEvent.change(screen.getByLabelText('入口函数'), { target: { value: 'calculate' } })
    fireEvent.change(screen.getByLabelText('执行超时（秒）'), { target: { value: '10' } })
    expect(changed).toHaveBeenLastCalledWith(expect.objectContaining({ runtime: expect.objectContaining({ language: 'python', entrypoint: 'calculate', timeoutSeconds: 10, source: expect.stringContaining('inputs["原文"]') }) }))
  })

  it('已生效的写入声明不允许借启用Python改成纯计算', () => {
    render(<PythonRuntimeEditor callable={{ sideEffects: 'dataWrite' }} locked onChange={vi.fn()} />)
    expect((screen.getByLabelText('启用 Python 实现') as HTMLInputElement).disabled).toBe(true)
  })

  it('草稿动作启用独立源码时清除函数绑定，已生效绑定不允许切换', async () => {
    const changed = vi.fn()
    const view = render(<PythonRuntimeEditor kind="action" callable={{ sideEffects: 'none', functionId: 'fn' }} locked={false} onChange={changed} />)
    expect(screen.getByRole('region', { name: '动作源码' })).toBeTruthy()
    await userEvent.click(screen.getByLabelText('启用 Python 实现'))
    expect(changed).toHaveBeenCalledWith(expect.objectContaining({ functionId: undefined, sideEffects: 'none', runtime: expect.objectContaining({ language: 'python' }) }))
    view.rerender(<PythonRuntimeEditor kind="action" callable={{ sideEffects: 'none', functionId: 'fn' }} locked onChange={changed} />)
    expect((screen.getByLabelText('启用 Python 实现') as HTMLInputElement).disabled).toBe(true)
  })

  it('保存前验证源码预算、入口、超时及互斥实现声明', () => {
    const definition = newDefinition('function', 'category', 'group')
    definition.name = '计算'
    definition.function!.runtime = { language: 'python', source: 'def main(inputs):\n    return {}', entrypoint: 'main', timeoutSeconds: 5 }
    expect(validateDefinition(definition)).toBeUndefined()
    definition.function!.runtime.timeoutSeconds = 31
    expect(validateDefinition(definition)).toContain('1–30')
    definition.function!.runtime.timeoutSeconds = 5
    definition.function!.runtime.entrypoint = '__import__'
    expect(validateDefinition(definition)).toContain('入口')
    definition.function!.runtime.entrypoint = 'main'
    definition.function!.runtime.source = '中'.repeat(22000)
    expect(validateDefinition(definition)).toContain('64 KiB')
    definition.function!.runtime.source = 'def main(inputs):\n    return {}'
    definition.function!.artifactRef = 'registry/handler'
    expect(validateDefinition(definition)).toContain('不能同时配置')
  })

  it('动作源码可独立保存，但拒绝函数绑定和副作用冲突', () => {
    const definition = newDefinition('action', 'category', 'group')
    definition.name = '独立动作'
    definition.action!.sideEffects = 'none'
    definition.action!.runtime = { language: 'python', source: 'def main(inputs):\n    return {}', entrypoint: 'main', timeoutSeconds: 5 }
    expect(validateDefinition(definition)).toBeUndefined()
    definition.action!.functionId = 'fn'
    expect(validateDefinition(definition)).toContain('不能同时配置')
    definition.action!.functionId = undefined
    definition.action!.sideEffects = 'dataWrite'
    expect(validateDefinition(definition)).toContain('无副作用')
  })

  it('按稳定参数ID提示改名后的旧字面量引用，修正后消失且不自动改源码', () => {
    const changed = vi.fn()
    const callable: Callable = {
      sideEffects: 'none',
      input: [{ id: 'input-id', name: '原文', dataType: 'string' }],
      output: [{ id: 'output-id', name: '旧结果', dataType: 'string' }],
      runtime: { language: 'python', entrypoint: 'main', timeoutSeconds: 5, source: 'def main(inputs):\n    return {"旧结果": inputs["原文"]}\n' },
    }
    const rendered = render(<PythonRuntimeEditor callable={callable} locked={false} onChange={changed} />)
    expect(screen.queryByRole('list', { name: '参数改名引用提醒' })).toBeNull()
    const renamed: Callable = { ...callable, input: [{ ...callable.input![0], name: '正文' }], output: [{ ...callable.output![0], name: '结果' }] }
    rendered.rerender(<PythonRuntimeEditor callable={renamed} locked={false} onChange={changed} />)
    expect(screen.getByText(/输入参数「原文」已改为「正文」，第 2 行仍可能使用旧名称/)).toBeTruthy()
    expect(screen.getByText(/输出参数「旧结果」已改为「结果」，第 2 行仍可能使用旧名称/)).toBeTruthy()
    expect(changed).not.toHaveBeenCalled()
    expect(EditorView.findFromDOM(screen.getByRole('textbox', { name: 'Python 源码' }))!.state.doc.toString()).toBe(callable.runtime!.source)
    rendered.rerender(<PythonRuntimeEditor callable={{ ...renamed, runtime: { ...renamed.runtime!, source: 'def main(inputs):\n    return {"结果": inputs["正文"]}\n' } }} locked={false} onChange={changed} />)
    expect(screen.queryByRole('list', { name: '参数改名引用提醒' })).toBeNull()
  })

  it('切换到其他稳定ID的定义不继承上一份参数改名提醒', () => {
    const runtime = { language: 'python' as const, entrypoint: 'main', timeoutSeconds: 5, source: 'return {"结果": inputs["原文"]}' }
    const rendered = render(<PythonRuntimeEditor callable={{ sideEffects: 'none', input: [{ id: 'first', name: '原文', dataType: 'string' }], runtime }} locked={false} onChange={vi.fn()} />)
    rendered.rerender(<PythonRuntimeEditor callable={{ sideEffects: 'none', input: [{ id: 'second', name: '正文', dataType: 'string' }], runtime }} locked={false} onChange={vi.fn()} />)
    expect(screen.queryByRole('list', { name: '参数改名引用提醒' })).toBeNull()
  })
})
