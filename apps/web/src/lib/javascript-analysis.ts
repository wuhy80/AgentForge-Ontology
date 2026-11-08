import { format } from 'prettier/standalone'
import babel from 'prettier/plugins/babel'
import estree from 'prettier/plugins/estree'
import type { SourceDiagnostic } from './types'

export async function analyzeJavascript(source: string): Promise<{ source: string; diagnostics: SourceDiagnostic[] }> {
  try {
    const formatted = await format(source, { parser: 'babel', plugins: [babel, estree], printWidth: 88, tabWidth: 2, semi: true, endOfLine: 'lf' })
    return { source: formatted, diagnostics: [] }
  } catch (cause) {
    const location = (cause as { loc?: { start?: { line?: number; column?: number } } })?.loc?.start
    const line = Math.max(1, Math.min(source.split('\n').length, location?.line ?? 1))
    const columnUtf16 = location?.column ?? 1
    const text = source.split('\n')[line - 1]
    const column = Array.from(text.slice(0, Math.max(0, columnUtf16 - 1))).length + 1
    return { source, diagnostics: [{ code: 'javascript_syntax_error', message: 'JavaScript 语法错误，请检查此处的括号、引号和语句', line, column }] }
  }
}
