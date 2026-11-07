import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ApiClient } from '../../lib/api'
import type { Schema, SchemaStorageMapping } from '../../lib/types'
import { SchemaPropertiesEditor } from './SchemaPropertiesEditor'

afterEach(cleanup)

function fixture() {
  const schema: Schema = {
    mode: 'schema_2_db', storageLayout: 'single',
    storageBinding: { dataSourceId: 'source', table: 'notes', idColumn: 'id' },
    properties: [
      { id: 'title', name: '标题', dataType: 'string', mapping: { dataSourceId: 'source', column: 'title' } },
      { id: 'summary', name: '摘要', dataType: 'text', nullable: true, mapping: { dataSourceId: 'source', column: 'summary' } },
    ],
    indexes: [{ id: 'title-index', name: '标题索引', unique: false, fieldIds: ['title'] }],
  }
  const primary = { dataSourceId: 'source', dataSourceName: '知识库', driver: 'mysql', namespace: 'knowledge', table: 'notes', keyColumn: 'id' }
  const physical: SchemaStorageMapping = {
    schemaId: 'notes', definitionETag: 'current', managed: false, mode: 'schema_2_db', primary,
    properties: schema.properties.map((property) => ({ ...primary, propertyId: property.id, propertyName: property.name, column: property.mapping!.column, databaseType: property.dataType === 'string' ? 'varchar(255)' : 'text', exists: true })),
  }
  const api = {
    listDataSources: vi.fn().mockResolvedValue([{ id: 'source', name: '知识库', database: 'knowledge', driver: 'mysql', enabled: true }]),
    listSourceTables: vi.fn().mockResolvedValue([]),
    inspectSourceTable: vi.fn().mockResolvedValue({ columns: [] }),
    getSchemaStorageMapping: vi.fn().mockResolvedValue(physical),
  }
  const props = { api: api as unknown as ApiClient, schema, onChange: vi.fn(), locked: false, allowAppend: false, disabled: false, active: true, draft: false, maxProperties: 32 }
  return { api, props, physical }
}

