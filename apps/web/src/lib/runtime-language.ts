import type { ScriptLanguage, ScriptRuntime } from './types'

export const languageLabels: Record<ScriptLanguage, string> = { python: 'Python', javascript: 'JavaScript', go: 'Go' }
export const runtimeTemplates: Record<ScriptLanguage, string> = {
  python: 'def main(inputs):\n    return {}\n',
  javascript: 'function main(inputs) {\n  return {};\n}\n',
  go: 'package main\n\nfunc Main(inputs map[string]any) (map[string]any, error) {\n\treturn map[string]any{}, nil\n}\n',
}
export const dataWriteTemplates: Record<ScriptLanguage, string> = {
  python: 'def main(inputs, ctx):\n    # ctx.data 暂存写入计划；返回并验证输出后整批提交。\n    return {}\n',
  javascript: 'function main(inputs, ctx) {\n  // ctx.data 暂存写入计划；返回并验证输出后整批提交。\n  return {};\n}\n',
  go: 'package main\n\nfunc Main(inputs map[string]any, ctx *OntologyContext) (map[string]any, error) {\n\t// ctx.Data 暂存写入计划；返回并验证输出后整批提交。\n\treturn map[string]any{}, nil\n}\n',
}
export const dataWriteSDKSignatures: Record<ScriptLanguage, string> = {
  python: 'ctx.data.create(schemaId, values, clientKey)\nctx.data.update(schemaId, id, values, ifMatch, clientKey)\nctx.data.delete(schemaId, id, ifMatch, clientKey)\nctx.data.upsert(schemaId, match, values, ifMatch, clientKey, conflictPolicy="check")\nctx.data.bulk_create(schemaId, items)\nctx.data.bulk_update(schemaId, items)\nctx.data.bulk_delete(schemaId, items)\nctx.data.bulk_upsert(schemaId, items, conflictPolicy="check")',
  javascript: 'ctx.data.create(schemaId, values, clientKey)\nctx.data.update(schemaId, id, values, ifMatch, clientKey)\nctx.data.delete(schemaId, id, ifMatch, clientKey)\nctx.data.upsert(schemaId, match, values, ifMatch, clientKey, conflictPolicy = "check")\nctx.data.bulk_create(schemaId, items)\nctx.data.bulk_update(schemaId, items)\nctx.data.bulk_delete(schemaId, items)\nctx.data.bulk_upsert(schemaId, items, conflictPolicy = "check")',
  go: 'ctx.Data.Create(schemaID, values, clientKey)\nctx.Data.Update(schemaID, id, values, ifMatch, clientKey)\nctx.Data.Delete(schemaID, id, ifMatch, clientKey)\nctx.Data.Upsert(schemaID, match, values, ifMatch, clientKey, conflictPolicy)\nctx.Data.BulkCreate(schemaID, items)\nctx.Data.BulkUpdate(schemaID, items)\nctx.Data.BulkDelete(schemaID, items)\nctx.Data.BulkUpsert(schemaID, items, conflictPolicy)\n// match: OntologyMatch{ID, IndexID, Values}\n// items: []OntologyWriteItem{ClientKey, ID, IfMatch, Match, Values}',
}

export function initialRuntime(language: ScriptLanguage = 'python', dataWrite = false): ScriptRuntime {
  return { language, source: (dataWrite ? dataWriteTemplates : runtimeTemplates)[language], entrypoint: language === 'go' ? 'Main' : 'main', timeoutSeconds: language === 'go' ? 30 : 5 }
}

const keywords: Record<ScriptLanguage, Set<string>> = {
  python: new Set('False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield'.split(' ')),
  javascript: new Set('arguments eval await break case catch class const continue debugger default delete do else enum export extends false finally for function if implements import in instanceof interface let new null package private protected public return static super switch this throw true try typeof var void while with yield'.split(' ')),
  go: new Set('main init break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var'.split(' ')),
}

export function validEntrypoint(language: ScriptLanguage, name: string): boolean {
  return /^(?!__)[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name) && !keywords[language].has(name) && (language !== 'go' || name !== '_')
}
