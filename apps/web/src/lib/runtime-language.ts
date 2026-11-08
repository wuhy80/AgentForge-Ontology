import type { ScriptLanguage, ScriptRuntime } from './types'

export const languageLabels: Record<ScriptLanguage, string> = { python: 'Python', javascript: 'JavaScript', go: 'Go' }
export const runtimeTemplates: Record<ScriptLanguage, string> = {
  python: 'def main(inputs):\n    return {}\n',
  javascript: 'function main(inputs) {\n  return {};\n}\n',
  go: 'package main\n\nfunc Main(inputs map[string]any) (map[string]any, error) {\n\treturn map[string]any{}, nil\n}\n',
}

export function initialRuntime(language: ScriptLanguage = 'python'): ScriptRuntime {
  return { language, source: runtimeTemplates[language], entrypoint: language === 'go' ? 'Main' : 'main', timeoutSeconds: language === 'go' ? 30 : 5 }
}

const keywords: Record<ScriptLanguage, Set<string>> = {
  python: new Set('False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield'.split(' ')),
  javascript: new Set('arguments eval await break case catch class const continue debugger default delete do else enum export extends false finally for function if implements import in instanceof interface let new null package private protected public return static super switch this throw true try typeof var void while with yield'.split(' ')),
  go: new Set('main init break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var'.split(' ')),
}

export function validEntrypoint(language: ScriptLanguage, name: string): boolean {
  return /^(?!__)[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name) && !keywords[language].has(name) && (language !== 'go' || name !== '_')
}
