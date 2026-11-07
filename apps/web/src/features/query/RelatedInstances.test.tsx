import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ApiClient } from '../../lib/api'
import { ApiError } from '../../lib/api'
import type { DefinitionRecord, QueryResult, SchemaLink } from '../../lib/types'
import { RelatedInstances } from './RelatedInstances'

afterEach(cleanup)

const schema: DefinitionRecord = { status: 'active', etag: 'schema-current', definition: {
  id: 'notes', tenantId: 'tenant', categoryId: 'knowledge', groupId: 'group', kind: 'schema', name: '笔记',
  schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'title', name: '标题', dataType: 'string' }] },
} }
const target: DefinitionRecord = { ...schema, definition: { ...schema.definition, id: 'tags', name: '标签', schema: { ...schema.definition.schema!, properties: [{ id: 'label', name: '标签名称', dataType: 'string' }] } } }
const link: DefinitionRecord = { status: 'active', etag: 'link-current', definition: {
  ...schema.definition, id: 'note-tags', kind: 'link', name: '笔记标记标签', schema: undefined,
  link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'managed' } },
} }
const out: SchemaLink = { linkRef: { id: 'note-tags' }, direction: 'out', name: '标记标签', sourceSchemaId: 'notes', targetSchemaId: 'tags', cardinality: 'N:M' }
const result: QueryResult = { fields: target.definition.schema!.properties, data: [{ id: 'tag-1', schemaId: 'tags', etag: 'tag-current', values: { label: '知识图谱' } }], hasMore: true, nextCursor: 'related-cursor' }
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((yes) => { resolve = yes })
  return { promise, resolve }
}
function fixture(navigation = out, currentLink = link) {
  const api = {
    schemaLinks: vi.fn().mockResolvedValue([navigation]),
    getCurrent: vi.fn().mockImplementation(async (id) => id === 'note-tags' ? currentLink : id === 'notes' ? schema : target),
    query: vi.fn().mockResolvedValue(result),
    listLinkInstances: vi.fn().mockResolvedValue({ data: [{ id: 'edge-1', linkId: 'note-tags', sourceId: 'note-1', targetId: 'tag-1', etag: 'edge-current' }], hasMore: true, nextCursor: 'edges-cursor' }),
    createLinkInstance: vi.fn().mockResolvedValue({ id: 'new-edge', etag: 'new-current' }),
    deleteLinkInstance: vi.fn().mockResolvedValue(undefined),
  }
  const props = { api: api as unknown as ApiClient, schema, instanceId: 'note-1', onClose: vi.fn(), onForbidden: vi.fn() }
  return { api, props }
}

