import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DefinitionRecord, QueryResult, QuerySpec } from '../../lib/types'
import { ModelDataPreview } from './ModelDataPreview'

vi.mock('./ModelLinkPreview', () => ({ ModelLinkPreview: ({ instanceId, active }: { instanceId?: string; active: boolean }) => <section aria-label="关联预览测试">{active && instanceId ? instanceId : '没有选中记录'}</section> }))
afterEach(cleanup)

const current: DefinitionRecord = { etag: 'active', status: 'active', definition: {
  id: 'notes', name: '笔记', kind: 'schema', tenantId: 'tenant', categoryId: 'category', groupId: 'group',
  schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
    { id: 'origin', name: '来源键', dataType: 'string' },
    { id: 'title', name: '标题', dataType: 'text' },
    { id: 'amount', name: '测量值', dataType: 'decimal', precision: 24, scale: 4 },
    { id: 'body', name: '原始Markdown', dataType: 'longtext' },
  ] },
} }
const first: QueryResult = { fields: current.definition.schema!.properties.filter((property) => property.id === 'title' || property.id === 'amount'), data: [
  { id: 'note-1', schemaId: 'notes', etag: 'row-1', values: { title: '第一篇笔记', amount: '9007199254740993.1234' } },
  { id: 'note-2', schemaId: 'notes', etag: 'row-2', values: { title: '第二篇笔记', amount: null } },
], hasMore: true, nextCursor: 'second-page' }
function fixture() {
  const api = { getCurrent: vi.fn().mockResolvedValue(current), query: vi.fn<(spec: QuerySpec, signal?: AbortSignal) => Promise<QueryResult>>().mockResolvedValue(first) }
  const props = { api: api as unknown as ApiClient, schemaId: 'notes', active: true, hasCurrent: true, draft: false }
  return { api, props }
}
async function table() { return screen.findByRole('table', { name: '对象数据预览' }) }

