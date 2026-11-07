import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ApiClient } from '../../lib/api'
import type { DefinitionRecord, Link } from '../../lib/types'
import { ModelEditor } from './ModelEditor'

afterEach(cleanup)

const categories = [
  { id: 'production', tenantId: 'tenant', name: '生产管理' },
  { id: 'quality', tenantId: 'tenant', name: '质量管理' },
]
function schema(id = 'equipment', name = '生产设备', categoryId = 'production'): DefinitionRecord {
  return { status: 'draft', etag: `etag-${id}`, definition: {
    id, name, tenantId: 'tenant', categoryId, groupId: 'group', kind: 'schema',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: 'name-field', name: '设备名称', dataType: 'string' },
    ], indexes: [{ id: 'name-index', name: '设备名称索引', unique: false, fieldIds: ['name-field'] }] },
  } }
}
function link(id: string, name: string, sourceSchemaId: string, targetSchemaId: string, status = 'active'): DefinitionRecord {
  return { status, etag: `etag-${id}`, definition: {
    id, name, tenantId: 'tenant', categoryId: 'production', groupId: 'group', kind: 'link',
    link: { sourceSchemaId, targetSchemaId, forwardName: `${name}正向`, reverseName: `${name}反向`, cardinality: '1:N', onDelete: 'restrict' } as Link,
  } }
}
function action(id: string, name: string, targets?: string[], status = 'active'): DefinitionRecord {
  return { status, etag: `etag-${id}`, definition: {
    id, name, tenantId: 'tenant', categoryId: 'production', groupId: 'group', kind: 'action',
    action: { input: [], output: [], sideEffects: 'dataWrite', ...(targets ? { targetSchemaIds: targets } : {}) },
  } }
}
function fixture(initial = schema(), entries = [initial]) {
  const api = {
    listGroups: vi.fn().mockResolvedValue([{ id: 'group', categoryId: 'production', tenantId: 'tenant', name: '基础资料' }]),
    listDataSources: vi.fn().mockResolvedValue([]), listSourceTables: vi.fn().mockResolvedValue([]),
    getCurrent: vi.fn().mockResolvedValue(initial), query: vi.fn().mockResolvedValue({ fields: initial.definition.schema?.properties ?? [], data: [], hasMore: false }),
    schemaLinks: vi.fn().mockResolvedValue([]),
    getSchemaStorageMapping: vi.fn().mockResolvedValue({ schemaId: initial.definition.id, definitionETag: initial.etag, managed: true, mode: 'schema_2_db', primary: { dataSourceName: '平台默认存储', driver: 'mysql', namespace: 'data', table: 'entities', keyColumn: 'id' }, properties: [] }),
    saveDraft: vi.fn().mockImplementation(async (_id, definition) => ({ ...initial, definition, etag: 'saved' })),
  }
  const props = {
    api: api as unknown as ApiClient, categoryId: initial.definition.categoryId, categories, entries,
    initialKind: initial.definition.kind, initialRecord: initial, onClose: vi.fn(), onOrganizations: vi.fn(),
    onSaved: vi.fn().mockResolvedValue(undefined), onPending: vi.fn(), onPublished: vi.fn().mockResolvedValue(undefined),
  }
  return { api, props }
}
async function selectTab(name: string) {
  await userEvent.click(screen.getByRole('tab', { name }))
  return screen.getByRole('tabpanel', { name })
}
async function editProperty(index = 1) {
  await userEvent.click(screen.getByRole('button', { name: `编辑对象属性第${index}项` }))
  return screen.findByRole('dialog', { name: '编辑属性' })
}