describe('对象属性列表与编辑对话框', () => {
  it('默认只展示摘要，打开单项才读取该属性的表列与实际映射', async () => {
    const { api, props } = fixture()
    render(<SchemaPropertiesEditor {...props} currentId="notes" currentETag="current" />)
    expect(await screen.findAllByText('知识库')).toHaveLength(2)
    expect(screen.getByRole('table', { name: '对象属性列表' }).querySelectorAll('tbody tr')).toHaveLength(2)
    expect(api.inspectSourceTable).not.toHaveBeenCalled()
    expect(api.getSchemaStorageMapping).not.toHaveBeenCalled()
    expect(screen.queryByText('查看当前生效的数据库映射')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第2项' }))
    const dialog = screen.getByRole('dialog', { name: '编辑属性' })
    expect(within(dialog).getByLabelText('属性2映射列')).toBeTruthy()
    expect(within(dialog).queryByLabelText('属性1映射列')).toBeNull()
    expect(api.inspectSourceTable).toHaveBeenCalledTimes(1)
    const actual = within(dialog).getByRole('region', { name: '属性实际数据库映射' })
    expect(await within(actual).findByText('text')).toBeTruthy()
    expect(within(actual).queryByText('varchar(255)')).toBeNull()
    expect(api.getSchemaStorageMapping).toHaveBeenCalledTimes(1)
    expect(api.getSchemaStorageMapping).toHaveBeenCalledWith('notes', expect.any(AbortSignal))
    expect(screen.queryByRole('table', { name: '当前生效的数据库映射' })).toBeNull()
  })

  it('数据库缺列在当前属性中说明，刷新只重新核对而不修改草稿', async () => {
    const { api, props, physical } = fixture()
    api.getSchemaStorageMapping.mockResolvedValueOnce({ ...physical, properties: [{ ...physical.properties[0], databaseType: '', exists: false }] })
    render(<SchemaPropertiesEditor {...props} currentId="notes" currentETag="current" />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    const actual = screen.getByRole('region', { name: '属性实际数据库映射' })
    expect(await within(actual).findByText('字段缺失')).toBeTruthy()
    await userEvent.click(within(actual).getByRole('button', { name: '刷新核对' }))
    expect(await within(actual).findByText('varchar(255)')).toBeTruthy()
    expect(within(actual).queryByText('字段缺失')).toBeNull()
    expect(api.getSchemaStorageMapping).toHaveBeenCalledTimes(2)
    expect(props.onChange).not.toHaveBeenCalled()
  })

  it('定义摘要不匹配时拒绝展示过期属性映射', async () => {
    const { props } = fixture()
    render(<SchemaPropertiesEditor {...props} currentId="notes" currentETag="new-current" />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    const actual = screen.getByRole('region', { name: '属性实际数据库映射' })
    expect((await within(actual).findByRole('alert')).textContent).toMatch(/当前定义已更新/)
    expect(within(actual).queryByText('varchar(255)')).toBeNull()
  })

  it('平台存储在属性弹窗显示服务端返回的物理位置，不推算或展示其他属性', async () => {
    const { api, props, physical } = fixture()
    const primary = { ...physical.primary, dataSourceName: '平台实际库', namespace: 'actual_database', table: 'af_actual_notes', keyColumn: 'actual_key' }
    api.getSchemaStorageMapping.mockResolvedValue({ ...physical, managed: true, primary, properties: physical.properties.map((property) => ({ ...property, ...primary, column: `f_actual_${property.propertyId}` })) })
    const schema = { ...props.schema, storageBinding: undefined, properties: props.schema.properties.map((property) => ({ ...property, mapping: undefined })) }
    render(<SchemaPropertiesEditor {...props} schema={schema} currentId="notes" currentETag="current" />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    const actual = screen.getByRole('region', { name: '属性实际数据库映射' })
    expect(await within(actual).findByText('f_actual_title')).toBeTruthy()
    for (const value of ['平台实际库', 'actual_database', 'af_actual_notes', 'actual_key', 'varchar(255)']) {
      expect(within(actual).getByText(value)).toBeTruthy()
    }
    expect(within(actual).queryByText('f_actual_summary')).toBeNull()
    expect(screen.queryByText('查看当前生效的数据库映射')).toBeNull()
  })

  it('取消后中止请求，迟到的旧属性结果不覆盖重新打开的属性', async () => {
    const { api, props, physical } = fixture()
    let finish!: (value: SchemaStorageMapping) => void
    api.getSchemaStorageMapping.mockImplementationOnce(() => new Promise<SchemaStorageMapping>((resolve) => { finish = resolve }))
    render(<SchemaPropertiesEditor {...props} currentId="notes" currentETag="current" />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    const signal = api.getSchemaStorageMapping.mock.calls[0][1] as AbortSignal
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '取消' }))
    expect(signal.aborted).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第2项' }))
    const actual = screen.getByRole('region', { name: '属性实际数据库映射' })
    expect(await within(actual).findByText('text')).toBeTruthy()
    await act(async () => finish({ ...physical, properties: [{ ...physical.properties[0], databaseType: 'late_old_type' }] }))
    expect(within(actual).queryByText('late_old_type')).toBeNull()
    expect(within(actual).getByText('text')).toBeTruthy()
  })

  it('尚未发布的属性不虚构实际字段', async () => {
    const { api, props } = fixture()
    props.schema.properties.push({ id: 'draft-field', name: '待发布字段', dataType: 'string' })
    render(<SchemaPropertiesEditor {...props} currentId="notes" currentETag="current" draft />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第3项' }))
    const actual = screen.getByRole('region', { name: '属性实际数据库映射' })
    expect(await within(actual).findByText(/尚未生效/)).toBeTruthy()
    expect(within(actual).queryByText('varchar(255)')).toBeNull()
    expect(api.getSchemaStorageMapping).toHaveBeenCalledTimes(1)
  })

  it('Escape 丢弃嵌套映射修改并将焦点还给对应编辑按钮', async () => {
    const { props } = fixture()
    render(<SchemaPropertiesEditor {...props} />)
    const edit = screen.getByRole('button', { name: '编辑对象属性第1项' })
    await userEvent.click(edit)
    const column = within(screen.getByRole('dialog')).getByLabelText('属性1映射列')
    await userEvent.clear(column)
    await userEvent.type(column, 'discarded')
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(props.onChange).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(edit)
    await userEvent.click(edit)
    expect((within(screen.getByRole('dialog')).getByLabelText('属性1映射列') as HTMLInputElement).value).toBe('title')
  })

  it('页面忙碌时 portal 内字段和应用同时禁用，离开页签丢弃局部编辑', async () => {
    const { props } = fixture()
    const view = render(<SchemaPropertiesEditor {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '编辑对象属性第1项' }))
    view.rerender(<SchemaPropertiesEditor {...props} disabled />)
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByLabelText('对象属性第1项名称').matches(':disabled')).toBe(true)
    expect(within(dialog).getByLabelText('属性1映射列').matches(':disabled')).toBe(true)
    expect((within(dialog).getByRole('button', { name: '应用修改' }) as HTMLButtonElement).disabled).toBe(true)
    view.rerender(<SchemaPropertiesEditor {...props} active={false} />)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(props.onChange).not.toHaveBeenCalled()
  })
})
