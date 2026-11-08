import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, QueryResult } from '../../lib/types'
import { LinkDataPreview } from './LinkDataPreview'

const base = { tenantId: 'tenant', categoryId: 'knowledge', groupId: 'group' }
const schemas: DefinitionRecord[] = [
  { status: 'active', etag: 'notes-current', definition: { ...base, id: 'notes', kind: 'schema', name: '笔记', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'body', name: '正文', dataType: 'longtext' }, { id: 'title', name: '标题', dataType: 'string' }] } } },
  { status: 'active', etag: 'refs-current', definition: { ...base, id: 'refs', kind: 'schema', name: '引用记录', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'text', name: '引用内容', dataType: 'string' }] } } },
]
const current: DefinitionRecord = { status: 'active', etag: 'link-current', definition: { ...base, id: 'note-refs', kind: 'link', name: '笔记产生引用', link: { sourceSchemaId: 'notes', targetSchemaId: 'refs', forwardName: '产生引用', reverseName: '源自笔记', cardinality: '1:N', onDelete: 'restrict', storageBinding: { mode: 'foreign_key', dataSourceId: 'source', foreignKeySide: 'target', foreignKeyColumn: 'note_id' } } } }
const origin: QueryResult = { fields: [schemas[0].definition.schema!.properties[1]], data: [{ id: 'note-1', schemaId: 'notes', etag: 'row-current', values: { title: 'Ontology概念' } }], hasMore: false }
const related: QueryResult = { fields: schemas[1].definition.schema!.properties, data: [{ id: 'ref-1', schemaId: 'refs', etag: 'ref-current', values: { text: '关系建模' } }], hasMore: true, nextCursor: 'related-next' }
function fixture(query = vi.fn().mockResolvedValue(origin)) {
  const api = { query } as unknown as ApiClient
  return { query, api }
}
async function selectOrigin() {
  fireEvent.click(screen.getByRole('button', { name: '读取起点记录' }))
  await screen.findByRole('option', { name: /Ontology概念/ })
  fireEvent.change(screen.getByLabelText('起点记录'), { target: { value: 'note-1' } })
}

