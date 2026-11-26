import type { DefinitionRecord } from '../../lib/types'
import type { DefinitionReference } from './ontology-graph'

const literalId = /^(['"`])([a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12})\1$/i
const operations = /^(?:bulk_?(?:create|update|delete|upsert)|create|update|delete|upsert)$/i

export function collectCallableTargets({ definition }: DefinitionRecord): DefinitionReference[] {
  const callable = definition.action ?? definition.function
  if (!callable) return []
  const references = new Map<string, DefinitionReference>()
  function add(targetId: string, label: string, kind: 'target' | 'function' = 'target') {
    references.set(JSON.stringify([targetId, label]), { targetId, label, kind })
  }
  for (const id of callable.targetSchemaIds ?? []) add(id, '作用对象')
  for (const target of callable.dataWrite ?? []) add(target.schemaId, '声明写入')
  if (callable.recordRead) add(callable.recordRead.schemaId, '声明读取')
  if (callable.functionId) add(callable.functionId, '绑定函数')
  const runtime = callable.runtime
  if (!runtime) return [...references.values()]
  const tokens = runtime.source.match(/\/\*[\s\S]*?\*\/|\/\/[^\r\n]*|#[^\r\n]*|"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:\\[\s\S]|[^"\\])*"|'(?:\\[\s\S]|[^'\\])*'|`[\s\S]*?`|[a-zA-Z_$][\w$]*|[^\s]/g)?.filter(token => !token.startsWith('#') && !token.startsWith('//') && !token.startsWith('/*')) ?? []
  for (let i = 0; i + 7 < tokens.length; i++) {
    const [receiver, dot, member, secondDot, operation, open, literal, separator] = tokens.slice(i, i + 8)
    if (receiver !== 'ctx' || dot !== '.' || member.toLowerCase() !== 'data' || secondDot !== '.' || !operations.test(operation) || open !== '(' || ![',', ')'].includes(separator)) continue
    const id = literalId.exec(literal)?.[2]
    if (id) add(id.toLowerCase(), '源码写入')
  }
  return [...references.values()]
}
