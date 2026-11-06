import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Publication } from '../../lib/types'
import { ModelsPage } from './ModelsPage'

afterEach(cleanup)
const categories = [{ id: 'category', tenantId: 'tenant', name: '生产管理' }]
const groups = [{ id: 'group', tenantId: 'tenant', categoryId: 'category', name: '基础资料' }]
function record(status = 'draft', etag = 'saved-1'): DefinitionRecord {
  return { etag, status, hasCurrent: status === 'active', definition: {
    id: 'equipment', tenantId: 'tenant', kind: 'schema', categoryId: 'category', groupId: 'group', name: '生产设备',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name', name: '设备名称', dataType: 'string' }], indexes: [] },
  } }
}
function client(initial = record()) {
  return {
    listGroups: vi.fn().mockResolvedValue(groups), getDraft: vi.fn().mockResolvedValue(initial),
    getCurrent: vi.fn().mockResolvedValue(initial), createCategory: vi.fn(), createGroup: vi.fn(),
    createDefinition: vi.fn(), saveDraft: vi.fn(), preview: vi.fn().mockResolvedValue({ operationId: 'preview', status: 'preview', statements: ['CREATE TABLE test (...)'] }),
    publish: vi.fn(), getPublication: vi.fn(),
  }
}
function mount(api: ReturnType<typeof client>, entries = [record()]) {
  const refresh = vi.fn().mockResolvedValue(undefined)
  render(<ModelsPage api={api as unknown as ApiClient} categories={categories} entries={entries} onRefresh={refresh} onQuery={vi.fn()} />)
  return refresh
}
async function edit() {
  await userEvent.click(screen.getByRole('button', { name: '编辑' }))
  return screen.findByRole('dialog')
}

