import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceQueryResult, SourceTable, SourceTableInfo } from '../../lib/types'
import { SourceManager, sourcePreviewSQL } from './SourceManager'
import { SourcesPage } from './SourcesPage'

vi.mock('./SourceSQLEditor', () => ({
  SourceSQLEditor: ({ value, onChange, disabled }: { value: string; onChange: (value: string) => void; disabled: boolean }) => <textarea aria-label="SQL 语句" value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} />,
}))

const source: DataSource = { id: 'source', tenantId: 'tenant', name: '业务库', driver: 'mysql', host: 'localhost', port: 3306, database: 'business', username: 'reader', tlsMode: 'disable', tenantIsolation: 'dedicated', enabled: true, passwordSet: true, etag: 'source-etag' }
const table: SourceTable = { namespace: 'business', name: 'orders', signature: 'signature', columns: [
  { name: 'id', databaseType: 'bigint', dataType: 'int64', nullable: false, primaryKey: true, generated: false, position: 1, comment: '订单主键' },
  { name: 'amount', databaseType: 'decimal(24,4)', dataType: 'decimal', nullable: true, primaryKey: false, generated: false, position: 2, defaultValue: '0.0000', comment: '订单金额' },
  { name: 'memo', databaseType: 'varchar(255)', dataType: 'string', nullable: true, primaryKey: false, generated: false, position: 3, defaultValue: null, comment: '业务备注' },
], indexes: [{ name: 'PRIMARY', unique: true, primary: true, columns: ['id'] }, { name: 'idx_amount', unique: false, primary: false, columns: ['amount'] }] }
const result: SourceQueryResult = { columns: ['amount'], rows: [['9007199254740993.0001'], [null], ['']], limit: 100, hasMore: true, elapsedMs: 3 }
const overview = { sourceId: 'source', etag: 'source-etag', checkedAt: '2026-11-08T02:00:00Z', serverVersion: '26.7.0', database: 'business', charset: 'utf8mb4', metrics: {
  connections: { available: true, value: '12', unit: 'connections', scope: 'instance', basis: '当前数据库实例连接数' },
  storageBytes: { available: true, value: '1048576', unit: 'bytes', scope: 'database', basis: '当前数据库存储占用' },
  qps: { available: false, unit: 'queries/s', scope: 'instance', basis: '两个采样点间查询增量', reason: '数据库账号缺少统计权限' },
} }
const info: SourceTableInfo = { namespace: table.namespace, name: table.name, type: 'table', columnCount: 3, primaryKeyCount: 1 }
function fixture(items: SourceTableInfo[] = [info]) {
  return {
    listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: vi.fn().mockResolvedValue([]),
    getDataSource: vi.fn().mockResolvedValue(source), getSourceOverview: vi.fn().mockResolvedValue(overview), listSourceTables: vi.fn().mockResolvedValue(items),
    inspectSourceTable: vi.fn().mockImplementation((_id: string, name: string, namespace: string) => Promise.resolve({ ...table, name, namespace })), queryDataSource: vi.fn().mockResolvedValue(result),
  }
}
function mount(mocks = fixture(), saved = source) {
  const api = mocks as unknown as ApiClient, onBack = vi.fn(), onDenied = vi.fn()
  const rendered = render(<SourceManager api={api} source={saved} active onBack={onBack} onDenied={onDenied} />)
  return { ...rendered, api, onBack, onDenied, mocks }
}
async function ready() { await screen.findByRole('cell', { name: '订单金额' }) }
function selectTab(name: string) { fireEvent.click(screen.getByRole('tab', { name })) }
function fillCurrentQuery() {
  fireEvent.click(screen.getByRole('button', { name: '填入查询' }))
  fireEvent.click(screen.getByRole('menuitem', { name: '当前表查询' }))
}

