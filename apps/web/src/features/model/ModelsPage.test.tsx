import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
    publish: vi.fn(), getPublication: vi.fn(), listDataSources: vi.fn().mockResolvedValue([]), listSourceTables: vi.fn().mockResolvedValue([]), inspectSourceTable: vi.fn(),
  }
}
function mount(api: ReturnType<typeof client>, entries = [record()]) {
  const refresh = vi.fn().mockResolvedValue(undefined)
  render(<ModelsPage api={api as unknown as ApiClient} categoryId='category' categories={categories} entries={entries} onRefresh={refresh} onQuery={vi.fn()} onOrganizations={vi.fn()} />)
  return refresh
}
async function edit() {
  await userEvent.click(screen.getByRole('button', { name: '编辑' }))
  return screen.findByRole('region', { name: '模型编辑' })
}

describe('模型工作流', () => {
  it('模型地址直接读取独立编辑页，返回列表更新路由', async () => {
    const api = client()
    const onNavigate = vi.fn()
    render(<ModelsPage api={api as unknown as ApiClient} categoryId="category" categories={categories} entries={[record()]} onRefresh={vi.fn().mockResolvedValue(undefined)} onQuery={vi.fn()} onOrganizations={vi.fn()} route="models/equipment" onNavigate={onNavigate} />)
    const editor = await screen.findByRole('region', { name: '模型编辑' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.queryByRole('table', { name: '按分组排列的业务模型' })).toBeNull()
    expect(api.getDraft).toHaveBeenCalledWith('equipment')
    await userEvent.click(within(editor).getByRole('button', { name: '返回模型列表' }))
    expect(onNavigate).toHaveBeenCalledWith('models')
  })

  it('跨数据源属性保存完整关联映射并要求允许空值', async () => {
    const initial = record()
    initial.definition.schema!.storageBinding = { dataSourceId: 'source-a', table: 'equipment', idColumn: 'id' }
    initial.definition.schema!.properties[0].mapping = { dataSourceId: 'source-a', column: 'name' }
    const api = client(initial)
    api.listDataSources.mockResolvedValue([
      { id: 'source-a', tenantId: 'tenant', name: '设备库', driver: 'mysql', database: 'equipment', enabled: true, etag: 'a', tenantIsolation: 'dedicated' },
      { id: 'source-b', tenantId: 'tenant', name: '资产库', driver: 'postgresql', database: 'assets', enabled: true, etag: 'b', tenantIsolation: 'column' },
    ])
    api.saveDraft.mockImplementation(async (_id, definition) => ({ ...initial, definition, etag: 'saved-2' }))
    mount(api, [initial])
    const editor = await edit()
    await within(within(editor).getByLabelText('属性1数据源')).findByRole('option', { name: '资产库 · PostgreSQL' })
    fireEvent.change(within(editor).getByLabelText('属性1数据源'), { target: { value: 'source-b' } })
    fireEvent.change(within(editor).getByLabelText('属性1命名空间'), { target: { value: 'public' } })
    fireEvent.change(within(editor).getByLabelText('属性1来源表'), { target: { value: 'asset_details' } })
    fireEvent.change(within(editor).getByLabelText('属性1映射列'), { target: { value: 'asset_name' } })
    fireEvent.change(within(editor).getByLabelText('属性1关联键列'), { target: { value: 'equipment_id' } })
    fireEvent.change(within(editor).getByLabelText('属性1租户列'), { target: { value: 'tenant_id' } })
    await userEvent.click(within(editor).getByRole('button', { name: '保存草稿' }))
    await within(editor).findByText('跨来源属性“设备名称”必须允许空值。')
    expect(api.saveDraft).not.toHaveBeenCalled()
    await userEvent.click(within(editor).getByRole('checkbox', { name: '允许空值' }))
    await userEvent.click(within(editor).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({ schema: expect.objectContaining({ properties: [expect.objectContaining({ id: 'name', nullable: true, mapping: { dataSourceId: 'source-b', namespace: 'public', table: 'asset_details', column: 'asset_name', keyColumn: 'equipment_id', tenantColumn: 'tenant_id' } })] }) }), 'saved-1'))
  })

  it('反向读取源列保留已有属性ID和中文名，迟到的目录结果不覆盖新绑定', async () => {
    const initial = record()
    initial.definition.schema!.mode = 'db_2_schema'
    initial.definition.schema!.storageBinding = { dataSourceId: 'source-a', table: 'equipment', idColumn: 'id' }
    initial.definition.schema!.properties[0].mapping = { dataSourceId: 'source-a', column: 'equipment_name' }
    const api = client(initial)
    api.inspectSourceTable.mockResolvedValue({ namespace: 'business', name: 'equipment', signature: 'snapshot', indexes: [], columns: [
      { name: 'id', databaseType: 'bigint', dataType: 'int64', nullable: false, primaryKey: true, generated: false, position: 1 },
      { name: 'equipment_name', databaseType: 'varchar(255)', dataType: 'string', nullable: true, primaryKey: false, generated: false, position: 2 },
    ] })
    api.saveDraft.mockImplementation(async (_id, definition) => ({ ...initial, definition, etag: 'saved-2' }))
    mount(api, [initial])
    const editor = await edit()
    await userEvent.click(within(editor).getByRole('button', { name: '从主表读取属性' }))
    await within(editor).findByText(/已载入 1 个主表属性/)
    expect((within(editor).getByLabelText('对象属性第1项名称') as HTMLInputElement).value).toBe('设备名称')
    await userEvent.click(within(editor).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({ schema: expect.objectContaining({ properties: [expect.objectContaining({ id: 'name', name: '设备名称', mapping: expect.objectContaining({ column: 'equipment_name' }) })] }) }), 'saved-1'))
    let finish!: (value: unknown) => void
    api.inspectSourceTable.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    await userEvent.click(within(editor).getByRole('button', { name: '从主表读取属性' }))
    fireEvent.change(within(editor).getByLabelText('主表名称'), { target: { value: 'changed_table' } })
    await act(async () => finish({ namespace: 'business', name: 'equipment', columns: [], indexes: [], signature: 'late' }))
    expect((within(editor).getByLabelText('主表名称') as HTMLInputElement).value).toBe('changed_table')
    expect((within(editor).getByLabelText('对象属性第1项名称') as HTMLInputElement).value).toBe('设备名称')
    api.inspectSourceTable.mockResolvedValueOnce({ namespace: 'business', name: 'changed_table', signature: 'generated', indexes: [], columns: [
      { name: 'id', databaseType: 'bigint', dataType: 'int64', nullable: false, primaryKey: true, generated: true, position: 1 },
      { name: 'derived_name', databaseType: 'varchar(255)', dataType: 'string', nullable: false, primaryKey: false, generated: true, position: 2 },
    ] })
    await userEvent.click(within(editor).getByRole('button', { name: '从主表读取属性' }))
    await within(editor).findByText(/derived_name（生成列暂未支持）/)
    expect((within(editor).getByLabelText('对象属性第1项名称') as HTMLInputElement).value).toBe('设备名称')
  })

  it('已生效的正向绑定允许增加可空属性，已有属性和映射保持锁定', async () => {
    const initial = record('active')
    initial.definition.schema!.storageBinding = { dataSourceId: 'source-a', table: 'equipment', idColumn: 'id' }
    initial.definition.schema!.properties[0].mapping = { dataSourceId: 'source-a', column: 'name' }
    const api = client(initial)
    mount(api, [initial])
    const editor = await edit()
    expect((within(editor).getByLabelText('对象属性第1项类型') as HTMLSelectElement).disabled).toBe(true)
    expect((within(editor).getByLabelText('属性1数据源') as HTMLSelectElement).disabled).toBe(true)
    await userEvent.click(within(editor).getByRole('button', { name: '添加属性' }))
    expect((within(editor).getByLabelText('对象属性第2项类型') as HTMLSelectElement).disabled).toBe(false)
    expect((within(editor).getAllByRole('checkbox', { name: '允许空值' })[1] as HTMLInputElement).checked).toBe(true)
    expect((within(editor).getAllByRole('checkbox', { name: '允许空值' })[1] as HTMLInputElement).disabled).toBe(true)
    expect(within(editor).queryByRole('button', { name: '删除对象属性第1项' })).toBeNull()
  })

  it('新建定义固定绑定顶部当前分类，仅允许该分类的分组', async () => {
    const secondCategory = { id: 'quality', tenantId: 'tenant', name: '质量管理' }
    const secondGroup = { id: 'quality-group', tenantId: 'tenant', categoryId: 'quality', name: '质检资料' }
    const api = client()
    api.listGroups.mockResolvedValue([...groups, secondGroup])
    render(<ModelsPage api={api as unknown as ApiClient} categoryId="quality" categories={[...categories, secondCategory]} entries={[]} onRefresh={vi.fn().mockResolvedValue(undefined)} onQuery={vi.fn()} onOrganizations={vi.fn()} />)
    expect(screen.queryByLabelText('按分类筛选')).toBeNull()
    expect(screen.queryByRole('button', { name: '管理分类' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    const category = within(dialog).getByLabelText('分类') as HTMLInputElement
    expect(category.value).toBe('质量管理')
    expect(category.readOnly).toBe(true)
    expect(api.listGroups).toHaveBeenCalledWith('quality', expect.any(AbortSignal))
    await within(dialog).findByRole('option', { name: '质检资料' })
    expect(within(dialog).queryByRole('option', { name: '基础资料' })).toBeNull()
    fireEvent.change(within(dialog).getByLabelText('分组'), { target: { value: secondGroup.id } })
    await userEvent.type(within(dialog).getByLabelText('业务名称'), '质检项目')
    await userEvent.type(within(dialog).getByLabelText('对象属性第1项名称'), '项目名称')
    const created = record()
    Object.assign(created.definition, { categoryId: 'quality', groupId: secondGroup.id, name: '质检项目' })
    created.definition.schema!.properties[0].name = '项目名称'
    api.createDefinition.mockResolvedValue(created)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledWith(expect.objectContaining({ categoryId: 'quality', groupId: secondGroup.id })))
  })

  it('未选择分类或当前分类没有分组时引导独立管理页', async () => {
    const api = client()
    api.listGroups.mockResolvedValue([])
    const onOrganizations = vi.fn()
    const props = { api: api as unknown as ApiClient, categories, entries: [], onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn(), onOrganizations }
    const view = render(<ModelsPage {...props} categoryId="" />)
    expect(screen.getByText('先选择当前业务分类')).toBeTruthy()
    expect((screen.getByRole('button', { name: '新建定义' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '前往分类与分组' }))
    expect(onOrganizations).toHaveBeenCalledTimes(1)
    expect(api.listGroups).not.toHaveBeenCalled()
    view.rerender(<ModelsPage {...props} categoryId="category" />)
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    await within(dialog).findByText('当前分类还没有分组，请前往「分类与分组」创建分组后再新建定义。')
    await userEvent.type(within(dialog).getByLabelText('业务名称'), '尚无分组')
    expect((within(dialog).getByRole('button', { name: '保存草稿' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(within(dialog).getByRole('button', { name: '前往分类与分组' }))
    expect(onOrganizations).toHaveBeenCalledTimes(2)
    expect(api.createDefinition).not.toHaveBeenCalled()
  })

  it('目录与统计限定当前分类，关系仍可引用其他分类的已授权模型', async () => {
    const other = record('active')
    Object.assign(other.definition, { id: 'quality-model', categoryId: 'quality', groupId: 'quality-group', name: '检验报告' })
    const api = client()
    render(<ModelsPage api={api as unknown as ApiClient} categoryId="category" categories={[...categories, { id: 'quality', tenantId: 'tenant', name: '质量管理' }]} entries={[record('active'), other]} onRefresh={vi.fn().mockResolvedValue(undefined)} onQuery={vi.fn()} onOrganizations={vi.fn()} />)
    expect(screen.getByRole('button', { name: '全部定义1' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '对象模型1' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '检验报告' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '关系0' }))
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    expect(within(within(dialog).getByLabelText('来源模型')).getByRole('option', { name: '质量管理 / 检验报告' })).toBeTruthy()
  })

  it('从分类与分组返回时刷新可选分组，保留尚未保存的模型表单', async () => {
    const api = client()
    api.listGroups.mockResolvedValueOnce([])
    const onOrganizations = vi.fn()
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, entries: [], onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn(), onOrganizations }
    const view = render(<ModelsPage {...props} active />)
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    await within(dialog).findByText('当前分类还没有分组，请前往「分类与分组」创建分组后再新建定义。')
    await userEvent.type(within(dialog).getByLabelText('业务名称'), '待完成的设备模型')
    await userEvent.type(within(dialog).getByLabelText('对象属性第1项名称'), '设备编码')
    await userEvent.click(within(dialog).getByRole('button', { name: '前往分类与分组' }))
    expect(onOrganizations).toHaveBeenCalledTimes(1)
    const groupsSignal = api.listGroups.mock.calls[0][1] as AbortSignal
    view.rerender(<ModelsPage {...props} active={false} />)
    expect(groupsSignal.aborted).toBe(true)
    api.listGroups.mockResolvedValue(groups)
    expect(api.listGroups).toHaveBeenCalledTimes(1)
    view.rerender(<ModelsPage {...props} active />)
    await within(dialog).findByRole('option', { name: '基础资料' })
    expect(api.listGroups).toHaveBeenCalledTimes(2)
    expect((within(dialog).getByLabelText('业务名称') as HTMLInputElement).value).toBe('待完成的设备模型')
    expect((within(dialog).getByLabelText('对象属性第1项名称') as HTMLInputElement).value).toBe('设备编码')
    fireEvent.change(within(dialog).getByLabelText('分组'), { target: { value: 'group' } })
    expect((within(dialog).getByRole('button', { name: '保存草稿' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('分类切换关闭旧编辑并重置搜索，迟到的旧定义读取不会打开新分类编辑器', async () => {
    const api = client()
    let finishRead!: (value: DefinitionRecord) => void
    api.getDraft.mockImplementationOnce(() => new Promise((resolve) => { finishRead = resolve }))
    const secondCategory = { id: 'quality', tenantId: 'tenant', name: '质量管理' }
    const props = { api: api as unknown as ApiClient, categories: [...categories, secondCategory], entries: [record()], onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn(), onOrganizations: vi.fn() }
    const view = render(<ModelsPage {...props} categoryId="category" />)
    await userEvent.type(screen.getByLabelText('搜索业务模型'), '生产')
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    view.rerender(<ModelsPage {...props} categoryId="quality" />)
    await act(async () => finishRead(record()))
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
    expect((screen.getByLabelText('搜索业务模型') as HTMLInputElement).value).toBe('')
    expect(screen.queryByRole('button', { name: '生产设备' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    expect((within(dialog).getByLabelText('分类') as HTMLInputElement).value).toBe('质量管理')
    view.rerender(<ModelsPage {...props} categoryId="category" />)
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
  })

  it('未确认发布按分类保留，切回原分类后仍可恢复', async () => {
    const api = client()
    const publication = { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'activation_pending' }
    api.publish.mockRejectedValue(new ApiError(503, 'activation_pending', '等待激活', publication))
    const props = { api: api as unknown as ApiClient, categories: [...categories, { id: 'quality', tenantId: 'tenant', name: '质量管理' }], entries: [record()], onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn(), onOrganizations: vi.fn() }
    const view = render(<ModelsPage {...props} categoryId="category" />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    view.rerender(<ModelsPage {...props} categoryId="quality" />)
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    view.rerender(<ModelsPage {...props} categoryId="category" />)
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待激活.*恢复发布/ }))
    expect(within(await screen.findByRole('region', { name: '模型编辑' })).getByRole('button', { name: /继续恢复发布/ })).toBeTruthy()
  })

  it('切换分类后迟到的发布结果仅保留原分类恢复意图', async () => {
    const api = client()
    let rejectPublication!: (cause: unknown) => void
    api.publish.mockImplementation(() => new Promise((_, reject) => { rejectPublication = reject }))
    const props = { api: api as unknown as ApiClient, categories: [...categories, { id: 'quality', tenantId: 'tenant', name: '质量管理' }], entries: [record()], onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn(), onOrganizations: vi.fn() }
    const view = render(<ModelsPage {...props} categoryId="category" />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await waitFor(() => expect(api.publish).toHaveBeenCalledTimes(1))
    view.rerender(<ModelsPage {...props} categoryId="quality" />)
    await act(async () => rejectPublication(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' })))
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    view.rerender(<ModelsPage {...props} categoryId="category" />)
    expect(screen.getByRole('button', { name: /生产设备.*等待确认.*恢复发布/ })).toBeTruthy()
  })

  it('服务器返回其他分类的定义时拒绝在当前分类打开', async () => {
    const api = client()
    const other = record()
    other.definition.categoryId = 'quality'
    api.getDraft.mockResolvedValue(other)
    mount(api)
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    await screen.findByText('定义不属于当前分类，请切换顶部分类后重新打开。')
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
  })

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
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待激活.*恢复发布/ }))
    dialog = await screen.findByRole('region', { name: '模型编辑' })
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
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
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
    const dialog = await screen.findByRole('region', { name: '模型编辑' })
    expect((within(dialog).getByRole('button', { name: '添加属性' }) as HTMLButtonElement).disabled).toBe(true)
    expect((within(dialog).getByRole('button', { name: '添加索引' }) as HTMLButtonElement).disabled).toBe(true)
    expect(within(dialog).queryByLabelText('对象属性第1项枚举')).toBeNull()
    expect(within(dialog).getByText(/对象属性暂不支持绑定枚举/)).toBeTruthy()
  }, 15000)

  it('保存刷新完成后目录服从最新远端内容，不被本地副本长期覆盖', async () => {
    const api = client()
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, onOrganizations: vi.fn(), onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '我的草稿')
    const saved = record('draft', 'saved-2'); saved.definition.name = '我的草稿'
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await within(dialog).findByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    const external = record('draft', 'external'); external.definition.name = '其他协作者的新名称'
    view.rerender(<ModelsPage {...props} entries={[external]} />)
    expect(screen.getByRole('button', { name: '其他协作者的新名称' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '我的草稿' })).toBeNull()
  })

  it('目录刷新失败并清空授权目录时不残留乐观副本', async () => {
    const api = client()
    const onRefresh = vi.fn().mockRejectedValue(new Error('目录不可用'))
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, onOrganizations: vi.fn(), onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    const name = within(dialog).getByLabelText('业务名称')
    await userEvent.clear(name); await userEvent.type(name, '仍保存在服务器的草稿')
    const saved = record('draft', 'saved-2'); saved.definition.name = '仍保存在服务器的草稿'
    api.saveDraft.mockResolvedValue(saved)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存草稿' }))
    await within(dialog).findByText('草稿已保存，目录刷新失败；可稍后刷新目录。')
    view.rerender(<ModelsPage {...props} entries={[]} />)
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    expect(screen.queryByRole('button', { name: '仍保存在服务器的草稿' })).toBeNull()
    expect(screen.getByText('从第一个业务对象开始')).toBeTruthy()
  })

  it('目录编辑授权被拒绝时关闭旧草稿和待恢复记录，但保留新建入口', async () => {
    const api = client()
    const initial = record()
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, onOrganizations: vi.fn(), onRefresh, onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[initial]} />)
    const publication = { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', publication))
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    view.rerender(<ModelsPage {...props} entries={[]} draftDirectoryDenied />)
    await waitFor(() => expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull())
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    expect(screen.queryByText('生产设备')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '新建定义' }))
    expect((within(await screen.findByRole('region', { name: '模型编辑' })).getByLabelText('业务名称') as HTMLInputElement).value).toBe('')
  })

  it('重开待恢复记录先重新验证草稿授权，403 时清除旧缓存', async () => {
    const api = client()
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    mount(api)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    api.getDraft.mockRejectedValue(new ApiError(403, 'forbidden', '无权读取此草稿'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待确认.*恢复发布/ }))
    await screen.findByText('无权读取此草稿')
    expect(api.getDraft).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
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
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    api.getDraft.mockRejectedValue(new ApiError(404, 'not_found', '没有草稿'))
    api.getCurrent.mockResolvedValue(record('active'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待激活.*恢复发布/ }))
    dialog = await screen.findByRole('region', { name: '模型编辑' })
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
    await userEvent.click(within(dialog).getByRole('button', { name: '返回模型列表' }))
    api.getDraft.mockRejectedValue(new ApiError(404, 'not_found', '没有草稿'))
    api.getCurrent.mockResolvedValue(record('active', 'different-current'))
    await userEvent.click(screen.getByRole('button', { name: /生产设备.*等待确认.*恢复发布/ }))
    await screen.findByText(/待恢复内容已改变，已清除旧草稿缓存/)
    expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })

  it('逐对象编辑授权撤销导致草稿被目录过滤时也清除已打开的私有候选', async () => {
    const initial = record('draft', 'private-draft'); initial.hasCurrent = true; initial.definition.name = '仅编辑者可见的草稿'
    const api = client(initial)
    api.publish.mockRejectedValue(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, onOrganizations: vi.fn(), onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[initial]} />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await within(dialog).findByText('发布结果待确认')
    const current = record('active', 'public-current')
    view.rerender(<ModelsPage {...props} entries={[current]} />)
    await waitFor(() => expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull())
    expect(screen.queryByText('仅编辑者可见的草稿')).toBeNull()
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
    expect(screen.getByRole('button', { name: '生产设备' })).toBeTruthy()
  })

  it('目录撤权后迟到的发布响应不能重新创建私有候选缓存', async () => {
    const api = client()
    let rejectPublication!: (cause: unknown) => void
    api.publish.mockImplementation(() => new Promise((_, reject) => { rejectPublication = reject }))
    const props = { api: api as unknown as ApiClient, categoryId: 'category', categories, onOrganizations: vi.fn(), onRefresh: vi.fn().mockResolvedValue(undefined), onQuery: vi.fn() }
    const view = render(<ModelsPage {...props} entries={[record()]} />)
    const dialog = await edit()
    await userEvent.click(within(dialog).getByRole('button', { name: /发布定义/ }))
    await waitFor(() => expect(api.publish).toHaveBeenCalledTimes(1))
    view.rerender(<ModelsPage {...props} entries={[]} draftDirectoryDenied />)
    rejectPublication(new ApiError(503, 'prepare_failed', '等待恢复', { id: 'operation', tenantId: 'tenant', definitionId: 'equipment', status: 'prepare_failed' }))
    await waitFor(() => expect(screen.queryByRole('region', { name: '模型编辑' })).toBeNull())
    expect(screen.queryByRole('region', { name: '待恢复的发布' })).toBeNull()
  })
})
