import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiClient, ApiError } from '../../lib/api'
import type { BulkWriteRequest, WriteCapabilities, WriteOperation } from '../../lib/types'
import { ModelWriteOperations, WriteOperationDialog, writeOperationLabels } from './ModelWriteOperations'

afterEach(cleanup)
const names = Object.keys(writeOperationLabels) as WriteOperation[]
function capabilities(): WriteCapabilities {
  return { schemaId: 'orders', operations: names.map((name) => ({ name, allowed: true, writeFields: ['title', 'amount'] })),
    fields: [{ id: 'title', name: '标题', dataType: 'string' }, { id: 'amount', name: '金额', dataType: 'decimal', precision: 30, scale: 4 }],
    matchModes: ['id', 'unique_key'], uniqueKeys: [{ id: 'by-title', name: '标题唯一键', fieldIds: ['title'] }],
    overwriteAllowed: false, maxBulkItems: 2, maxRequestBytes: 1024, maxResponseBytes: 1024 }
}
function fixture(operation: WriteOperation, overrides = {}) {
  const api = { getInstance: vi.fn().mockImplementation(async (_schema: string, id: string) => ({ schemaId: 'orders', id, etag: `etag-${id}`, values: { title: id } })),
    bulkWrite: vi.fn().mockImplementation(async (_schema: string, _operation: string, input: BulkWriteRequest) => ({ schemaId: 'orders', atomic: true, committed: true,
      results: input.items.map((item) => ({ schemaId: 'orders', id: item.id ?? 'new', clientKey: item.clientKey, outcome: operation === 'bulk_update' ? 'updated' : 'created' })) })),
    upsertInstance: vi.fn().mockResolvedValue({ schemaId: 'orders', id: 'one', outcome: 'updated' }), ...overrides }
  const onClose = vi.fn(), onDenied = vi.fn()
  render(<WriteOperationDialog api={api as unknown as ApiClient} capabilities={capabilities()} operation={operation} intents={new Map()} onClose={onClose} onDenied={onDenied} />)
  return { api, onClose, onDenied }
}
async function fillField(scope: ReturnType<typeof within>, name: string, text: string) {
  await userEvent.selectOptions(scope.getByLabelText(`${name}的写入方式`), 'value')
  await userEvent.clear(scope.getByLabelText(name, { exact: true }))
  await userEvent.type(scope.getByLabelText(name, { exact: true }), text)
}

