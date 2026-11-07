import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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
    const source = screen.getByLabelText('Python 源码') as HTMLTextAreaElement
    expect(source.disabled).toBe(false)
    expect(source.value).toContain('def main(inputs):')
    fireEvent.change(source, { target: { value: 'def calculate(inputs):\n    return {"结果": inputs["原文"]}\n' } })
    fireEvent.change(screen.getByLabelText('入口函数'), { target: { value: 'calculate' } })
    fireEvent.change(screen.getByLabelText('执行超时（秒）'), { target: { value: '10' } })
    expect(changed).toHaveBeenLastCalledWith(expect.objectContaining({ runtime: expect.objectContaining({ language: 'python', entrypoint: 'calculate', timeoutSeconds: 10, source: expect.stringContaining('inputs["原文"]') }) }))
  })

  it('已生效的写入声明不允许借启用Python改成纯计算', () => {
    render(<PythonRuntimeEditor callable={{ sideEffects: 'dataWrite' }} locked onChange={vi.fn()} />)
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
})
