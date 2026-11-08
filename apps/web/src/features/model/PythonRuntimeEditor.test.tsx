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
})
