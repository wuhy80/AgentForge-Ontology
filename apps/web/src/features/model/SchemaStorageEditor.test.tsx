import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ApiClient } from '../../lib/api'
import type { Schema, SchemaStorageMapping, SourceColumn, SourceTable } from '../../lib/types'
import { SchemaStorageEditor } from './SchemaStorageEditor'

afterEach(cleanup)
const schema: Schema = { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'title', name: '标题', dataType: 'string' }] }
const sources = [
  { id: 'mysql', name: '业务库', driver: 'mysql', enabled: true, tenantIsolation: 'dedicated' },
  { id: 'postgres', name: '资产库', driver: 'postgresql', enabled: true, tenantIsolation: 'dedicated' },
]
const column = (name: string, patch: Partial<SourceColumn> = {}): SourceColumn => ({ name, databaseType: 'varchar(255)', dataType: 'string', nullable: false, primaryKey: false, generated: false, maxLength: 255, position: 1, ...patch })
const catalog = (name = 'notes', columns = [column('title')]): SourceTable => ({ namespace: 'business', name, columns, indexes: [], signature: name })
const physical: SchemaStorageMapping = {
  schemaId: 'notes', definitionETag: 'active-etag', mode: 'schema_2_db', managed: true,
  primary: { dataSourceName: '平台默认存储', driver: 'mysql', namespace: 'actual_database', table: 'actual_table_from_server', keyColumn: 'id', tenantColumn: 'tenant_id' },
  properties: [
    { propertyId: 'title', propertyName: '标题', dataSourceName: '平台默认存储', driver: 'mysql', namespace: 'actual_database', table: 'actual_table_from_server', keyColumn: 'id', column: 'actual_column_from_server', databaseType: 'varchar(255)', exists: true },
    { propertyId: 'body', propertyName: '正文', dataSourceName: '平台默认存储', driver: 'mysql', namespace: 'actual_database', table: 'actual_table_from_server', keyColumn: 'id', column: 'missing_body', databaseType: '', exists: false },
  ],
}
function client() {
  return {
    listDataSources: vi.fn().mockResolvedValue(sources),
    listSourceTables: vi.fn().mockResolvedValue([{ namespace: 'business', name: 'notes', type: 'table' }]),
    inspectSourceTable: vi.fn().mockResolvedValue(catalog()),
    getSchemaStorageMapping: vi.fn().mockResolvedValue(physical),
  }
}
function Harness({ api, initial, locked = false }: { api: ReturnType<typeof client>; initial: Schema; locked?: boolean }) {
  const [value, setValue] = useState(initial)
  return <><SchemaStorageEditor api={api as unknown as ApiClient} schema={value} section="mappings" active locked={locked} onChange={setValue} /><output aria-label="映射结果">{JSON.stringify(value)}</output></>
}
function mapped(mode = 'db_2_schema'): Schema {
  return { ...schema, mode, storageBinding: { dataSourceId: 'mysql', namespace: 'business', table: 'notes', idColumn: 'id' }, properties: [{ ...schema.properties[0], mapping: { dataSourceId: 'mysql', column: 'old_title' } }] }
}