describe('关系当前态数据预览', () => {
  it('手动读取小页起点，默认避开longtext，再用当前Link执行带起点的正向查询与续页', async () => {
    const { api, query } = fixture(vi.fn().mockResolvedValueOnce(origin).mockResolvedValue(related))
    render(<LinkDataPreview api={api} current={current} schemas={schemas} draft />)
    expect(query).not.toHaveBeenCalled()
    expect(screen.getByText(/草稿尚未发布/)).toBeTruthy()
    expect(screen.getByText(/此外部关系只读/)).toBeTruthy()
    await selectOrigin()
    expect(query).toHaveBeenNthCalledWith(1, { from: { schemaId: 'notes' }, select: ['title'], page: { limit: 10 } }, expect.any(AbortSignal))
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await screen.findByRole('cell', { name: '关系建模' })
    expect(query).toHaveBeenNthCalledWith(2, { from: { schemaId: 'refs' }, via: { linkId: 'note-refs', direction: 'out', instanceId: 'note-1' }, select: ['text'], page: { limit: 5 } }, expect.any(AbortSignal))
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    await waitFor(() => expect(query).toHaveBeenCalledTimes(3))
    expect(query.mock.calls[2][0].page).toEqual({ limit: 5, cursor: 'related-next' })
  })

  it('切换方向清除起点和旧结果，反向起点来自目标对象', async () => {
    const refs: QueryResult = { ...related, hasMore: false }
    const { api, query } = fixture(vi.fn().mockResolvedValueOnce(origin).mockResolvedValueOnce(related).mockResolvedValueOnce(refs).mockResolvedValue(origin))
    render(<LinkDataPreview api={api} current={current} schemas={schemas} draft={false} />)
    await selectOrigin()
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await screen.findByRole('table', { name: '关系数据预览' })
    fireEvent.change(screen.getByLabelText('查询方向'), { target: { value: 'in' } })
    expect(screen.queryByRole('table', { name: '关系数据预览' })).toBeNull()
    expect(screen.queryByLabelText('起点记录')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '读取起点记录' }))
    await screen.findByRole('option', { name: /关系建模/ })
    fireEvent.change(screen.getByLabelText('起点记录'), { target: { value: 'ref-1' } })
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await waitFor(() => expect(query).toHaveBeenCalledTimes(4))
    expect(query.mock.calls[3][0]).toMatchObject({ from: { schemaId: 'notes' }, via: { linkId: 'note-refs', direction: 'in', instanceId: 'ref-1' }, select: ['title'] })
  })

  it('取消和切方向使迟到响应无效，端点目录撤权卸载并清除结果', async () => {
    let resolve!: (result: QueryResult) => void
    const delayed = new Promise<QueryResult>((done) => { resolve = done })
    const { api, query } = fixture(vi.fn().mockResolvedValueOnce(origin).mockReturnValueOnce(delayed).mockResolvedValue(origin))
    const view = render(<LinkDataPreview api={api} current={current} schemas={schemas} draft={false} />)
    await selectOrigin()
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    const signal = query.mock.calls[1][1] as AbortSignal
    fireEvent.click(screen.getByRole('button', { name: '取消读取' }))
    expect(signal.aborted).toBe(true)
    fireEvent.change(screen.getByLabelText('查询方向'), { target: { value: 'in' } })
    await act(async () => resolve(related))
    expect(screen.queryByRole('table', { name: '关系数据预览' })).toBeNull()
    view.rerender(<LinkDataPreview api={api} current={current} schemas={[schemas[0]]} draft={false} />)
    expect(screen.getByText(/端点的当前定义不可见/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: '读取起点记录' })).toBeNull()
  })

  it('403清理旧关联结果与起点，游标失效不沿用旧页', async () => {
    const { api, query } = fixture(vi.fn().mockResolvedValueOnce(origin).mockResolvedValueOnce(related).mockRejectedValueOnce(new ApiError(422, 'invalid_cursor', '关联内容已改变')).mockResolvedValueOnce(related).mockRejectedValueOnce(new ApiError(403, 'forbidden', '没有查询权限')))
    render(<LinkDataPreview api={api} current={current} schemas={schemas} draft={false} />)
    await selectOrigin()
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await screen.findByRole('table', { name: '关系数据预览' })
    fireEvent.click(screen.getByRole('button', { name: '关联下一页' }))
    await screen.findByText(/旧游标已失效/)
    expect(screen.queryByRole('table', { name: '关系数据预览' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await screen.findByRole('table', { name: '关系数据预览' })
    expect(query.mock.calls[3][0].page).toEqual({ limit: 5 })
    fireEvent.click(screen.getByRole('button', { name: '查询关联记录' }))
    await screen.findByRole('alert')
    expect(screen.queryByRole('table', { name: '关系数据预览' })).toBeNull()
    expect(screen.queryByLabelText('起点记录')).toBeNull()
  })

  it('未生效、未绑定或缺少当前端点时不访问业务数据', () => {
    const { api, query } = fixture()
    const view = render(<LinkDataPreview api={api} schemas={schemas} draft />)
    expect(screen.getByText(/尚未生效/)).toBeTruthy()
    view.rerender(<LinkDataPreview api={api} current={{ ...current, definition: { ...current.definition, link: { ...current.definition.link!, storageBinding: undefined } } }} schemas={schemas} draft />)
    expect(screen.getByText(/当前只有关系定义/)).toBeTruthy()
    view.rerender(<LinkDataPreview api={api} current={current} schemas={[{ ...schemas[0], definition: { ...schemas[0].definition, tenantId: 'other-tenant' } }, schemas[1]]} draft={false} />)
    expect(screen.getByText(/端点的当前定义不可见/)).toBeTruthy()
    expect(query).not.toHaveBeenCalled()
  })
})
