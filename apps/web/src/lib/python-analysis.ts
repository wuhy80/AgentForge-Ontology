import type { Diagnostic, Workspace } from '@astral-sh/ruff-wasm-web'
import type { SourceDiagnostic } from './types'

export const pythonToolSettings = {
  'target-version': 'py312',
  'line-length': 88,
  'indent-width': 4,
  // Parsing only: no style checks, import rewriting or automatic fixes.
  lint: { select: [] },
  format: { 'indent-style': 'space', 'quote-style': 'preserve', 'line-ending': 'lf' },
}

export function analyzePython(workspace: Pick<Workspace, 'check'>, source: string): SourceDiagnostic[] {
  return (workspace.check(source) as Diagnostic[]).slice(0, 50).map((diagnostic) => ({
    code: diagnostic.code ?? 'invalid-syntax',
    message: `Python 语法：${diagnostic.message}`,
    line: diagnostic.start_location.row,
    column: diagnostic.start_location.column,
    endLine: diagnostic.end_location.row,
    endColumn: diagnostic.end_location.column,
  }))
}