describe('Schema 数据源与物理字段映射', () => {
  it('默认存储逐项展示服务端实际数据库、表、列和缺失字段', async () => {
    const api = client()
    render(<SchemaStorageEditor api={api as unknown as ApiClient} schema={schema} section="mappings" active locked currentId="notes" currentETag="active-etag" onChange={vi.fn()} />)
    const table = await screen.findByRole('table', { name: '当前生效的数据库映射' })
    const title = within(table).getByRole('row', { name: /标题/ })
    expect(title.textContent).toContain('actual_database')
    expect(title.textContent).toContain('actual_table_from_server')
    expect(title.textContent).toContain('actual_column_from_server')
    expect(title.textContent).toContain('varchar(255)')
    expect(within(table).getByRole('row', { name: /正文/ }).textContent).toContain('字段缺失')
    expect(api.getSchemaStorageMapping).toHaveBeenCalledWith('notes', expect.any(AbortSignal))
  })

  it('未发布草稿只显示待生成状态，已有草稿明确区分生效映射', async () => {
    const api = client()
    const props = { api: api as unknown as ApiClient, schema, section: 'mappings' as const, active: true, locked: false, onChange: vi.fn() }
    const view = render(<SchemaStorageEditor {...props} draft />)
    expect(screen.getByRole('table', { name: '待发布的平台存储映射' }).textContent).toContain('尚未生效')
    expect(api.getSchemaStorageMapping).not.toHaveBeenCalled()
    view.rerender(<SchemaStorageEditor {...props} draft currentId="notes" currentETag="active-etag" />)
    await screen.findByRole('table', { name: '当前生效的数据库映射' })
    expect(screen.getByText(/草稿尚未发布；以下显示当前生效定义/)).toBeTruthy()
  })

  it('定义摘要不匹配时拒绝展示旧映射', async () => {
    const api = client()
    render(<SchemaStorageEditor api={api as unknown as ApiClient} schema={schema} section="mappings" active locked currentId="notes" currentETag="new-etag" onChange={vi.fn()} />)
    expect((await screen.findByRole('alert')).textContent).toContain('当前定义已更新')
    expect(screen.queryByRole('table', { name: '当前生效的数据库映射' })).toBeNull()
  })

  it('切换模型后忽略迟到的旧映射响应', async () => {
    const api = client()
    let finish!: (value: SchemaStorageMapping) => void
    api.getSchemaStorageMapping.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const props = { api: api as unknown as ApiClient, schema, section: 'mappings' as const, active: true, locked: true, onChange: vi.fn() }
    const view = render(<SchemaStorageEditor {...props} currentId="old" />)
    view.rerender(<SchemaStorageEditor {...props} currentId="notes" currentETag="active-etag" />)
    await screen.findByRole('table', { name: '当前生效的数据库映射' })
    await act(async () => finish({ ...physical, schemaId: 'old', properties: [{ ...physical.properties[0], propertyName: '迟到的旧模型属性' }] }))
    expect(screen.queryByText('迟到的旧模型属性')).toBeNull()
  })

  it('反向建模通过表列目录选择，禁止手填并禁用不兼容和生成字段', async () => {
    const api = client()
    api.inspectSourceTable.mockResolvedValue(catalog('notes', [column('title'), column('amount', { dataType: 'int64', databaseType: 'bigint' }), column('generated_title', { generated: true })]))
    render(<Harness api={api} initial={mapped()} />)
    const field = screen.getByLabelText('属性1字段选择')
    await within(field).findByRole('option', { name: 'title · varchar(255)' })
    expect((screen.getByLabelText('属性1来源表') as HTMLInputElement).readOnly).toBe(true)
    expect((screen.getByLabelText('属性1映射列') as HTMLInputElement).readOnly).toBe(true)
    expect((within(field).getByRole('option', { name: /amount/ }) as HTMLOptionElement).disabled).toBe(true)
    expect((within(field).getByRole('option', { name: /generated_title/ }) as HTMLOptionElement).disabled).toBe(true)
    await userEvent.selectOptions(screen.getByLabelText('属性1来源表选择'), JSON.stringify(['business', 'notes']))
    await userEvent.selectOptions(field, 'title')
    expect(JSON.parse(screen.getByLabelText('映射结果').textContent!).properties[0].mapping).toMatchObject({ dataSourceId: 'mysql', namespace: 'business', table: 'notes', column: 'title' })
  })

  it('跨源切换立即清空旧表、字段和关联键，旧目录失败不覆盖新选择', async () => {
    const api = client()
    let failOld!: (cause: Error) => void
    api.inspectSourceTable.mockImplementationOnce(() => new Promise((_resolve, reject) => { failOld = reject }))
    const initial = mapped()
    initial.properties[0].mapping = { dataSourceId: 'mysql', table: 'notes', column: 'old_title', keyColumn: 'old_key', tenantColumn: 'old_tenant' }
    api.listSourceTables.mockImplementation(async (id) => [{ namespace: id === 'postgres' ? 'public' : 'business', name: id === 'postgres' ? 'assets' : 'notes', type: 'table' }])
    render(<Harness api={api} initial={initial} />)
    await within(screen.getByLabelText('属性1数据源')).findByRole('option', { name: /资产库/ })
    await userEvent.selectOptions(screen.getByLabelText('属性1数据源'), 'postgres')
    expect(JSON.parse(screen.getByLabelText('映射结果').textContent!).properties[0].mapping).toEqual({ dataSourceId: 'postgres', namespace: 'public', column: '' })
    await within(screen.getByLabelText('属性1来源表选择')).findByRole('option', { name: 'public.assets' })
    await userEvent.selectOptions(screen.getByLabelText('属性1来源表选择'), JSON.stringify(['public', 'assets']))
    await within(screen.getByLabelText('属性1字段选择')).findByRole('option', { name: 'title · varchar(255)' })
    await act(async () => failOld(new Error('旧数据源目录失败')))
    expect(screen.queryByText('旧数据源目录失败')).toBeNull()
  })

  it('正向建模保留新增物理字段输入，已生效映射可读取目录但不可改绑', async () => {
    const api = client()
    const view = render(<Harness api={api} initial={mapped('schema_2_db')} />)
    const field = screen.getByLabelText('属性1映射列') as HTMLInputElement
    expect(field.readOnly).toBe(false)
    fireEvent.change(field, { target: { value: 'new_title' } })
    expect(JSON.parse(screen.getByLabelText('映射结果').textContent!).properties[0].mapping.column).toBe('new_title')
    view.unmount()
    render(<Harness api={api} initial={mapped('schema_2_db')} locked />)
    await within(screen.getByLabelText('属性1字段选择')).findByRole('option', { name: 'title · varchar(255)' })
    expect((screen.getByLabelText('属性1数据源') as HTMLSelectElement).disabled).toBe(true)
    expect((screen.getByLabelText('属性1字段选择') as HTMLSelectElement).disabled).toBe(true)
    const refresh = screen.getByRole('button', { name: '读取列目录' }) as HTMLButtonElement
    await waitFor(() => expect(refresh.disabled).toBe(false))
    await userEvent.click(refresh)
    await waitFor(() => expect(api.inspectSourceTable).toHaveBeenCalledTimes(3))
  })

  it('UUID允许char(36)来源，MySQL长文本不误选较短TEXT列', async () => {
    const api = client()
    api.inspectSourceTable.mockResolvedValue(catalog('notes', [column('uuid_column', { maxLength: 36, databaseType: 'char(36)' }), column('short_text', { dataType: 'text', databaseType: 'text' }), column('long_text', { dataType: 'longtext', databaseType: 'longtext' })]))
    const initial = mapped()
    initial.properties = [{ ...initial.properties[0], dataType: 'uuid' }, { id: 'body', name: '正文', dataType: 'longtext', mapping: { dataSourceId: 'mysql', column: '' } }]
    render(<Harness api={api} initial={initial} />)
    const uuid = await within(screen.getByLabelText('属性1字段选择')).findByRole('option', { name: 'uuid_column · char(36)' })
    expect((uuid as HTMLOptionElement).disabled).toBe(false)
    const body = screen.getByLabelText('属性2字段选择')
    await within(body).findByRole('option', { name: 'long_text · longtext' })
    expect((within(body).getByRole('option', { name: /short_text/ }) as HTMLOptionElement).disabled).toBe(true)
    expect((within(body).getByRole('option', { name: 'long_text · longtext' }) as HTMLOptionElement).disabled).toBe(false)
    expect(api.inspectSourceTable).toHaveBeenCalledTimes(1)
  })
})