describe('对象八个内置写操作', () => {
  it('未发布不读取，已发布展示八项操作和中文不可用原因', async () => {
    const caps = capabilities(); caps.operations[2] = { name: 'delete', allowed: false, writeFields: [], reason: 'mapped_write_scope_unsupported' }
    const api = { writeCapabilities: vi.fn().mockResolvedValue(caps) }
    const view = render(<ModelWriteOperations api={api as unknown as ApiClient} schemaId="orders" active hasCurrent={false} />)
    expect(api.writeCapabilities).not.toHaveBeenCalled()
    view.rerender(<ModelWriteOperations api={api as unknown as ApiClient} schemaId="orders" active hasCurrent />)
    await screen.findByText('此来源的租户隔离配置尚不支持写入')
    expect(screen.getAllByRole('article')).toHaveLength(8)
    expect((screen.getByRole('button', { name: '删除数据' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.queryByText('mapped_write_scope_unsupported')).toBeNull()
  })

  it('切换对象后丢弃迟到的写入能力', async () => {
    let finish!: (caps: WriteCapabilities) => void
    const api = { writeCapabilities: vi.fn().mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockRejectedValueOnce(new Error('新对象不可用')) }
    const view = render(<ModelWriteOperations api={api as unknown as ApiClient} schemaId="orders" active hasCurrent />)
    view.rerender(<ModelWriteOperations api={api as unknown as ApiClient} schemaId="other" active hasCurrent />)
    await screen.findByText('新对象不可用')
    await act(async () => finish(capabilities()))
    expect((screen.getByRole('button', { name: '创建数据' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('覆盖权限更窄时仅收窄覆盖字段及唯一键，旧能力响应关闭覆盖选项', async () => {
    const caps = capabilities(); caps.overwriteAllowed = true
    const props = { api: {} as ApiClient, capabilities: caps, operation: 'upsert' as const, intents: new Map<string, string>(), onClose: vi.fn(), onDenied: vi.fn() }
    const view = render(<WriteOperationDialog {...props} />)
    expect(screen.queryByRole('option', { name: '同步覆盖提交字段' })).toBeNull()
    view.rerender(<WriteOperationDialog {...props} capabilities={{ ...caps, overwriteWriteFields: ['title'], overwriteUniqueKeyIds: [] }} />)
    expect(screen.getByLabelText('金额的写入方式')).toBeTruthy()
    await userEvent.selectOptions(screen.getByLabelText('匹配方式'), 'by-title')
    await userEvent.selectOptions(screen.getByLabelText('并发策略'), 'overwrite')
    expect(screen.queryByLabelText('金额的写入方式')).toBeNull()
    expect(screen.queryByRole('option', { name: '标题唯一键' })).toBeNull()
    expect((screen.getByLabelText('匹配方式') as HTMLSelectElement).value).toBe('id')
    await userEvent.selectOptions(screen.getByLabelText('并发策略'), 'check')
    expect(screen.getByLabelText('金额的写入方式')).toBeTruthy()
    expect(screen.getByRole('option', { name: '标题唯一键' })).toBeTruthy()
  })

  it('业务唯一键匹配可读取已有实例并携带强 ETag，精确小数保持字符串', async () => {
    const { api } = fixture('upsert')
    await userEvent.selectOptions(screen.getByLabelText('匹配方式'), 'by-title')
    await userEvent.type(within(screen.getByRole('group', { name: '匹配键' })).getByLabelText('标题'), 'one')
    await userEvent.type(screen.getByLabelText('已有实例 ID（校验更新时填写）'), 'one')
    await userEvent.click(screen.getByRole('button', { name: '读取记录' }))
    await screen.findByText(/已读取当前记录/)
    await fillField(within(screen.getByRole('region', { name: '写入记录 1' })), '金额', '9007199254740993.0001')
    await userEvent.click(screen.getByRole('button', { name: '提交' }))
    await waitFor(() => expect(api.upsertInstance).toHaveBeenCalledWith('orders', expect.objectContaining({ match: { indexId: 'by-title', values: { title: 'one' } },
      conflictPolicy: 'check', values: { amount: '9007199254740993.0001' }, idempotencyKey: expect.any(String) }), '"etag-one"', expect.any(AbortSignal)))
  })

  it('批量更新先读取每条记录，一次请求携带逐项强 ETag', async () => {
    const { api } = fixture('bulk_update')
    await userEvent.click(screen.getByRole('button', { name: /添加记录/ }))
    for (const [index, row] of screen.getAllByRole('region', { name: /写入记录/ }).entries()) {
      const scope = within(row)
      await userEvent.type(scope.getByLabelText('实例 ID'), `row${index}`)
      await userEvent.click(scope.getByRole('button', { name: '读取记录' }))
      await waitFor(() => expect(scope.queryByText(/已读取当前记录/)).toBeTruthy())
      await fillField(scope, '标题', `new${index}`)
    }
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    await screen.findByText('整批已提交，共 2 条。')
    expect(api.bulkWrite).toHaveBeenCalledTimes(1)
    expect(api.bulkWrite.mock.calls[0][2]).toMatchObject({ atomic: true, items: [
      { id: 'row0', ifMatch: '"etag-row0"', values: { title: 'new0' } }, { id: 'row1', ifMatch: '"etag-row1"', values: { title: 'new1' } },
    ] })
  })

  it('修改已读实例 ID 会清除凭证并阻止提交', async () => {
    const { api } = fixture('bulk_update')
    await userEvent.type(screen.getByLabelText('实例 ID'), 'one')
    await userEvent.click(screen.getByRole('button', { name: '读取记录' }))
    await screen.findByText(/已读取当前记录/)
    await userEvent.type(screen.getByLabelText('实例 ID'), '-other')
    await fillField(within(screen.getByRole('region', { name: '写入记录 1' })), '标题', 'new')
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', '请先读取每条目标记录，再基于已读取的内容提交。')
    expect(api.bulkWrite).not.toHaveBeenCalled()
  })

  it('未知结果锁定内容并使用原请求重试，拒绝不对应的回执', async () => {
    const bulkWrite = vi.fn().mockResolvedValueOnce({ schemaId: 'other', atomic: true, committed: true, results: [] })
      .mockImplementationOnce(async (_schema: string, _operation: string, input: BulkWriteRequest) => ({ schemaId: 'orders', atomic: true, committed: true,
        results: input.items.map((item) => ({ schemaId: 'orders', id: 'new', clientKey: item.clientKey, outcome: 'created' })) }))
    fixture('bulk_create', { bulkWrite })
    await fillField(within(screen.getByRole('region', { name: '写入记录 1' })), '标题', 'retry')
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    await screen.findByText(/提交结果尚未确认/)
    expect((screen.getByLabelText('标题', { exact: true }) as HTMLInputElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    await screen.findByText('整批已提交，共 1 条。')
    expect(bulkWrite.mock.calls[0][2]).toEqual(bulkWrite.mock.calls[1][2])
  })

  it('原子失败保留内容，权限撤销清理记录并通知调用方', async () => {
    const bulkWrite = vi.fn().mockRejectedValueOnce(new ApiError(409, 'conflict', '整批已回滚')).mockRejectedValueOnce(new ApiError(403, 'forbidden', '权限已撤销'))
    const { onDenied } = fixture('bulk_create', { bulkWrite })
    await fillField(within(screen.getByRole('region', { name: '写入记录 1' })), '标题', 'keep')
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    await screen.findByText('整批已回滚')
    expect((screen.getByLabelText('标题', { exact: true }) as HTMLInputElement).value).toBe('keep')
    await userEvent.click(screen.getByRole('button', { name: '原子提交整批' }))
    await waitFor(() => expect(onDenied).toHaveBeenCalledWith('权限已撤销'))
    expect(screen.queryByLabelText('标题', { exact: true })).toBeNull()
  })
})
