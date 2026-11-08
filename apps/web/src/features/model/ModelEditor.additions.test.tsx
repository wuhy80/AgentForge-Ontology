import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ApiClient } from '../../lib/api'
import type { DefinitionRecord } from '../../lib/types'
import { ModelEditor } from './ModelEditor'

afterEach(cleanup)

function fixture(kind: 'schema' | 'enum' = 'schema') {
  const current: DefinitionRecord = { etag: 'current', status: 'active', hasCurrent: true, definition: {
    id: 'model', tenantId: 'tenant', categoryId: 'category', groupId: 'group', name: '设备', kind,
    ...(kind === 'schema' ? { schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name', name: '名称', dataType: 'string' as const }], indexes: [{ id: 'old-index', name: '名称索引', unique: false, fieldIds: ['name'] }] } }
      : { enum: { values: [{ code: 'READY', label: '就绪' }, { code: 'OLD', label: '旧状态' }] } }),
  } }
  const api = {
    listGroups: vi.fn().mockResolvedValue([{ id: 'group', categoryId: 'category', name: '基础资料' }]),
    listDataSources: vi.fn().mockResolvedValue([]), getCurrent: vi.fn().mockResolvedValue(current),
    metadataCapabilities: vi.fn().mockResolvedValue({ schemaMigrations: { addFields: true, addIndexes: true, destructive: false } }),
    getSchemaStorageMapping: vi.fn().mockResolvedValue({ schemaId: 'model', definitionETag: 'current', properties: [] }),
    saveDraft: vi.fn().mockImplementation(async (_id, definition) => {
      const saved = structuredClone(definition)
      saved.schema?.properties.forEach((property: { id: string }, index: number) => { property.id ||= `saved-${index}` })
      saved.schema?.indexes?.forEach((index: { id: string }, position: number) => { index.id ||= `saved-index-${position}` })
      return { definition: saved, status: 'draft', hasCurrent: true, etag: 'draft' }
    }),
    preview: vi.fn().mockResolvedValue({ statements: ['ALTER TABLE test ADD COLUMN added BIGINT;', 'UPDATE test SET added = ?;'] }),
  }
  const props = { api: api as unknown as ApiClient, categoryId: 'category', categories: [{ id: 'category', tenantId: 'tenant', name: '业务' }], entries: [current], currentEntries: [current], initialRecord: current, initialKind: kind, onClose: vi.fn(), onOrganizations: vi.fn(), onSaved: vi.fn().mockResolvedValue(undefined), onPending: vi.fn(), onPublished: vi.fn().mockResolvedValue(undefined) }
  return { api, current, props }
}

describe('已生效定义安全增量', () => {
  it('旧后端没有显式迁移能力时保持当前平台结构锁定', async () => {
    const { api, props } = fixture()
    api.metadataCapabilities.mockResolvedValue({})
    render(<ModelEditor {...props} />)
    await waitFor(() => expect(api.metadataCapabilities).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('button', { name: '添加属性' })).toBeNull()
    await userEvent.click(screen.getByRole('tab', { name: '索引信息' }))
    expect(screen.queryByRole('button', { name: '添加索引' })).toBeNull()
  })

  it('已有草稿无法读取当前态时禁止把草稿字段误认为新增项', async () => {
    const { api, current, props } = fixture()
    const draft: DefinitionRecord = { ...current, status: 'draft', etag: 'draft', hasCurrent: true }
    api.getCurrent.mockRejectedValue(new Error('当前结构读取失败'))
    render(<ModelEditor {...props} entries={[draft]} currentEntries={[]} initialRecord={draft} />)
    expect((await screen.findByRole('alert')).textContent).toContain('当前结构读取失败')
    expect(screen.queryByRole('button', { name: '添加属性' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    expect(within(screen.getByRole('dialog')).getByLabelText('对象属性第1项类型').matches(':disabled')).toBe(true)
  })

  it('平台字段保存取得ID后仍可编辑，既有字段和索引保持锁定，预览列出迁移计划', async () => {
    const { api, props } = fixture()
    render(<ModelEditor {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    expect(within(screen.getByRole('dialog')).getByLabelText('对象属性第1项类型').matches(':disabled')).toBe(true)
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '取消' }))
    await userEvent.click(screen.getByRole('button', { name: '添加属性' }))
    let dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('对象属性第2项名称'), '计数')
    await userEvent.selectOptions(within(dialog).getByLabelText('对象属性第2项类型'), 'int64')
    await userEvent.click(within(dialog).getByLabelText('允许空值'))
    await userEvent.selectOptions(within(dialog).getByLabelText('对象属性第2项默认值方式'), 'value')
    const input = within(dialog).getByLabelText('对象属性第2项默认值')
    await userEvent.clear(input); await userEvent.type(input, '9223372036854775807')
    await userEvent.click(within(dialog).getByRole('button', { name: '添加属性' }))
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledTimes(1))
    await screen.findByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')
    expect(api.saveDraft.mock.calls[0][1].schema.properties[1]).toMatchObject({ nullable: false, default: '9223372036854775807' })
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第2项' }))
    dialog = screen.getByRole('dialog')
    expect(within(dialog).getByLabelText('对象属性第2项类型').matches(':disabled')).toBe(false)
    await userEvent.click(within(dialog).getByRole('button', { name: '取消' }))
    await userEvent.click(screen.getByRole('tab', { name: '索引信息' }))
    expect(screen.getByLabelText('索引1第一个属性').matches(':disabled')).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '添加索引' }))
    expect(screen.getByLabelText('索引2第一个属性').matches(':disabled')).toBe(false)
    await userEvent.type(screen.getByLabelText('索引2名称'), '计数唯一')
    await userEvent.selectOptions(screen.getByLabelText('索引2第一个属性'), 'saved-1')
    fireEvent.change(screen.getByLabelText('索引2名称').closest('.model-index-row')!.querySelector('select')!, { target: { value: 'unique' } })
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledTimes(2))
    await userEvent.click(screen.getByRole('button', { name: '预览计划' }))
    expect(await screen.findByText(/ALTER TABLE test ADD COLUMN/)).toBeTruthy()
    expect(screen.getByText(/UPDATE test SET added/)).toBeTruthy()
  })

  it('枚举允许增加和停用，旧编码不能更改或删除，新值保存后仍可编辑', async () => {
    const { api, props } = fixture('enum')
    render(<ModelEditor {...props} />)
    expect(screen.getByLabelText('枚举值1编码').matches(':disabled')).toBe(true)
    expect(screen.queryByRole('button', { name: '删除枚举值1' })).toBeNull()
    await userEvent.click(screen.getAllByLabelText('停用值')[1])
    await userEvent.click(screen.getByRole('button', { name: '添加值' }))
    await userEvent.type(screen.getByLabelText('枚举值3编码'), 'RUNNING')
    await userEvent.type(screen.getByLabelText('枚举值3标签'), '运行中')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await screen.findByText('草稿已保存。可以继续配置索引、预览存储计划或发布。')
    expect(api.saveDraft.mock.calls[0][1].enum.values).toEqual([{ code: 'READY', label: '就绪' }, { code: 'OLD', label: '旧状态', deprecated: true }, { code: 'RUNNING', label: '运行中', deprecated: false }])
    expect(screen.getByLabelText('枚举值3编码').matches(':disabled')).toBe(false)
    expect(screen.getByRole('button', { name: '删除枚举值3' })).toBeTruthy()
  })
})