describe('模型数据预览', () => {
  it('只在进入已发布对象的预览时读取当前定义，默认小页投影且长正文按需选择', async () => {
    const { api, props } = fixture()
    const view = render(<ModelDataPreview {...props} active={false} />)
    expect(api.getCurrent).not.toHaveBeenCalled()
    view.rerender(<ModelDataPreview {...props} hasCurrent={false} />)
    expect(screen.getByText('此对象尚未发布，请先发布模型后再预览数据。')).toBeTruthy()
    expect(api.getCurrent).not.toHaveBeenCalled()
    view.rerender(<ModelDataPreview {...props} draft />)
    await table()
    expect(api.getCurrent).toHaveBeenCalledWith('notes', expect.any(AbortSignal))
    expect(api.query).toHaveBeenCalledWith({ from: { schemaId: 'notes' }, select: ['title', 'origin', 'amount'], page: { limit: 10 } }, expect.any(AbortSignal))
    expect(screen.getByText(/草稿尚未发布；此处读取当前生效模型的数据/)).toBeTruthy()
    await userEvent.click(screen.getByText('预览字段', { exact: true }))
    expect((screen.getByLabelText('预览字段：原始Markdown') as HTMLInputElement).checked).toBe(false)
  })

  it('仅按返回字段渲染并保留精确数值，选择记录联动底部关联数据', async () => {
    const { props } = fixture()
    render(<ModelDataPreview {...props} />)
    const result = await table()
    expect(within(result).queryByRole('columnheader', { name: '来源键' })).toBeNull()
    expect(within(result).getByRole('cell', { name: '9007199254740993.1234' })).toBeTruthy()
    expect(within(result).getByRole('cell', { name: '空值 NULL' })).toBeTruthy()
    expect(screen.getByRole('region', { name: '关联预览测试' }).textContent).toBe('note-1')
    await userEvent.click(screen.getByRole('button', { name: '预览记录 note-2' }))
    expect(screen.getByRole('region', { name: '关联预览测试' }).textContent).toBe('note-2')
    expect(screen.getByRole('region', { name: '选中记录预览' }).textContent).toContain('第二篇笔记')
    expect(result.compareDocumentPosition(screen.getByRole('region', { name: '关联预览测试' })) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('下一页和上一页重用已执行投影与对应游标，更改字段清理分页和关联选择', async () => {
    const { api, props } = fixture()
    api.query.mockResolvedValueOnce(first).mockResolvedValueOnce({ ...first, data: [{ ...first.data[0], id: 'note-3' }], hasMore: false, nextCursor: undefined })
    render(<ModelDataPreview {...props} />)
    await table()
    await userEvent.click(screen.getByRole('button', { name: '预览下一页' }))
    await waitFor(() => expect(screen.getByRole('region', { name: '关联预览测试' }).textContent).toBe('note-3'))
    expect(api.query.mock.calls[1][0].page).toEqual({ limit: 10, cursor: 'second-page' })
    await userEvent.click(screen.getByRole('button', { name: '预览上一页' }))
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(3))
    expect(api.query.mock.calls[2][0].page).toEqual({ limit: 10 })
    await userEvent.click(screen.getByText('预览字段', { exact: true }))
    await userEvent.click(screen.getByLabelText('预览字段：原始Markdown'))
    expect(screen.queryByRole('table', { name: '对象数据预览' })).toBeNull()
    expect(screen.getByRole('region', { name: '关联预览测试' }).textContent).toBe('没有选中记录')
    await userEvent.click(screen.getByRole('button', { name: '加载预览' }))
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(4))
    expect(api.query.mock.calls[3][0].select).toContain('body')
    expect(api.query.mock.calls[3][0].page).toEqual({ limit: 10 })
  })

  it('刷新后授权拒绝清理旧数据和关联，不静默重试其他字段', async () => {
    const { api, props } = fixture()
    api.query.mockResolvedValueOnce(first).mockRejectedValueOnce(new ApiError(403, 'forbidden_field', '无权读取该属性'))
    render(<ModelDataPreview {...props} />)
    await table()
    await userEvent.click(screen.getByRole('button', { name: '刷新预览' }))
    expect((await screen.findByRole('alert')).textContent).toContain('无权读取该属性')
    expect(screen.queryByText('第一篇笔记')).toBeNull()
    expect(screen.getByRole('region', { name: '关联预览测试' }).textContent).toBe('没有选中记录')
    expect(api.query).toHaveBeenCalledTimes(2)
    expect(screen.getByText('预览字段', { exact: true })).toBeTruthy()
  })

  it('当前模型读取失败后可在原页签重试，并重新读取定义和数据', async () => {
    const { api, props } = fixture()
    api.getCurrent.mockRejectedValueOnce(new ApiError(503, 'unavailable', '模型读取暂时不可用'))
    render(<ModelDataPreview {...props} />)
    expect((await screen.findByRole('alert')).textContent).toContain('模型读取暂时不可用')
    expect(api.query).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '重新读取模型' }))
    await table()
    expect(api.getCurrent).toHaveBeenCalledTimes(2)
    expect(api.getCurrent).toHaveBeenLastCalledWith('notes', expect.any(AbortSignal))
    expect(api.query).toHaveBeenCalledExactlyOnceWith({ from: { schemaId: 'notes' }, select: ['title', 'origin', 'amount'], page: { limit: 10 } }, expect.any(AbortSignal))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('button', { name: '重新读取模型' })).toBeNull()
  })

  it('取消读取后忽略迟到结果，重新加载仍使用当前配置', async () => {
    const { api, props } = fixture()
    let finish!: (result: QueryResult) => void
    api.query.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    render(<ModelDataPreview {...props} />)
    await screen.findByRole('button', { name: '取消预览' })
    await userEvent.click(screen.getByRole('button', { name: '取消预览' }))
    expect(api.query.mock.calls[0][1]?.aborted).toBe(true)
    await act(async () => finish(first))
    expect(screen.queryByRole('table', { name: '对象数据预览' })).toBeNull()
    expect(screen.getByText('数据预览已取消。')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '加载预览' }))
    await table()
    expect(api.query).toHaveBeenCalledTimes(2)
  })

  it('离开页签后取消请求，迟到数据不会在重进时出现', async () => {
    const { api, props } = fixture()
    let finish!: (result: QueryResult) => void
    api.query.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const view = render(<ModelDataPreview {...props} />)
    await waitFor(() => expect(api.query).toHaveBeenCalledTimes(1))
    view.rerender(<ModelDataPreview {...props} active={false} />)
    expect(api.query.mock.calls[0][1]?.aborted).toBe(true)
    await act(async () => finish(first))
    expect(screen.queryByText('第一篇笔记')).toBeNull()
    view.rerender(<ModelDataPreview {...props} />)
    await table()
    expect(api.getCurrent).toHaveBeenCalledTimes(2)
  })

  it('切换对象时旧定义响应不会被用于新对象查询', async () => {
    const { api, props } = fixture()
    let finish!: (result: DefinitionRecord) => void
    api.getCurrent.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    api.getCurrent.mockResolvedValueOnce({ ...current, definition: { ...current.definition, id: 'other-notes' } })
    const view = render(<ModelDataPreview {...props} />)
    view.rerender(<ModelDataPreview {...props} schemaId="other-notes" />)
    await table()
    await act(async () => finish(current))
    expect(api.query).toHaveBeenCalledTimes(1)
    expect(api.query.mock.calls[0][0].from.schemaId).toBe('other-notes')
  })

  it('游标失效清空旧结果并提示从第一页重新读取', async () => {
    const { api, props } = fixture()
    api.query.mockResolvedValueOnce(first).mockRejectedValueOnce(new ApiError(422, 'invalid_cursor', '游标已失效'))
    render(<ModelDataPreview {...props} />)
    await table()
    await userEvent.click(screen.getByRole('button', { name: '预览下一页' }))
    await screen.findByRole('alert')
    expect(screen.getByText('分页已失效，请刷新预览，从第一页重新读取。')).toBeTruthy()
    expect(screen.queryByRole('table', { name: '对象数据预览' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '加载预览' }))
    await table()
    expect(api.query.mock.calls[2][0].page).toEqual({ limit: 10 })
  })
})
