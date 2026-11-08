import { goLanguage } from '@codemirror/lang-go'
import type { SourceDiagnostic } from './types'

// Editor grammar hints help while typing. gofmt and the Go compiler remain
// authoritative, and only run in the execution service's isolated container.
export function analyzeGoSource(source: string): SourceDiagnostic[] {
  if (new TextEncoder().encode(source).byteLength > 65536) return [{ code: 'source_budget', message: '源码超过 64 KiB，请缩短源码后重试。', line: 1 }]
  const diagnostics: SourceDiagnostic[] = []
  goLanguage.parser.parse(source).iterate({ enter(node) {
    if (!node.type.isError || diagnostics.length >= 20) return
    const before = source.slice(0, node.from).split('\n')
    diagnostics.push({ code: 'go_editor_syntax_hint', message: 'Go 编辑器语法提示：请检查此处；完整语法检查使用「检查语法」', line: before.length, column: Array.from(before.at(-1) ?? '').length + 1 })
  } })
  return diagnostics
}