describe('模型工作流', () => {
  it('未保存编辑不能发布，保存后以服务器返回的新 etag 发布', async () => {
    const api = client()
    mount(api)
    const dialog = await edit()
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '设备档案')
    expect((within(dialog).getByRole('button', { name: /发布定义/ }) as HTMLButtonElement).disabled).toBe(true)
    const saved = record('draft', 'saved-2'); saved.definition.name = '设备档案'
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({ name: '设备档案' }), 'saved-1'))
    api.publish.mockResolvedValue({ id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'succeeded' })
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await waitFor(() => expect(api.publish).toHaveBeenCalledWith('equipment', 'saved-2'))
    await screen.findByText('发布成功，当前定义已生效。')
  })

  it('503 关闭编辑器后仍可恢复相同内容和 etag', async () => {
    const api = client()
    const publication: Publication = { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'activation_pending' }
    api.publish.mockRejectedValueOnce(new ApiError(503, 'activation_pending', '等待激活', publication)).mockResolvedValueOnce({ ...publication, status: 'succeeded' })
    mount(api)
    let dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    expect((within(dialog).getByLabelText('业务名称') as HTMLInputElement).closest('fieldset')?.disabled).toBe(true)
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待激活.*恢复发布/ }))
    dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /继续恢复发布/ }))
    await within(dialog).findByText('发布成功，当前定义已生效。')
    expect(api.publish.mock.calls).toEqual([['equipment', 'saved-1'], ['equipment', 'saved-1']])
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })

  it('网络响应丢失也保留可重试的发布状态', async () => {
    const api = client()
    api.publish.mockRejectedValue(new ApiError(0, 'network_error', '连接中断'))
    mount(api)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    expect(within(dialog).getByRole('button', { name: /继续恢复发布/ })).toBeTruthy()
  })

  it('412 保留本地编辑且已生效模型的结构保持锁定', async () => {
    const initial = record('active')
    const api = client(initial)
    api.saveDraft.mockRejectedValue(new ApiError(412, 'precondition_failed', '内容已改变'))
    mount(api, [initial])
    const dialog = await edit()
    expect((within(dialog).getByLabelText('对象属性第1项类型') as HTMLSelectElement).disabled).toBe(true)
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '本地设备名称')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await within(dialog).findByText(/服务器内容已改变，本地编辑仍然保留/)
    expect((name as HTMLInputElement).value).toBe('本地设备名称')
    expect((within(dialog).getByRole('button', { name: '保存草稿' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('新对象表单先保存属性，再以返回的稳定绑定建立索引', async () => {
    const api = client()
    mount(api, [])
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('业务名称'), '车间')
    await waitFor(() => expect(within(dialog).getByRole('option', { name: '基础资料' })).toBeTruthy())
    fireEvent.change(within(dialog).getByLabelText('分组'), { target: { value: 'group' } })
    await userEvent.type(within(dialog).getByLabelText('对象属性第1项名称'), '车间编码')
    expect((within(dialog).getByRole('button', { name: '添加索引' }) as HTMLButtonElement).disabled).toBe(true)
    const created = record(); created.definition.name = '车间'; created.definition.schema!.properties[0].name = '车间编码'
    api.createDefinition.mockResolvedValue(created)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect((within(dialog).getByRole('button', { name: '添加索引' }) as HTMLButtonElement).disabled).toBe(false))
    await userEvent.click(within(dialog).getByRole('button', { name: '添加索引' }))
    await userEvent.type(within(dialog).getByLabelText('索引1名称'), '编码唯一')
    fireEvent.change(within(dialog).getByLabelText('索引1第一个属性'), { target: { value: 'name' } })
    const saved = structuredClone(created); saved.etag = 'indexed'; saved.definition.schema!.indexes = [{ id: 'index', name: '编码唯一', unique: false, fieldIds: ['name'] }]
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({ schema: expect.objectContaining({ indexes: [expect.objectContaining({ fieldIds: ['name'] })] }) }), 'saved-1'))
  })

  it('对象属性和索引到达存储上限后停止添加，Schema 不提供枚举绑定入口', async () => {
    const initial = record()
    initial.definition.schema!.properties = Array.from({ length: 100 }, (_, index) => ({ id: `field-${index}`, name: `属性${index}`, dataType: 'int64' }))
    initial.definition.schema!.properties[0].dataType = 'string'
    initial.definition.schema!.indexes = Array.from({ length: 32 }, (_, index) => ({ id: `index-${index}`, name: `索引${index}`, unique: false, fieldIds: ['field-0'] }))
    const enumeration: DefinitionRecord = { etag: 'enum', status: 'active', hasCurrent: true, definition: { id: 'enum', tenantId: 'tenant', categoryId: 'category', groupId: 'group', name: '设备状态', kind: 'enum', enum: { values: [{ code: 'READY', label: '就绪' }] } } }
    const api = client(initial)
    mount(api, [initial, enumeration])
    await userEvent.click(screen.getByRole('button', { name: '生产设备' }))
    const dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByRole('button', { name: '添加属性' }) as HTMLButtonElement).disabled).toBe(true)
    expect((within(dialog).getByRole('button', { name: '添加索引' }) as HTMLButtonElement).disabled).toBe(true)
    expect(within(dialog).queryByLabelText('对象属性第1项枚举')).toBeNull()
    expect(within(dialog).getByText(/对象属性暂不支持绑定枚举/)).toBeTruthy()
  }, 15000)

  it('保存刷新完成后目录服从最新远端内容，不被本地副本长期覆盖', async () => {
    const api = client()
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const props = { api: api as unknown as ApiClient, categories, onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '我的草稿')
    const saved = record('draft', 'saved-2'); saved.definition.name = '我的草稿'
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await within(dialog).findByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    const external = record('draft', 'external'); external.definition.name = '其他协作者的新名称'
    view.rerender(<ModelsPage {...props} entries={[external]} />)
    expect(screen.getByRole('button', { name: '其他协作者的新名称' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '我的草稿' })).toBeNull()
  })

  it('目录刷新失败并清空授权目录时不残留乐观副本', async () => {
    const api = client()
    const onRefresh = vi.fn().mockRejectedValue(new Error('目录不可用'))
    const props = { api: api as unknown as ApiClient, categories, onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '仍保存在服务器的草稿')
    const saved = record('draft', 'saved-2'); saved.definition.name = '仍保存在服务器的草稿'
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await within(dialog).findByText('草稿已保存，目录刷新失败；可稍后刷新目录。')
    view.rerender(<ModelsPage {...props} entries={[]} />)
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    expect(screen.queryByRole('button', { name: '仍保存在服务器的草稿' })).toBeNull()
    expect(screen.getByText('从第一个业务对象开始')).toBeTruthy()
  })

  it('目录编辑授权被拒绝时关闭旧草稿和待恢复记录，但保留新建入口', async () => {
    const api = client()
    const initial = record()
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const props = { api: api as unknown as ApiClient, categories, onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[initial]} />)
    const publication = { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', publication))
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    view.rerender(<ModelsPage {...props} entries={[]} draftDirectoryDenied />)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    expect(screen.queryByText('生产设备')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    expect((within(await screen.findByRole('dialog')).getByLabelText('业务名称') as HTMLInputElement).value).toBe('')
  })

  it('重开待恢复记录先重新验证草稿授权，403 时清除旧缓存', async () => {
    const api = client()
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    mount(api)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    api.getDraft.mockRejectedValue(new ApiError(403, 'forbidden', '无权读取此草稿'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待确认.*恢复发布/ }))
    await screen.findByText('无权读取此草稿')
    expect(api.getDraft).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })

  it('已提交操作的草稿消失后仅以相同摘要的已授权当前态恢复', async () => {
    const api = client()
    const publication = { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'activation_pending' }
    api.publish.mockRejectedValueOnce(new ApiError(503, 'activation_pending', '等待激活', publication)).mockResolvedValueOnce({ ...publication, status: 'succeeded' })
    mount(api)
    let dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    api.getDraft.mockRejectedValue(new ApiError(404, 'not_found', '没有草稿'))
    api.getCurrent.mockResolvedValue(record('active'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待激活.*恢复发布/ }))
    dialog = await screen.findByRole('dialog')
    expect(api.getCurrent).toHaveBeenCalledWith('equipment')
    await userEvent.click(within(dialog).getByRole('button', { name: /继续恢复发布/ }))
    await within(dialog).findByText('发布成功，当前定义已生效。')
    expect(api.publish.mock.calls).toEqual([['equipment', 'saved-1'], ['equipment', 'saved-1']])
  })

  it('待恢复内容与已授权当前摘要不同则清理旧候选，禁止回填缓存', async () => {
    const api = client()
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    mount(api)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    await userEvent.click(within(dialog).getByRole('button', { name: '关闭' }))
    api.getDraft.mockRejectedValue(new ApiError(404, 'not_found', '没有草稿'))
    api.getCurrent.mockResolvedValue(record('active', 'different-current'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待确认.*恢复发布/ }))
    await screen.findByText(/待恢复内容已改变，已清除旧草稿缓存/)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })

  it('逐对象编辑授权撤销导致草稿被目录过滤时也清除已打开的私有候选', async () => {
    const initial = record('draft', 'private-draft'); initial.hasCurrent = true; initial.definition.name = '仅编辑者可见的草稿'
    const api = client(initial)
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    const props = { api: api as unknown as ApiClient, categories, onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[initial]} />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    const current = record('active', 'public-current')
    view.rerender(<ModelsPage {...props} entries={[current]} />)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByText('仅编辑者可见的草稿')).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    expect(screen.getByRole('button', { name: '生产设备' })).toBeTruthy()
  })

  it('目录撤权后迟到的发布响应不能重新创建私有候选缓存', async () => {
    const api = client()
    let rejectPublication!: (cause: unknown) => void
    api.publish.mockImplementation(() => new Promise((_, reject) => { rejectPublication = reject }))
    const props = { api: api as unknown as ApiClient, categories, onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await waitFor(() => expect(api.publish).toHaveBeenCalledTimes(1))
    view.rerender(<ModelsPage {...props} entries={[]} draftDirectoryDenied />)
    rejectPublication(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })
})
