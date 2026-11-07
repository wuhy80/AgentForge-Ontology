import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, QueryResult, SchemaLink } from '../../lib/types'
import { ModelLinkPreview } from './ModelLinkPreview'

afterEach(cleanup)
const notes: DefinitionRecord = { status: 'active', etag: 'notes-current', definition: {
  id: 'notes', name: '笔记', tenantId: 'tenant', categoryId: 'knowledge', groupId: 'group', kind: 'schema',
  schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'title', name: '标题', dataType: 'string' }] },
} }
const tags: DefinitionRecord = { ...notes, etag: 'tags-current', definition: { ...notes.definition, id: 'tags', name: '标签', schema: { ...notes.definition.schema!, properties: [
  { id: 'label', name: '标签名称', dataType: 'string' }, { id: 'secret', name: '内部说明', dataType: 'string' }, { id: 'body', name: '标签正文', dataType: 'longtext' },
] } } }
const link: DefinitionRecord = { status: 'active', etag: 'link-current', definition: { ...notes.definition, id: 'note-tags', kind: 'link', name: '笔记标签', schema: undefined,
  link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'managed' } },
} }
const out: SchemaLink = { linkRef: { id: 'note-tags' }, direction: 'out', sourceSchemaId: 'notes', targetSchemaId: 'tags', name: '标记标签', cardinality: 'N:M' }
const response: QueryResult = { data: [{ id: 'tag-1', schemaId: 'tags', etag: 'instance-current', values: { label: '知识图谱' } }], fields: [tags.definition.schema!.properties[0]], hasMore: true, nextCursor: 'next-tag-page' }
function pending<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((yes) => { resolve = yes })
  return { promise, resolve }
}
function fixture(navigation = out, relation = link) {
  const api = {
    schemaLinks: vi.fn().mockResolvedValue([navigation]),
    getCurrent: vi.fn().mockImplementation(async (id: string) => id === 'note-tags' ? relation : id === 'tags' ? tags : notes),
    query: vi.fn().mockResolvedValue(response),
  }
  const props = { api: api as unknown as ApiClient, schemaId: navigation.sourceSchemaId, instanceId: 'note-1', active: true }
  return { api, props }
}