describe('对象编辑页签', () => {
  it('从其他页签保存时切到存在无效属性或索引的配置页，并保留本地内容', async () => {
    const { api, props } = fixture()
    render(<ModelEditor {...props} />)
    await selectTab('属性配置')
    const invalidDialog = await editProperty()
    await userEvent.clear(within(invalidDialog).getByLabelText('对象属性第1项名称'))
    await userEvent.click(within(invalidDialog).getByRole('button', { name: '应用修改' }))
    await selectTab('动作信息')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    expect(screen.getByRole('tab', { name: '属性配置', selected: true })).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toContain('请填写属性的业务名称')
    const dialog = await editProperty()
    await userEvent.type(within(dialog).getByLabelText('对象属性第1项名称'), '设备中文名称')
    await userEvent.click(within(dialog).getByRole('button', { name: '应用修改' }))
    await selectTab('索引信息')
    await userEvent.clear(screen.getByLabelText('索引1名称'))
    await selectTab('Link 信息')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    expect(screen.getByRole('tab', { name: '索引信息', selected: true })).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toContain('请填写索引名称')
    const propertiesPanel = await selectTab('属性配置')
    expect(within(within(propertiesPanel).getByRole('table', { name: '对象属性列表' })).getByText('设备中文名称')).toBeTruthy()
    expect(api.saveDraft).not.toHaveBeenCalled()
  })

  it('默认基础信息，只有一个可见面板并支持方向键和首尾键切换', async () => {
    const { props } = fixture()
    render(<ModelEditor {...props} />)
    const tabs = within(screen.getByRole('tablist', { name: '模型配置' })).getAllByRole('tab')
    expect(tabs.map((tab) => tab.textContent?.replace(/\d+/g, '').trim())).toEqual(['基础信息', '属性配置', '索引信息', 'Link 信息', '动作信息', '数据预览'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(tabs[0].tabIndex).toBe(0)
    expect(tabs.slice(1).every((tab) => tab.tabIndex === -1)).toBe(true)
    expect(screen.getAllByRole('tabpanel').length).toBe(1)
    expect(screen.getByRole('tabpanel', { name: '基础信息' }).id).toBe(tabs[0].getAttribute('aria-controls'))
    tabs[0].focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(tabs[1])
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')
    expect(screen.getByRole('tabpanel', { name: '属性配置' }).getAttribute('aria-labelledby')).toBe(tabs[1].id)
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(tabs[5])
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(tabs[0])
    await userEvent.keyboard('{ArrowLeft}{Home}')
    expect(document.activeElement).toBe(tabs[0])
  })

  it('跨页签保留基础、属性和索引编辑，从关系页签保存完整草稿', async () => {
    const { api, props } = fixture()
    render(<ModelEditor {...props} />)
    await userEvent.clear(screen.getByLabelText('业务名称'))
    await userEvent.type(screen.getByLabelText('业务名称'), '设备台账')
    await selectTab('属性配置')
    const dialog = await editProperty()
    await userEvent.clear(within(dialog).getByLabelText('对象属性第1项名称'))
    await userEvent.type(within(dialog).getByLabelText('对象属性第1项名称'), '设备中文名称')
    await userEvent.click(within(dialog).getByRole('button', { name: '应用修改' }))
    await selectTab('索引信息')
    await userEvent.clear(screen.getByLabelText('索引1名称'))
    await userEvent.type(screen.getByLabelText('索引1名称'), '中文名称索引')
    await selectTab('基础信息')
    expect((screen.getByLabelText('业务名称') as HTMLInputElement).value).toBe('设备台账')
    const propertiesPanel = await selectTab('属性配置')
    expect(within(within(propertiesPanel).getByRole('table', { name: '对象属性列表' })).getByText('设备中文名称')).toBeTruthy()
    await selectTab('Link 信息')
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({
      name: '设备台账', schema: expect.objectContaining({
        properties: [expect.objectContaining({ id: 'name-field', name: '设备中文名称' })],
        indexes: [{ id: 'name-index', name: '中文名称索引', unique: false, fieldIds: ['name-field'] }],
      }),
    }), 'etag-equipment'))
    expect(screen.getByRole('tab', { name: 'Link 信息', selected: true })).toBeTruthy()
  })

  it('属性以行展示，取消编辑和新增不改变草稿，确认后才添加属性', async () => {
    const { api, props } = fixture()
    render(<ModelEditor {...props} />)
    const panel = await selectTab('属性配置')
    const propertiesTable = within(panel).getByRole('table', { name: '对象属性列表' })
    expect(within(propertiesTable).getByText('设备名称')).toBeTruthy()
    expect(within(panel).queryByLabelText('对象属性第1项名称')).toBeNull()
    expect(within(panel).queryByLabelText('属性1数据源')).toBeNull()

    const editDialog = await editProperty()
    await userEvent.clear(within(editDialog).getByLabelText('对象属性第1项名称'))
    await userEvent.type(within(editDialog).getByLabelText('对象属性第1项名称'), '取消后的名称')
    await userEvent.click(within(editDialog).getByRole('button', { name: '取消' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(within(propertiesTable).getByText('设备名称')).toBeTruthy()
    expect(within(propertiesTable).queryByText('取消后的名称')).toBeNull()

    await userEvent.click(within(panel).getByRole('button', { name: '添加属性' }))
    const cancelledAdd = await screen.findByRole('dialog', { name: '添加属性' })
    await userEvent.type(within(cancelledAdd).getByLabelText('对象属性第2项名称'), '取消新增属性')
    await userEvent.click(within(cancelledAdd).getByRole('button', { name: '取消' }))
    expect(within(panel).queryByRole('button', { name: '编辑对象属性第2项' })).toBeNull()

    await userEvent.click(within(panel).getByRole('button', { name: '添加属性' }))
    const addDialog = await screen.findByRole('dialog', { name: '添加属性' })
    await userEvent.type(within(addDialog).getByLabelText('对象属性第2项名称'), '设备编码')
    await userEvent.click(within(addDialog).getByRole('button', { name: '添加属性' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(within(propertiesTable).getByText('设备编码')).toBeTruthy()
    expect(api.saveDraft).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('equipment', expect.objectContaining({
      schema: expect.objectContaining({ properties: [
        expect.objectContaining({ id: 'name-field', name: '设备名称' }),
        expect.objectContaining({ name: '设备编码' }),
      ] }),
    }), 'etag-equipment'))
  })

  it('只在进入数据预览时读取当前定义与数据，返回基础信息保留未保存编辑', async () => {
    const initial = schema()
    initial.status = 'active'
    const { api, props } = fixture(initial)
    api.query.mockResolvedValue({ fields: initial.definition.schema!.properties, data: [{ id: 'equipment-1', schemaId: 'equipment', etag: 'row-1', values: { 'name-field': '一号设备' } }], hasMore: false })
    render(<ModelEditor {...props} />)
    expect(api.getCurrent).not.toHaveBeenCalled()
    expect(api.query).not.toHaveBeenCalled()
    await userEvent.clear(screen.getByLabelText('业务名称'))
    await userEvent.type(screen.getByLabelText('业务名称'), '设备台账')
    expect(api.getCurrent).not.toHaveBeenCalled()
    expect(api.query).not.toHaveBeenCalled()
    const panel = await selectTab('数据预览')
    expect(await within(panel).findByRole('table', { name: '对象数据预览' })).toBeTruthy()
    expect(api.getCurrent).toHaveBeenCalledExactlyOnceWith('equipment', expect.any(AbortSignal))
    expect(api.query).toHaveBeenCalledExactlyOnceWith({ from: { schemaId: 'equipment' }, select: ['name-field'], page: { limit: 10 } }, expect.any(AbortSignal))
    expect(within(panel).getByText(/草稿尚未发布；此处读取当前生效模型的数据/)).toBeTruthy()
    expect(within(panel).getByRole('cell', { name: '一号设备' })).toBeTruthy()
    await selectTab('基础信息')
    expect((screen.getByLabelText('业务名称') as HTMLInputElement).value).toBe('设备台账')
    expect(api.saveDraft).not.toHaveBeenCalled()
    expect(screen.queryByRole('table', { name: '对象数据预览' })).toBeNull()
  })

  it('关系按稳定ID包含入站、出站、自关联及跨分类，排除无关和异租户定义', async () => {
    const initial = schema()
    const other = schema('report', '检验报告', 'quality')
    const outgoing = link('outgoing', '设备报告', 'equipment', 'report')
    outgoing.definition.categoryId = 'quality'
    const incoming = link('incoming', '报告设备', 'report', 'equipment', 'draft')
    const self = link('self', '设备配套', 'equipment', 'equipment')
    const unrelated = link('unrelated', '无关关系', 'report', 'report')
    const foreign = link('foreign', '其他租户关系', 'equipment', 'report')
    foreign.definition.tenantId = 'another-tenant'
    const { props } = fixture(initial, [initial, other, outgoing, incoming, self, unrelated, foreign])
    render(<ModelEditor {...props} />)
    const panel = await selectTab('Link 信息')
    expect(within(panel).getByRole('link', { name: '设备报告' }).getAttribute('href')).toBe('#models/outgoing')
    expect(within(panel).getByRole('link', { name: '报告设备' }).getAttribute('href')).toBe('#models/incoming')
    expect(within(panel).getAllByRole('link', { name: '设备配套' }).length).toBe(1)
    expect(within(panel).queryByText('无关关系')).toBeNull()
    expect(within(panel).queryByText('其他租户关系')).toBeNull()
    expect(panel.textContent).toContain('检验报告')
    expect(panel.textContent).toContain('设备报告正向')
    expect(panel.textContent).toContain('报告设备反向')
    expect(panel.textContent).toContain('草稿')
  })

  it('动作只展示明确关联该对象的定义，空目标不作为全局关联', async () => {
    const initial = schema()
    const related = action('calibrate', '设备校准', ['equipment', 'report'])
    related.definition.categoryId = 'quality'
    const draft = action('inspect', '设备检测', ['equipment'], 'draft')
    const foreign = action('foreign-action', '其他租户动作', ['equipment'])
    foreign.definition.tenantId = 'another-tenant'
    const { props } = fixture(initial, [initial, related, draft, action('other', '报告归档', ['report']), action('generic', '未关联动作'), action('empty', '空目标动作', []), foreign])
    const view = render(<ModelEditor {...props} />)
    let panel = await selectTab('动作信息')
    expect(within(panel).getByRole('link', { name: '设备校准' }).getAttribute('href')).toBe('#models/calibrate')
    expect(within(panel).getByRole('link', { name: '设备检测' }).getAttribute('href')).toBe('#models/inspect')
    expect(within(panel).queryByText('报告归档')).toBeNull()
    expect(within(panel).queryByText('未关联动作')).toBeNull()
    expect(within(panel).queryByText('空目标动作')).toBeNull()
    expect(within(panel).queryByText('其他租户动作')).toBeNull()
    // A refreshed authorized directory immediately removes old related details.
    view.rerender(<ModelEditor {...props} entries={[initial]} />)
    panel = screen.getByRole('tabpanel', { name: '动作信息' })
    expect(within(panel).queryByRole('link')).toBeNull()
    expect(panel.textContent).toMatch(/暂无|尚未/)
    const links = await selectTab('Link 信息')
    expect(within(links).queryByRole('link')).toBeNull()
    expect(links.textContent).toMatch(/暂无|尚未/)
  })

  it('关联定义有待发布草稿时仍显示当前关联，并区分两种状态', async () => {
    const initial = schema()
    const activeLink = link('moving-link', '当前关系', 'equipment', 'report')
    const nextLink = link('moving-link', '草稿关系', 'report', 'report', 'draft')
    nextLink.hasCurrent = true
    const activeAction = action('moving-action', '当前动作', ['equipment'])
    const nextAction = action('moving-action', '草稿动作', ['equipment'], 'draft')
    nextAction.hasCurrent = true
    const { props } = fixture(initial, [initial, nextLink, nextAction])
    render(<ModelEditor {...props} currentEntries={[activeLink, activeAction]} />)
    let panel = await selectTab('Link 信息')
    expect(within(panel).getByRole('link', { name: '当前关系' })).toBeTruthy()
    expect(within(panel).queryByRole('link', { name: '草稿关系' })).toBeNull()
    expect(panel.textContent).toContain('已生效')
    panel = await selectTab('动作信息')
    expect(within(panel).getByRole('link', { name: '当前动作' })).toBeTruthy()
    expect(within(panel).getByRole('link', { name: '草稿动作' })).toBeTruthy()
    expect(panel.textContent).toContain('已生效')
    expect(panel.textContent).toContain('待发布变更')
  })

  it('已生效动作可按中文名维护关联对象，保留不可见引用并锁定运行声明', async () => {
    const initial = action('calibrate', '设备校准', ['equipment', 'invisible-schema'])
    const equipment = schema()
    equipment.status = 'active'
    const report = schema('report', '检验报告', 'quality')
    report.status = 'active'
    const foreign = schema('foreign', '其他租户设备')
    foreign.status = 'active'
    foreign.definition.tenantId = 'another-tenant'
    const { api, props } = fixture(initial, [initial, equipment, report, foreign, schema('unpublished', '尚未生效的模型')])
    render(<ModelEditor {...props} />)
    expect(screen.queryByRole('tablist')).toBeNull()
    expect((screen.getByLabelText('产物引用') as HTMLInputElement).disabled).toBe(true)
    const selected = screen.getByRole('checkbox', { name: '关联对象：生产管理 / 生产设备' }) as HTMLInputElement
    expect(selected.checked).toBe(true)
    expect(selected.disabled).toBe(false)
    expect(screen.queryByRole('checkbox', { name: /其他租户设备/ })).toBeNull()
    expect(screen.queryByRole('checkbox', { name: /尚未生效的模型/ })).toBeNull()
    await userEvent.click(screen.getByRole('checkbox', { name: '关联对象：质量管理 / 检验报告' }))
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('calibrate', expect.objectContaining({ action: expect.objectContaining({ targetSchemaIds: ['equipment', 'invisible-schema', 'report'], sideEffects: 'dataWrite' }) }), 'etag-calibrate'))
    await userEvent.click(screen.getByRole('checkbox', { name: '当前关联对象 1（目录不可见）' }))
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenLastCalledWith('calibrate', expect.objectContaining({ action: expect.objectContaining({ targetSchemaIds: ['equipment', 'report'] }) }), 'saved'))
  })
})
