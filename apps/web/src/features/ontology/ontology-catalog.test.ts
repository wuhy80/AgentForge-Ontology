import { describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { loadOntologyCatalog, MAX_ONTOLOGY_REFERENCES } from './ontology-catalog'

const categories: Organization[] = [
  { id: 'current', tenantId: 'tenant', name: '知识库' },
  { id: 'shared', tenantId: 'tenant', name: '公共词表' },
  { id: 'foreign', tenantId: 'another-tenant', name: '其他租户' },
]
const base = { tenantId: 'tenant', categoryId: 'current', groupId: 'content' }
const schema = (id: string, enumIds: string[] = []): DefinitionRecord => ({ status: 'active', etag: id, definition: {
  ...base, id, kind: 'schema', name: `对象${id}`, schema: { mode: 'schema_2_db', storageLayout: 'single', properties: enumIds.map((enumId, index) => ({ id: `field-${index}`, name: `字段${index}`, dataType: 'string', enumId })) },
} })
const enumeration = (id: string): DefinitionRecord => ({ status: 'active', etag: id, definition: {
  ...base, categoryId: 'shared', id, kind: 'enum', name: `枚举${id}`, enum: { values: [{ code: '9007199254740993', label: '精确编码' }] },
} })
function fixture(records: DefinitionRecord[], overrides: Record<string, unknown> = {}) {
  return {
    listDefinitions: vi.fn().mockResolvedValue(records),
    listGroups: vi.fn().mockResolvedValue([{ id: 'content', tenantId: 'tenant', categoryId: 'current', name: '内容' }]),
    getCurrent: vi.fn().mockImplementation(async (id: string) => enumeration(id)),
    ...overrides,
  } as unknown as ApiClient
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

describe('本体全景当前态授权目录', () => {
  it('只读取当前分类生效定义，复用目录分页并只补齐一层稳定引用', async () => {
    const root = schema('notes', ['status', 'status'])
    const action: DefinitionRecord = { status: 'active', etag: 'action', definition: { ...base, id: 'publish', kind: 'action', name: '发布笔记', action: { sideEffects: 'none', targetSchemaIds: ['notes'], functionId: 'summarize' } } }
    const linkedFunction: DefinitionRecord = { status: 'active', etag: 'fn', definition: { ...base, categoryId: 'shared', id: 'summarize', kind: 'function', name: '生成摘要', function: { sideEffects: 'none', input: [{ id: 'format', name: '格式', dataType: 'string', enumId: 'second-hop' }] } } }
    const api = fixture([root, action], { getCurrent: vi.fn().mockImplementation(async (id: string) => id === 'summarize' ? linkedFunction : enumeration(id)) })
    const catalog = await loadOntologyCatalog(api, 'current', categories)
    expect(api.listDefinitions).toHaveBeenCalledWith({ categoryId: 'current', view: 'current' }, expect.any(AbortSignal))
    expect(api.listGroups).toHaveBeenCalledWith('current', expect.any(AbortSignal))
    expect(vi.mocked(api.getCurrent).mock.calls.map(([id]) => id).sort()).toEqual(['status', 'summarize'])
    expect(catalog.records.map((record) => record.definition.id).sort()).toEqual(['notes', 'publish', 'status', 'summarize'])
    expect(catalog.groups).toHaveLength(1)
    expect(catalog).toMatchObject({ warnings: [], unavailableReferences: false, referenceTruncated: false })
    expect(catalog.records.find((record) => record.definition.id === 'status')?.definition.enum?.values[0].code).toBe('9007199254740993')
  })

  it.each([
    ['跨租户', () => [{ ...schema('notes'), definition: { ...schema('notes').definition, tenantId: 'another-tenant' } }]],
    ['错误分类', () => [{ ...schema('notes'), definition: { ...schema('notes').definition, categoryId: 'shared' } }]],
    ['草稿', () => [{ ...schema('notes'), status: 'draft' }]],
    ['重复身份', () => [schema('notes'), schema('notes')]],
    ['错误类型', () => [{ ...schema('notes'), definition: { ...schema('notes').definition, kind: 'unexpected' } }]],
  ])('主目录%s响应明确拒绝，不混入可展示目录', async (_name, records) => {
    const api = fixture(records() as DefinitionRecord[])
    await expect(loadOntologyCatalog(api, 'current', categories)).rejects.toMatchObject({ status: 502, code: 'invalid_response' })
    expect(api.getCurrent).not.toHaveBeenCalled()
  })

  it('主目录权限拒绝向上抛出，未访问的分类不发请求', async () => {
    const denied = new ApiError(403, 'forbidden', '目录权限已撤销')
    const api = fixture([], { listDefinitions: vi.fn().mockRejectedValue(denied) })
    await expect(loadOntologyCatalog(api, 'current', categories)).rejects.toBe(denied)
    const empty = fixture([])
    await expect(loadOntologyCatalog(empty, 'unknown', categories)).rejects.toMatchObject({ status: 403 })
    expect(empty.listDefinitions).not.toHaveBeenCalled()
  })

  it.each([403, 404])('外部引用HTTP%s不泄漏名称或错误细节，已授权主目录仍可浏览', async (status) => {
    const api = fixture([schema('notes', ['secret'])], { getCurrent: vi.fn().mockRejectedValue(new ApiError(status, 'unavailable', '机密枚举名称')) })
    const catalog = await loadOntologyCatalog(api, 'current', categories)
    expect(catalog.records.map((record) => record.definition.id)).toEqual(['notes'])
    expect(catalog.unavailableReferences).toBe(true)
    expect(JSON.stringify(catalog)).not.toContain('机密枚举名称')
    expect(catalog.warnings).toEqual([])
  })

  it.each([
    ['跨租户', () => ({ ...enumeration('status'), definition: { ...enumeration('status').definition, tenantId: 'another-tenant' } })],
    ['未知分类', () => ({ ...enumeration('status'), definition: { ...enumeration('status').definition, categoryId: 'inaccessible' } })],
    ['错误身份', () => enumeration('replacement')],
    ['错误类型', () => ({ ...schema('status'), definition: { ...schema('status').definition, categoryId: 'shared' } })],
    ['草稿', () => ({ ...enumeration('status'), status: 'draft' })],
  ])('补齐目标%s时不显示返回定义', async (_name, target) => {
    const api = fixture([schema('notes', ['status'])], { getCurrent: vi.fn().mockResolvedValue(target()) })
    const catalog = await loadOntologyCatalog(api, 'current', categories)
    expect(catalog.records).toHaveLength(1)
    expect(catalog.unavailableReferences).toBe(true)
  })

  it('外部引用或分组目录401终止整次加载', async () => {
    const denied = new ApiError(401, 'unauthenticated', '会话已过期')
    const api = fixture([schema('notes', ['status'])], { getCurrent: vi.fn().mockRejectedValue(denied) })
    await expect(loadOntologyCatalog(api, 'current', categories)).rejects.toBe(denied)
    const groupsDenied = fixture([schema('notes')], { listGroups: vi.fn().mockRejectedValue(denied) })
    await expect(loadOntologyCatalog(groupsDenied, 'current', categories)).rejects.toBe(denied)
  })

  it('分组和外部网络错误仅提供通用警告，不冒充授权拒绝', async () => {
    const api = fixture([schema('notes', ['status'])], {
      listGroups: vi.fn().mockRejectedValue(new ApiError(403, 'forbidden', '内部策略详情')),
      getCurrent: vi.fn().mockRejectedValue(new ApiError(503, 'unavailable', '内部服务位置')),
    })
    const catalog = await loadOntologyCatalog(api, 'current', categories)
    expect(catalog.records).toHaveLength(1)
    expect(catalog.groups).toEqual([])
    expect(catalog.warnings).toHaveLength(2)
    expect(catalog.unavailableReferences).toBe(false)
    expect(JSON.stringify(catalog)).not.toMatch(/内部策略详情|内部服务位置/)
  })

  it('按唯一引用预算和并发上限补齐，截断不伪装为完整依赖', async () => {
    let running = 0, peak = 0
    const ids = Array.from({ length: MAX_ONTOLOGY_REFERENCES + 1 }, (_, index) => `enum-${index}`)
    const api = fixture([schema('notes', ids)], { getCurrent: vi.fn().mockImplementation(async (id: string) => {
      running++; peak = Math.max(peak, running)
      await new Promise((resolve) => setTimeout(resolve, 0))
      running--
      return enumeration(id)
    }) })
    const catalog = await loadOntologyCatalog(api, 'current', categories)
    expect(api.getCurrent).toHaveBeenCalledTimes(MAX_ONTOLOGY_REFERENCES)
    expect(peak).toBeLessThanOrEqual(4)
    expect(catalog.records).toHaveLength(MAX_ONTOLOGY_REFERENCES + 1)
    expect(catalog.referenceTruncated).toBe(true)
  })

  it('在外部响应迟到且忽略signal时仍拒绝取消后的目录', async () => {
    const result = deferred<DefinitionRecord>()
    const api = fixture([schema('notes', ['status'])], { getCurrent: vi.fn().mockReturnValue(result.promise) })
    const controller = new AbortController()
    const loading = loadOntologyCatalog(api, 'current', categories, controller.signal)
    const assertion = expect(loading).rejects.toMatchObject({ name: 'AbortError' })
    await vi.waitFor(() => expect(api.getCurrent).toHaveBeenCalledTimes(1))
    controller.abort()
    result.resolve(enumeration('status'))
    await assertion
  })
})
