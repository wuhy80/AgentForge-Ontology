import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeAll, describe, expect, it } from 'vitest'
import { initSync, PositionEncoding, Workspace } from '@astral-sh/ruff-wasm-web'
import { analyzePython, pythonToolSettings } from './python-analysis'
import { parameterReferenceLines } from './python-parameter-references'

let workspace: Workspace
beforeAll(() => {
  initSync({ module: readFileSync(resolve('node_modules/@astral-sh/ruff-wasm-web/ruff_wasm_bg.wasm')) })
  workspace = new Workspace(pythonToolSettings, PositionEncoding.Utf32)
})

describe('真实 Ruff WASM Python 源码工具', () => {
  it('使用固定版本并检查 Python 3.12 语法，行列从 1 开始', () => {
    expect(Workspace.version()).toBe('0.17.4')
    const diagnostics = analyzePython(workspace, 'def main(inputs)\n    return {}\n')
    expect(diagnostics).toEqual([expect.objectContaining({ code: 'invalid-syntax', line: 1, column: 17 })])
    expect(analyzePython(workspace, 'type Result = dict[str, str]\ndef main(inputs):\n    return {}\n')).toEqual([])
    expect(analyzePython(workspace, 'x = 1\ny = "中文😀"; !!!')).toEqual(expect.arrayContaining([expect.objectContaining({ line: 2, column: 12 })]))
  })

  it('检查和格式化不执行顶层代码，保留注释与字符串，仅修改格式', () => {
    const source = '# 中文说明\nraise RuntimeError("must not execute")\ndef main( inputs ):\n return {\'结果\':inputs[\'正文\']} # 业务注释\n'
    expect(analyzePython(workspace, source)).toEqual([])
    expect(workspace.format(source)).toBe('# 中文说明\nraise RuntimeError("must not execute")\n\n\ndef main(inputs):\n    return {\'结果\': inputs[\'正文\']}  # 业务注释\n')
    expect(workspace.format(workspace.format(source))).toBe(workspace.format(source))
  })

  it('仅提示真实字面量键引用，覆盖下标/get/输出键及转义并忽略注释和文本', () => {
    const source = '# inputs["原文"]\ntext = \'inputs["原文"]\'\nvalue = inputs["原文"]\nsecond = inputs.get("原文")\nreturn {"原文": value, "结果": "原文"}\n'
    expect(parameterReferenceLines(source, '原文')).toEqual([3, 4, 5])
    expect(parameterReferenceLines('x = inputs["含\\\"引号"]', '含"引号')).toEqual([1])
    expect(parameterReferenceLines('return {"\\u539f\\u6587": 1}', '原文')).toEqual([1])
    expect(parameterReferenceLines('"""inputs["原文"]"""', '原文')).toEqual([])
  })
})
