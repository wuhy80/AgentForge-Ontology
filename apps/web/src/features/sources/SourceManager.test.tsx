import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceQueryResult, SourceTable } from '../../lib/types'
import { SourceManager } from './SourceManager'
import { SourcesPage } from './SourcesPage'

const source: DataSource = { id: 'source', tenantId: 'tenant', name: '业务库', driver: 'mysql', host: 'localhost', port: 3306, database: 'business', username: 'reader', tlsMode: 'disable', tenantIsolation: 'dedicated', enabled: true, passwordSet: true, etag: 'source-etag' }
const table: SourceTable = { namespace: 'business', name: 'orders', signature: 'signature', columns: [{ name: 'amount', databaseType: 'decimal(24,4)', dataType: 'decimal', nullable: true, primaryKey: false, generated: false, position: 1 }], indexes: [{ name: 'idx_amount', unique: false, primary: false, columns: ['amount'] }] }
const result: SourceQueryResult = { columns: ['amount'], rows: [['9007199254740993.0001'], [null], ['']], limit: 100, hasMore: true, elapsedMs: 3 }
function fixture() {
  return {
    listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: vi.fn().mockResolvedValue([]),
    getDataSource: vi.fn().mockResolvedValue(source), listSourceTables: vi.fn().mockResolvedValue([{ namespace: table.namespace, name: table.name, type: 'table' }]),
    inspectSourceTable: vi.fn().mockResolvedValue(table), queryDataSource: vi.fn().mockResolvedValue(result),
  }
}

describe('数据源表格与数据库管理', () => {
  it('列表直接测试已保存配置，显示真实成功或失败，测试不打开编辑器', async () => {
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
    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toContain('数据库连接失败')
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('进入管理清除密码，重新读取配置与目录；查看索引后执行SQL保留精确值/空值/空串，编辑SQL清除旧结果', async () => {
    const mocks = fixture()
    render(<SourcesPage api={mocks as unknown as ApiClient} administrator />)
    const row = await screen.findByRole('row', { name: /业务库/ })
    fireEvent.click(within(row).getByRole('button', { name: '编辑 业务库' }))
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'unsaved-secret' } })
    fireEvent.click(within(row).getByRole('button', { name: '管理' }))
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: 'business.orders' }))
    await screen.findByRole('cell', { name: 'idx_amount' })
    expect((screen.getByLabelText('SQL 语句') as HTMLTextAreaElement).value).toBe('SELECT * FROM `business`.`orders` LIMIT 100;')
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    await screen.findByText('9007199254740993.0001')
    expect(mocks.queryDataSource).toHaveBeenCalledWith('source', 'SELECT * FROM `business`.`orders` LIMIT 100;', 'source-etag', expect.any(AbortSignal))
    expect(screen.getByText('NULL')).toBeTruthy()
    expect(screen.getByText('空字符串')).toBeTruthy()
    expect(screen.getByRole('status').textContent).toContain('还有更多数据')
    fireEvent.change(screen.getByLabelText('SQL 语句'), { target: { value: 'SELECT amount FROM orders LIMIT 2;' } })
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '返回数据源列表' }))
    await screen.findByRole('row', { name: /业务库/ })
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
  })

  it('共享源仍可看表结构，直接SQL禁用', async () => {
    const mocks = fixture()
    mocks.getDataSource.mockResolvedValue({ ...source, tenantIsolation: 'column' })
    render(<SourceManager api={mocks as unknown as ApiClient} source={{ ...source, tenantIsolation: 'column' }} active onBack={vi.fn()} onDenied={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'business.orders' }))
    await screen.findByRole('cell', { name: 'idx_amount' })
    expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(true)
    expect(mocks.queryDataSource).not.toHaveBeenCalled()
  })

  it('查询被拒绝立即清理目录和旧结果；隐藏页面拒绝迟到响应', async () => {
    const mocks = fixture()
    let complete: (value: SourceQueryResult) => void = () => undefined
    mocks.queryDataSource.mockImplementationOnce(() => new Promise((resolve) => { complete = resolve }))
    const denied = vi.fn()
    const api = mocks as unknown as ApiClient
    const { rerender } = render(<SourceManager api={api} source={source} active onBack={vi.fn()} onDenied={denied} />)
    fireEvent.click(await screen.findByRole('button', { name: 'business.orders' }))
    await screen.findByRole('cell', { name: 'idx_amount' })
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    rerender(<SourceManager api={api} source={source} active={false} onBack={vi.fn()} onDenied={denied} />)
    await act(async () => { complete(result) })
    expect(screen.queryByText('9007199254740993.0001')).toBeNull()
    expect((screen.getByLabelText('SQL 语句') as HTMLTextAreaElement).value).toBe('')
    rerender(<SourceManager api={api} source={source} active onBack={vi.fn()} onDenied={denied} />)
    fireEvent.click(await screen.findByRole('button', { name: 'business.orders' }))
    await screen.findByRole('cell', { name: 'idx_amount' })
    mocks.queryDataSource.mockRejectedValueOnce(new ApiError(403, 'administrator_required', '管理权限已撤销'))
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    await waitFor(() => expect(denied).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: 'business.orders' })).toBeNull()
    expect(screen.queryByText('idx_amount')).toBeNull()
    expect(screen.queryByRole('region', { name: 'SQL 查询结果' })).toBeNull()
  })
})