describe('数据源表格与数据库管理', () => {
  it('预览 SQL 按数据库方言引用命名空间与表名，标识中的引号不会逃逸', () => {
    expect(sourcePreviewSQL('mysql', { namespace: '业务`库', name: 'order`items', type: 'table' })).toBe('SELECT * FROM `业务``库`.`order``items` LIMIT 100;')
    expect(sourcePreviewSQL('postgresql', { namespace: '业务"库', name: 'order"items', type: 'table' })).toBe('SELECT * FROM "业务""库"."order""items" LIMIT 100;')
  })

  it('列表测试已保存配置并显示真实成功或失败，不打开编辑器', async () => {
    const mocks = fixture()
    const compatibility = { connected: true, compatibilityStatus: 'supported', serverVersion: '26.7.0', capabilities: [], checkedAt: '2026-11-07T08:00:00Z' }
    const testDataSource = vi.fn().mockResolvedValueOnce({ success: true, serverVersion: '26.7.0', compatibility }).mockResolvedValueOnce({ success: false, serverVersion: '', compatibility: { ...compatibility, connected: false, compatibilityStatus: 'failed', message: '数据库连接失败' } })
    render(<SourcesPage api={{ ...mocks, testDataSource } as unknown as ApiClient} administrator />)
    const row = await screen.findByRole('row', { name: /业务库/ })
    fireEvent.click(within(row).getByRole('button', { name: '测试' }))
    await screen.findByText('业务库：连接成功 · MySQL 26.7.0')
    expect(testDataSource).toHaveBeenCalledWith('source')
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: '测试' }))
    expect((await screen.findByRole('alert')).textContent).toContain('数据库连接失败')
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('进入管理清除未保存密码，读取真实概览并只自动加载首表结构', async () => {
    const mocks = fixture()
    render(<SourcesPage api={mocks as unknown as ApiClient} administrator />)
    const row = await screen.findByRole('row', { name: /业务库/ })
    fireEvent.click(within(row).getByRole('button', { name: '编辑 业务库' }))
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'unsaved-secret' } })
    fireEvent.click(within(row).getByRole('button', { name: '管理' }))
    await ready()
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
    expect(screen.getByRole('heading', { name: '数据库管理', level: 1 })).toBeTruthy()
    expect(mocks.getDataSource).toHaveBeenCalledWith('source', expect.any(AbortSignal))
    expect(mocks.getSourceOverview).toHaveBeenCalledWith('source', expect.any(AbortSignal))
    expect(mocks.inspectSourceTable).toHaveBeenCalledWith('source', 'orders', 'business', expect.any(AbortSignal))
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
    expect(screen.getByRole('tab', { name: '字段详情' }).getAttribute('aria-selected')).toBe('true')
    const card = screen.getByRole('region', { name: '数据库基础信息' })
    for (const value of ['26.7.0', 'utf8mb4', '12']) expect(card.textContent).toContain(value)
    const qps = within(card).getByText('QPS').parentElement!
    expect(within(qps).getByRole('definition').textContent).toBe('暂不可用')
    expect(qps.textContent).toContain('数据库账号缺少统计权限')
    expect(card.textContent).not.toContain('unsaved-secret')
    fireEvent.click(screen.getByRole('button', { name: '返回数据源列表' }))
    await screen.findByRole('row', { name: /业务库/ })
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
  })

  it('实时概览暂不可用仍可独立读取表结构，缺失指标不会伪装成零', async () => {
    const mocks = fixture()
    mocks.getSourceOverview.mockRejectedValue(new ApiError(503, 'overview_unavailable', '实时统计服务暂不可用'))
    mount(mocks)
    await ready()
    const card = screen.getByRole('region', { name: '数据库基础信息' })
    expect(card.textContent).toContain('实时统计服务暂不可用')
    expect(within(card).getAllByText('暂不可用')).toHaveLength(5)
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
  })

  it('目录显示真实字段数和主键列数，20项分页及搜索会复位页码', async () => {
    const items = Array.from({ length: 45 }, (_, index) => ({ ...info, name: `orders_${String(index + 1).padStart(2, '0')}` }))
    mount(fixture(items))
    await ready()
    const catalog = screen.getByRole('region', { name: '数据库表目录' })
    for (const header of ['字段数', '主键数']) expect(within(catalog).getByRole('columnheader', { name: header })).toBeTruthy()
    const first = within(catalog).getByRole('row', { name: /orders_01/ })
    expect(within(first).getByRole('cell', { name: '3' })).toBeTruthy()
    expect(within(first).getByRole('cell', { name: '1' })).toBeTruthy()
    expect(within(catalog).getAllByRole('button', { name: /^business\.orders_/ })).toHaveLength(20)
    fireEvent.click(within(catalog).getByRole('button', { name: '下一页' }))
    expect(within(catalog).queryByRole('button', { name: 'business.orders_01' })).toBeNull()
    expect(within(catalog).getByRole('button', { name: 'business.orders_21' })).toBeTruthy()
    fireEvent.click(within(catalog).getByRole('button', { name: '下一页' }))
    expect(within(catalog).getAllByRole('button', { name: /^business\.orders_/ })).toHaveLength(5)
    fireEvent.change(screen.getByLabelText('搜索数据库表'), { target: { value: 'orders_01' } })
    expect(within(catalog).getByRole('button', { name: 'business.orders_01' })).toBeTruthy()
    expect((within(catalog).getByRole('button', { name: '上一页' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('搜索数据库表'), { target: { value: 'not-found' } })
    expect(within(catalog).queryByRole('button', { name: /^business\.orders_/ })).toBeNull()
    fireEvent.change(screen.getByLabelText('搜索数据库表'), { target: { value: '' } })
    expect(within(catalog).getByRole('button', { name: 'business.orders_01' })).toBeTruthy()
  })

  it('字段详情显示类型、空值、默认值、索引和注释，可按注释与字段名搜索', async () => {
    mount()
    await ready()
    const panel = screen.getByRole('tabpanel', { name: '字段详情' })
    const fieldTable = within(panel).getAllByRole('table')[0]
    for (const header of ['字段名', '类型', '可空', '默认值', '索引', '注释']) expect(within(panel).getByRole('columnheader', { name: header })).toBeTruthy()
    const amount = within(fieldTable).getByRole('row', { name: /amount/ })
    for (const value of ['decimal(24,4)', '0.0000', 'idx_amount', '订单金额']) expect(amount.textContent).toContain(value)
    fireEvent.change(screen.getByLabelText('搜索字段'), { target: { value: '订单金额' } })
    expect(within(fieldTable).getByRole('cell', { name: 'amount' })).toBeTruthy()
    expect(within(fieldTable).queryByRole('cell', { name: 'memo' })).toBeNull()
    fireEvent.change(screen.getByLabelText('搜索字段'), { target: { value: 'memo' } })
    expect(within(fieldTable).getByRole('cell', { name: 'memo' })).toBeTruthy()
    expect(within(fieldTable).queryByRole('cell', { name: 'amount' })).toBeNull()
  })

  it('键盘方向键和 Home/End 切换 Tabs，仅进入预览时自动读取业务数据', async () => {
    const { mocks } = mount()
    await ready()
    const tabs = within(screen.getByRole('tablist', { name: '数据库管理视图' }))
    const fields = tabs.getByRole('tab', { name: '字段详情' })
    fireEvent.keyDown(fields, { key: 'ArrowRight' })
    const preview = tabs.getByRole('tab', { name: '表数据预览' })
    expect(preview.getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(preview)
    fireEvent.keyDown(preview, { key: 'End' })
    const sql = tabs.getByRole('tab', { name: 'SQL控制台' })
    expect(sql.getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(sql, { key: 'Home' })
    expect(fields.getAttribute('aria-selected')).toBe('true')
    expect(mocks.queryDataSource).toHaveBeenCalledTimes(1)
  })

  it('每次进入预览自动刷新，SQL独立执行，编辑SQL清旧结果并保留精确值', async () => {
    const { mocks } = mount()
    mocks.queryDataSource.mockResolvedValueOnce(result).mockResolvedValueOnce(result).mockResolvedValueOnce({ ...result, rows: [['最新预览']] })
    await ready()
    selectTab('表数据预览')
    expect(screen.queryByRole('button', { name: '预览表数据' })).toBeNull()
    await screen.findByText('9007199254740993.0001')
    expect(mocks.queryDataSource).toHaveBeenLastCalledWith('source', 'SELECT * FROM `business`.`orders` LIMIT 100;', 'source-etag', expect.any(AbortSignal), 100)
    expect(screen.getByText('NULL')).toBeTruthy()
    expect(screen.getByText('空字符串')).toBeTruthy()
    selectTab('SQL控制台')
    expect(screen.queryByText('9007199254740993.0001')).toBeNull()
    expect((screen.getByLabelText('SQL 语句') as HTMLTextAreaElement).value).toBe('')
    fireEvent.change(screen.getByLabelText('SQL 语句'), { target: { value: 'SELECT amount FROM orders LIMIT 2;' } })
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    await screen.findByText('9007199254740993.0001')
    expect(mocks.queryDataSource).toHaveBeenLastCalledWith('source', 'SELECT amount FROM orders LIMIT 2;', 'source-etag', expect.any(AbortSignal), 100)
    fireEvent.change(screen.getByLabelText('SQL 语句'), { target: { value: 'SELECT amount FROM orders LIMIT 3;' } })
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
    selectTab('表数据预览')
    expect(screen.queryByText('9007199254740993.0001')).toBeNull()
    await screen.findByText('最新预览')
    expect(mocks.queryDataSource).toHaveBeenCalledTimes(3)
  })

  it('切换 Tab 取消查询并拒绝不遵守取消信号的迟到结果', async () => {
    const mocks = fixture()
    let complete!: (value: SourceQueryResult) => void
    mocks.queryDataSource.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve }))
    mount(mocks)
    await ready()
    selectTab('SQL控制台')
    fillCurrentQuery()
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    const signal = mocks.queryDataSource.mock.calls[0][3] as AbortSignal
    selectTab('表数据预览')
    expect(signal.aborted).toBe(true)
    await screen.findByText('9007199254740993.0001')
    await act(async () => complete({ ...result, rows: [['迟到SQL结果']] }))
    expect(screen.queryByText('迟到SQL结果')).toBeNull()
    selectTab('SQL控制台')
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
  })

  it('重新进入预览取消前一次加载，迟到预览不能覆盖新结果', async () => {
    const mocks = fixture()
    let complete!: (value: SourceQueryResult) => void
    mocks.queryDataSource.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve }))
    mocks.queryDataSource.mockResolvedValueOnce({ ...result, rows: [['最新预览']] })
    mount(mocks)
    await ready()
    selectTab('表数据预览')
    const signal = mocks.queryDataSource.mock.calls[0][3] as AbortSignal
    selectTab('字段详情')
    expect(signal.aborted).toBe(true)
    selectTab('表数据预览')
    await screen.findByText('最新预览')
    await act(async () => complete({ ...result, rows: [['过期预览']] }))
    expect(screen.queryByText('过期预览')).toBeNull()
    expect(screen.getByText('最新预览')).toBeTruthy()
    expect(mocks.queryDataSource).toHaveBeenCalledTimes(2)
  })

  it('预览失败后重新进入才重试，页面不循环查询', async () => {
    const { mocks } = mount()
    mocks.queryDataSource.mockRejectedValueOnce(new ApiError(422, 'source_query_failed', '数据库读取暂时失败'))
    await ready()
    selectTab('表数据预览')
    await screen.findByRole('alert')
    expect(mocks.queryDataSource).toHaveBeenCalledTimes(1)
    selectTab('字段详情')
    selectTab('表数据预览')
    await screen.findByText('9007199254740993.0001')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(mocks.queryDataSource).toHaveBeenCalledTimes(2)
  })

  it('刷新结构重新读取配置、概览、当前目录和所选表，不自动查业务数据', async () => {
    const mocks = fixture([info, { ...info, name: 'customers' }])
    mount(mocks)
    await ready()
    fireEvent.click(screen.getByRole('button', { name: 'business.customers' }))
    await waitFor(() => expect(mocks.inspectSourceTable).toHaveBeenLastCalledWith('source', 'customers', 'business', expect.any(AbortSignal)))
    fireEvent.click(screen.getByRole('button', { name: '刷新结构' }))
    await waitFor(() => expect(mocks.getSourceOverview).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocks.inspectSourceTable).toHaveBeenCalledTimes(3))
    expect(mocks.getDataSource).toHaveBeenCalledTimes(2)
    expect(mocks.listSourceTables).toHaveBeenLastCalledWith('source', 'business', expect.any(AbortSignal))
    expect(mocks.inspectSourceTable).toHaveBeenLastCalledWith('source', 'customers', 'business', expect.any(AbortSignal))
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
  })

  it('共享源仍可看结构，预览与 SQL 均禁止读取业务数据', async () => {
    const mocks = fixture(), shared: DataSource = { ...source, tenantIsolation: 'column' }
    mocks.getDataSource.mockResolvedValue(shared)
    mount(mocks, shared)
    await ready()
    selectTab('表数据预览')
    expect(screen.queryByRole('button', { name: '预览表数据' })).toBeNull()
    expect(screen.getByText(/共享数据库请通过/)).toBeTruthy()
    selectTab('SQL控制台')
    expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByLabelText('SQL 语句') as HTMLTextAreaElement).disabled).toBe(true)
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
  })

  it.each([401, 403])('%i 拒绝立即清除目录、概览和业务结果并通知父级', async (status) => {
    const { mocks, onDenied } = mount()
    await ready()
    selectTab('SQL控制台')
    fillCurrentQuery()
    mocks.queryDataSource.mockRejectedValueOnce(new ApiError(status, 'forbidden', '管理权限已撤销'))
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    await waitFor(() => expect(onDenied).toHaveBeenCalledOnce())
    expect(screen.queryByRole('button', { name: 'business.orders' })).toBeNull()
    expect(screen.queryByRole('cell', { name: '订单金额' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
  })

  it('概览 403 不等待挂起的目录请求，立即撤销内容且迟到目录不能恢复', async () => {
    const mocks = fixture()
    let completeCatalog!: (items: SourceTableInfo[]) => void
    mocks.listSourceTables.mockImplementationOnce(() => new Promise<SourceTableInfo[]>((resolve) => { completeCatalog = resolve }))
    mocks.getSourceOverview.mockRejectedValueOnce(new ApiError(403, 'administrator_required', '概览管理权限已撤销'))
    const { onDenied } = mount(mocks)
    await waitFor(() => expect(onDenied).toHaveBeenCalledOnce())
    expect(mocks.listSourceTables).toHaveBeenCalledOnce()
    expect((mocks.listSourceTables.mock.calls[0][2] as AbortSignal).aborted).toBe(true)
    expect(screen.getByRole('alert').textContent).toContain('概览管理权限已撤销')
    expect(screen.queryByRole('button', { name: 'business.orders' })).toBeNull()
    expect(screen.getByRole('region', { name: '数据库基础信息' }).textContent).not.toContain('localhost')
    await act(async () => completeCatalog([info]))
    expect(onDenied).toHaveBeenCalledOnce()
    expect(screen.queryByRole('button', { name: 'business.orders' })).toBeNull()
    expect(screen.queryByRole('cell', { name: '订单金额' })).toBeNull()
    expect(mocks.inspectSourceTable).not.toHaveBeenCalled()
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
  })

  it('412 清除旧业务结果并禁用查询，保留目录直到刷新结构载入新 etag', async () => {
    const { mocks, onDenied } = mount()
    await ready()
    selectTab('SQL控制台')
    fillCurrentQuery()
    mocks.queryDataSource.mockRejectedValueOnce(new ApiError(412, 'precondition_failed', '数据源配置已更新'))
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    expect((await screen.findByRole('alert')).textContent).toContain('刷新结构')
    expect(screen.getByRole('button', { name: 'business.orders' })).toBeTruthy()
    expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(true)
    expect(onDenied).not.toHaveBeenCalled()
    mocks.getDataSource.mockResolvedValue({ ...source, etag: 'source-updated' })
    mocks.getSourceOverview.mockResolvedValue({ ...overview, etag: 'source-updated' })
    fireEvent.click(screen.getByRole('button', { name: '刷新结构' }))
    await waitFor(() => expect(mocks.getDataSource).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(mocks.inspectSourceTable).toHaveBeenCalledTimes(2))
    selectTab('SQL控制台')
    fillCurrentQuery()
    await waitFor(() => expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    await screen.findByText('9007199254740993.0001')
    expect(mocks.queryDataSource).toHaveBeenLastCalledWith('source', expect.any(String), 'source-updated', expect.any(AbortSignal), 100)
  })

  it('隐藏页面取消待定查询并清空旧内容，重新显示时读取服务器最新结构', async () => {
    const mocks = fixture()
    let complete!: (value: SourceQueryResult) => void
    mocks.queryDataSource.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve }))
    const rendered = mount(mocks)
    await ready()
    selectTab('SQL控制台')
    fillCurrentQuery()
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    const signal = mocks.queryDataSource.mock.calls[0][3] as AbortSignal
    rendered.rerender(<SourceManager api={rendered.api} source={source} active={false} onBack={rendered.onBack} onDenied={rendered.onDenied} />)
    expect(signal.aborted).toBe(true)
    await act(async () => complete(result))
    expect(screen.queryByText('9007199254740993.0001')).toBeNull()
    expect(screen.queryByRole('button', { name: 'business.orders' })).toBeNull()
    rendered.rerender(<SourceManager api={rendered.api} source={source} active onBack={rendered.onBack} onDenied={rendered.onDenied} />)
    await ready()
    expect(mocks.getDataSource).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('tab', { name: '字段详情' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
  })
})
