import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Kind, Organization } from '../../lib/types'
import { collectDefinitionReferences } from './ontology-graph'

export type OntologyCatalog = {
  records: DefinitionRecord[]
  groups: Organization[]
  warnings: string[]
  referenceTruncated: boolean
  unavailableReferences: boolean
}

export const MAX_ONTOLOGY_REFERENCES = 120
const REFERENCE_CONCURRENCY = 4
const kinds = new Set<Kind>(['schema', 'enum', 'link', 'action', 'function'])
const isObject = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)

function currentRecord(value: unknown): value is DefinitionRecord {
  if (!isObject(value) || value.status !== 'active' || !isObject(value.definition)) return false
  const definition = value.definition
  if (typeof definition.id !== 'string' || !definition.id || typeof definition.tenantId !== 'string'
    || typeof definition.categoryId !== 'string' || typeof definition.name !== 'string'
    || !kinds.has(definition.kind as Kind)) return false
  const payload = definition[definition.kind as Kind]
  if (!isObject(payload)) return false
  if (definition.kind === 'schema' && !Array.isArray(payload.properties)) return false
  if (definition.kind === 'enum' && !Array.isArray(payload.values)) return false
  if (definition.kind === 'link' && (typeof payload.sourceSchemaId !== 'string' || typeof payload.targetSchemaId !== 'string')) return false
  if (['action', 'function'].includes(definition.kind as string)
    && (payload.input !== undefined && !Array.isArray(payload.input) || payload.output !== undefined && !Array.isArray(payload.output))) return false
  return true
}

function assertNotAborted(signal: AbortSignal) {
  if (signal.aborted) throw new DOMException('本体目录读取已取消', 'AbortError')
}

// All instances, runtime calls and writes stay outside this loader. Reference
// discovery is bounded to the direct references in this category's current view.
export async function loadOntologyCatalog(api: ApiClient, categoryId: string, categories: Organization[], signal?: AbortSignal): Promise<OntologyCatalog> {
  const category = categories.find((item) => item.id === categoryId)
  if (!category?.tenantId) throw new ApiError(403, 'category_unavailable', '当前分类不可访问，请重新读取目录')
  const tenantId = category.tenantId
  const accessibleCategories = new Set(categories.filter((item) => item.tenantId === tenantId).map((item) => item.id))
  const controller = new AbortController()
  const abort = () => controller.abort()
  if (signal?.aborted) controller.abort()
  else signal?.addEventListener('abort', abort, { once: true })
  const pending = controller.signal
  const warnings = new Set<string>()
  try {
    assertNotAborted(pending)
    const groupPromise = api.listGroups(categoryId, pending).then((groups) => {
      assertNotAborted(pending)
      return groups.filter((group) => group.tenantId === tenantId && group.categoryId === categoryId)
    }).catch((cause: unknown) => {
      assertNotAborted(pending)
      if (cause instanceof ApiError && cause.status === 401) throw cause
      warnings.add('分组目录暂不可用，仍可浏览已授权定义。')
      return []
    })
    const [localRecords, groups] = await Promise.all([
      api.listDefinitions({ categoryId, view: 'current' }, pending), groupPromise,
    ])
    assertNotAborted(pending)
    const records = new Map<string, DefinitionRecord>()
    if (!Array.isArray(localRecords)) throw new ApiError(502, 'invalid_response', '本体目录响应无效，请重新读取')
    for (const record of localRecords) {
      if (!currentRecord(record) || record.definition.tenantId !== tenantId || record.definition.categoryId !== categoryId
        || records.has(record.definition.id)) throw new ApiError(502, 'invalid_response', '本体目录响应无效，请重新读取')
      records.set(record.definition.id, record)
    }

    const references = new Map<string, Set<Kind>>()
    let unavailableReferences = false
    for (const record of localRecords) for (const reference of collectDefinitionReferences(record)) {
      const expected: Kind = reference.kind === 'enum' ? 'enum' : reference.kind === 'function' ? 'function' : 'schema'
      const local = records.get(reference.targetId)
      if (local) {
        if (local.definition.kind !== expected) unavailableReferences = true
        continue
      }
      const expectedKinds = references.get(reference.targetId) ?? new Set<Kind>()
      expectedKinds.add(expected)
      references.set(reference.targetId, expectedKinds)
    }
    const allTargets = [...references.entries()]
    const referenceTruncated = allTargets.length > MAX_ONTOLOGY_REFERENCES
    const targets = allTargets.slice(0, MAX_ONTOLOGY_REFERENCES)
    let next = 0
    async function readReferences() {
      while (next < targets.length) {
        assertNotAborted(pending)
        const [id, expected] = targets[next++]
        if (!id || expected.size !== 1) { unavailableReferences = true; continue }
        try {
          const record = await api.getCurrent(id, pending)
          assertNotAborted(pending)
          if (!currentRecord(record) || record.definition.id !== id || record.definition.tenantId !== tenantId
            || !accessibleCategories.has(record.definition.categoryId) || !expected.has(record.definition.kind)) {
            unavailableReferences = true
            continue
          }
          records.set(id, record)
        } catch (cause) {
          assertNotAborted(pending)
          if (cause instanceof ApiError && cause.status === 401) throw cause
          if (cause instanceof ApiError && [403, 404].includes(cause.status)) unavailableReferences = true
          else warnings.add('部分引用定义读取失败，可重新加载；已加载范围不代表完整依赖。')
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(REFERENCE_CONCURRENCY, targets.length) }, readReferences))
    assertNotAborted(pending)
    return {
      records: [...records.values()].sort((left, right) => left.definition.name.localeCompare(right.definition.name, 'zh-CN') || left.definition.id.localeCompare(right.definition.id)),
      groups, warnings: [...warnings], referenceTruncated, unavailableReferences,
    }
  } finally {
    signal?.removeEventListener('abort', abort)
    controller.abort()
  }
}