describe('模型数据预览底部 Link 数据', () => {
  it('首次读取真实正向数据，分页保留起点、方向和字段投影', async () => {
    const { api, props } = fixture()
    render(<ModelLinkPreview {...props} />)
    await screen.findByRole('cell', { name: '知识图谱' })
    expect(api.query.mock.calls[0][0]).toEqual({ from: { schemaId: 'tags' }, via: { linkId: 'note-tags', direction: 'out', instanceId: 'note-1' }, select: ['label', 'secret'], page: { limit: 5 } })
    const table = screen.getByRole('table', { name: '标记标签关联数据' })
    expect(within(table).queryByRole('columnheader', { name: '内部说明' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(2))
    expect(api.query.mock.calls[1][0]).toEqual({ ...api.query.mock.calls[0][0], page: { limit: 5, cursor: 'next-tag-page' } })
  })

  it('反向导航仍以返回端对象为查询来源', async () => {
    const navigation: SchemaLink = { ...out, direction: 'in', sourceSchemaId: 'tags', targetSchemaId: 'notes', name: '标记笔记' }
    const { api, props } = fixture(navigation)
    api.query.mockResolvedValue({ fields: notes.definition.schema!.properties, data: [{ id: 'note-2', schemaId: 'notes', etag: 'note', values: { title: '数据库建模' } }], hasMore: false })
    render(<ModelLinkPreview {...props} instanceId="tag-1" />)
    await screen.findByRole('cell', { name: '数据库建模' })
    expect(api.query.mock.calls[0][0]).toEqual({ from: { schemaId: 'notes' }, via: { linkId: 'note-tags', direction: 'in', instanceId: 'tag-1' }, select: ['title'], page: { limit: 5 } })
    expect(screen.getByText('反向 · N:M')).toBeTruthy()
  })

  it('未绑定的关系展示配置入口，不伪装成空关联或触发实例查询', async () => {
    const unbound = { ...link, definition: { ...link.definition, link: { ...link.definition.link!, storageBinding: undefined } } }
    const { api, props } = fixture(out, unbound)
    render(<ModelLinkPreview {...props} />)
    await screen.findByText('尚未配置关系数据映射')
    expect(screen.getByRole('link', { name: '配置“笔记标签”' }).getAttribute('href')).toBe('#models/note-tags')
    expect(api.query).not.toHaveBeenCalled()
    expect(screen.queryByText('此记录没有可见的关联数据。')).toBeNull()
  })

  it('切换记录或退出 Tab 取消旧查询并忽略迟到数据', async () => {
    const { api, props } = fixture()
    const old = pending<QueryResult>()
    api.query.mockReturnValueOnce(old.promise).mockResolvedValueOnce({ ...response, data: [{ ...response.data[0], values: { label: '新记录关联' } }] })
    const view = render(<ModelLinkPreview {...props} />)
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(1))
    const oldSignal = api.query.mock.calls[0][1] as AbortSignal
    view.rerender(<ModelLinkPreview {...props} instanceId="note-2" />)
    await screen.findByRole('cell', { name: '新记录关联' })
    expect(oldSignal.aborted).toBe(true)
    expect(api.query.mock.calls[1][0].via.instanceId).toBe('note-2')
    await act(async () => old.resolve(response))
    expect(screen.queryByText('知识图谱')).toBeNull()
    view.rerender(<ModelLinkPreview {...props} active={false} />)
    expect(screen.queryByText('新记录关联')).toBeNull()
  })

  it('授权拒绝清除旧结果，允许选择已授权字段后重试', async () => {
    const { api, props } = fixture()
    api.query.mockResolvedValueOnce(response).mockRejectedValueOnce(new ApiError(403, 'forbidden_field', '无权读取内部说明')).mockResolvedValueOnce(response)
    render(<ModelLinkPreview {...props} />)
    await screen.findByRole('cell', { name: '知识图谱' })
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    expect((await screen.findByRole('alert')).textContent).toContain('无权读取内部说明')
    expect(screen.queryByText('知识图谱')).toBeNull()
    fireEvent.click(screen.getByText('关联返回字段（已选 2 项）'))
    fireEvent.click(screen.getByLabelText('标记标签返回字段：内部说明'))
    fireEvent.click(screen.getByRole('button', { name: '应用关联字段' }))
    await screen.findByRole('cell', { name: '知识图谱' })
    expect(api.query.mock.calls[2][0].select).toEqual(['label'])
    expect(api.query.mock.calls[2][0].page).toEqual({ limit: 5 })
  })

  it('无记录、无关系与空关系结果分别说明，inactive时不发请求', async () => {
    const { api, props } = fixture()
    const view = render(<ModelLinkPreview {...props} active={false} />)
    expect(api.schemaLinks).not.toHaveBeenCalled()
    view.rerender(<ModelLinkPreview {...props} instanceId={undefined} />)
    expect(screen.getByText('选择上方记录后，在这里预览它的关联数据。')).toBeTruthy()
    expect(api.schemaLinks).not.toHaveBeenCalled()
    api.schemaLinks.mockResolvedValueOnce([])
    view.rerender(<ModelLinkPreview {...props} />)
    await screen.findByText('当前对象没有可见的生效 Link。')
    fireEvent.click(screen.getByRole('button', { name: '刷新关联目录' }))
    api.query.mockResolvedValue({ ...response, data: [], hasMore: false })
    await screen.findByText('此记录没有可见的关联数据。')
  })

  it('按四项分批读取关系，防止对整个目录同时发起查询', async () => {
    const { api, props } = fixture()
    const items = Array.from({ length: 9 }, (_, index) => ({ ...out, linkRef: { id: `link-${index}` }, name: `关系${index}` }))
    api.schemaLinks.mockResolvedValue(items)
    api.getCurrent.mockImplementation(async (id: string) => id === 'tags' ? tags : { ...link, definition: { ...link.definition, id } })
    render(<ModelLinkPreview {...props} />)
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(4))
    fireEvent.click(screen.getByRole('button', { name: '显示更多关系（剩余 5 项）' }))
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(8))
  })

  it('StrictMode重放 effect 后仍会完成关联读取', async () => {
    const { api, props } = fixture()
    render(<StrictMode><ModelLinkPreview {...props} /></StrictMode>)
    await screen.findByRole('cell', { name: '知识图谱' })
    expect((api.query.mock.calls.at(-1)![1] as AbortSignal).aborted).toBe(false)
  })
})
