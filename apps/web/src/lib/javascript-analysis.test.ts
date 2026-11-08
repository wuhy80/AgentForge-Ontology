import { describe, expect, it } from 'vitest'
import { analyzeJavascript } from './javascript-analysis'
import { analyzeGoSource } from './go-source-analysis'
import { parameterReferenceLines } from './python-parameter-references'
import { initialRuntime, validEntrypoint } from './runtime-language'

describe('JavaScript 与 Go 源码工具', () => {
  it('真实 JavaScript 格式化保留注释、中文与精度字符串且不执行顶层代码', async () => {
    const source = '// 中文说明\nthrow Error("must not execute");\nfunction main( inputs ){return {"结果":"9007199254740993", "正文":inputs["正文"]};}// 注释\n'
    const result = await analyzeJavascript(source)
    expect(result.diagnostics).toEqual([])
    expect(result.source).toContain('// 中文说明')
    expect(result.source).toContain('// 注释')
    expect(result.source).toContain('"9007199254740993"')
    expect(result.source).toContain('function main(inputs) {')
    expect(await analyzeJavascript(result.source)).toEqual(result)
  })

  it('语法错误保留原始源码，Unicode 码点行列从 1 起', async () => {
    const source = 'function main(inputs) {\n  const text = "中文😀"; return );\n}'
    const result = await analyzeJavascript(source)
    const line = source.split('\n')[1]
    expect(result.source).toBe(source)
    expect(result.diagnostics).toEqual([expect.objectContaining({ code: 'javascript_syntax_error', line: 2, column: Array.from(line.slice(0, line.indexOf(')'))).length + 1 })])
  })

  it('Go 本地语法提示不冒充编译，支持源码字节预算', () => {
    expect(analyzeGoSource(initialRuntime('go').source)).toEqual([])
    expect(analyzeGoSource('package main\nfunc Main( {')).toEqual(expect.arrayContaining([expect.objectContaining({ code: 'go_editor_syntax_hint', line: 2 })]))
    expect(analyzeGoSource('中'.repeat(22000))[0].code).toBe('source_budget')
  })

  it.each(['javascript', 'go'] as const)('%s 改名提示只识别真实输入与输出键引用', (language) => {
    const source = language === 'go'
      ? 'package main\n// inputs["原文"]\nvar text = "inputs[\\"原文\\"]"\nfunc Main(inputs map[string]any) (map[string]any, error) {\n return map[string]any{"原文": inputs["原文"], "结果": "原文"}, nil\n}'
      : '// inputs["原文"]\nconst text = \'inputs["原文"]\';\nfunction main(inputs) {\n return {"原文": inputs["原文"], "结果": "原文"};\n}'
    expect(parameterReferenceLines(source, '原文', language)).toEqual([language === 'go' ? 5 : 4])
  })

  it('各语言入口校验拒绝保留关键字并采用各自默认入口及超时', () => {
    expect(initialRuntime('javascript')).toMatchObject({ entrypoint: 'main', timeoutSeconds: 5 })
    expect(initialRuntime('go')).toMatchObject({ entrypoint: 'Main', timeoutSeconds: 30 })
    for (const name of ['eval', 'arguments', 'return', 'class', '__handler']) expect(validEntrypoint('javascript', name)).toBe(false)
    for (const name of ['main', 'init', '_', 'func']) expect(validEntrypoint('go', name)).toBe(false)
    expect(validEntrypoint('javascript', 'calculate')).toBe(true)
    expect(validEntrypoint('go', 'Calculate')).toBe(true)
  })
})