describe('真实关联数据界面', () => {
  it('按 Link 和方向查询，条件及投影作用于返回对象，续页复用已执行的关系查询', async () => {
    const { api, props } = fixture()
    render(<RelatedInstances {...props} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: '关联字段：标签名称' }))
    fireEvent.change(screen.getByLabelText('关联筛选属性'), { target: { value: 'label' } })
    fireEvent.change(screen.getByLabelText('比较值'), { target: { value: '知识图谱' } })
    fireEvent.click(screen.getByRole('button', { name: '查询关联对象' }))
    await within(await screen.findByRole('region', { name: '关联查询结果' })).findByText('知识图谱')
    expect(api.query.mock.calls[0][0]).toEqual({ from: { schemaId: 'tags' }, via: { linkId: 'note-tags', direction: 'out', instanceId: 'note-1' }, select: ['label'], where: { op: 'and', args: [{ op: 'eq', field: 'label', value: '知识图谱' }] }, page: { limit: 25 } })
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(2))
    expect(api.query.mock.calls[1][0]).toEqual({ ...api.query.mock.calls[0][0], page: { limit: 25, cursor: 'related-cursor' } })
    fireEvent.change(screen.getByLabelText('关联每页条数'), { target: { value: '10' } })
    expect((screen.getByRole('button', { name: '关联下一页' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('取消请求并忽略迟到结果，授权拒绝清空已经读取的数据', async () => {
    const { api, props } = fixture()
    const pending = deferred<QueryResult>()
    api.query.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(result).mockRejectedValueOnce(new ApiError(403, 'forbidden', '关系查询无权限'))
    render(<RelatedInstances {...props} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: '关联字段：标签名称' }))
    fireEvent.click(screen.getByRole('button', { name: '查询关联对象' }))
    const signal = api.query.mock.calls[0][1] as AbortSignal
    fireEvent.click(screen.getByRole('button', { name: '取消关联查询' }))
    expect(signal.aborted).toBe(true)
    await act(async () => pending.resolve(result))
    expect(screen.queryByText('知识图谱')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '查询关联对象' }))
    await screen.findByText('知识图谱')
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    await screen.findByText('关系查询无权限')
    expect(props.onForbidden).toHaveBeenCalledWith(expect.stringContaining('用户与权限'))
    expect(screen.queryByText('知识图谱')).toBeNull()
  })

  it('未连接关系数据时说明缺少映射，并提供配置入口', async () => {
    const unbound = { ...link, definition: { ...link.definition, link: { ...link.definition.link!, storageBinding: undefined } } }
    const { api, props } = fixture(out, unbound)
    render(<RelatedInstances {...props} />)
    await screen.findByText('此关系尚未连接关系数据')
    expect(screen.getByRole('link', { name: '配置“笔记标记标签”' }).getAttribute('href')).toBe('#models/note-tags')
    expect(screen.queryByRole('button', { name: '查询关联对象' })).toBeNull()
    expect(api.query).not.toHaveBeenCalled()
  })

  it('反向添加仍保存原始source/target方向，移除使用边的ETag，并提供边列表续页', async () => {
    const reverse: SchemaLink = { ...out, direction: 'in', name: '标记笔记', sourceSchemaId: 'tags', targetSchemaId: 'notes' }
    const { api, props } = fixture(reverse)
    api.query.mockResolvedValue({ ...result, fields: schema.definition.schema!.properties, data: [{ id: 'note-1', schemaId: 'notes', etag: 'note-current', values: { title: '关系数据库建模' } }] })
    render(<RelatedInstances {...props} schema={target} instanceId="tag-1" />)
    fireEvent.click(await screen.findByRole('checkbox', { name: '关联字段：标题' }))
    fireEvent.click(screen.getByRole('button', { name: '维护对象关系' }))
    fireEvent.click(screen.getByRole('button', { name: '读取可关联对象' }))
    await screen.findByLabelText('选择要关联的对象')
    expect(api.query.mock.calls[0][0]).toEqual({ from: { schemaId: 'notes' }, select: ['title'], page: { limit: 25 } })
    fireEvent.change(screen.getByLabelText('选择要关联的对象'), { target: { value: 'note-1' } })
    fireEvent.click(screen.getByRole('button', { name: '添加关系' }))
    await waitFor(() => expect(api.createLinkInstance).toHaveBeenCalledWith('note-tags', 'note-1', 'tag-1'))
    fireEvent.click(screen.getByRole('button', { name: '读取已有关系' }))
    await screen.findByRole('table', { name: '已有关系' })
    expect(api.listLinkInstances.mock.calls[0].slice(0, 4)).toEqual(['note-tags', 'in', 'tag-1', undefined])
    fireEvent.click(screen.getByRole('button', { name: '更多已有关系' }))
    await waitFor(() => expect(api.listLinkInstances).toHaveBeenCalledTimes(2))
    expect(api.listLinkInstances.mock.calls[1][3]).toBe('edges-cursor')
    fireEvent.click(screen.getByRole('button', { name: '移除关系' }))
    expect(api.deleteLinkInstance).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '确认移除关系' }))
    await waitFor(() => expect(api.deleteLinkInstance).toHaveBeenCalledWith('note-tags', 'edge-1', 'edge-current'))
  })

  it('外部数据库映射只开放关联查询，不能维护关系事实', async () => {
    const mapped = { ...link, definition: { ...link.definition, link: { ...link.definition.link!, storageBinding: { mode: 'junction' as const, dataSourceId: 'source', table: 'note_tags', sourceColumn: 'note_id', targetColumn: 'tag_id' } } } }
    const { props } = fixture(out, mapped)
    render(<RelatedInstances {...props} />)
    await screen.findByText('已有数据库映射 · 只读关系')
    expect(screen.queryByRole('button', { name: '维护对象关系' })).toBeNull()
  })

  it('写入未确认前禁用收起、关闭、切换关系及查询，成功后使旧关联结果失效', async () => {
    const { api, props } = fixture()
    const pending = deferred<{ id: string; etag: string }>()
    api.createLinkInstance.mockReturnValueOnce(pending.promise)
    render(<RelatedInstances {...props} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: '关联字段：标签名称' }))
    fireEvent.click(screen.getByRole('button', { name: '查询关联对象' }))
    await screen.findByText('知识图谱')
    fireEvent.click(screen.getByRole('button', { name: '维护对象关系' }))
    fireEvent.click(screen.getByRole('button', { name: '读取可关联对象' }))
    await screen.findByLabelText('选择要关联的对象')
    fireEvent.change(screen.getByLabelText('选择要关联的对象'), { target: { value: 'tag-1' } })
    fireEvent.click(screen.getByRole('button', { name: '添加关系' }))
    expect((screen.getByRole('button', { name: '收起关系维护' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '关闭对话框' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '查询关联对象' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByLabelText('查询关系') as HTMLSelectElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '收起关系维护' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(props.onClose).not.toHaveBeenCalled()
    await act(async () => pending.resolve({ id: 'new-edge', etag: 'new-current' }))
    await screen.findByText('关系已添加。请重新查询关联数据。')
    expect(screen.getByText('关联查询配置或数据已改变，请重新查询；续页已停用。')).toBeTruthy()
    expect((screen.getByRole('button', { name: '关联下一页' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '关闭对话框' }) as HTMLButtonElement).disabled).toBe(false)
  })
})
