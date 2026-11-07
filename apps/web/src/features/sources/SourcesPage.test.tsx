import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceCompatibility, SourceVersionOption } from '../../lib/types'
import { SourcesPage } from './SourcesPage'

const source: DataSource = { id: 'source-a', tenantId: 'tenant', name: '设备数据源', driver: 'mysql', host: 'localhost', port: 3306, database: 'equipment', username: 'reader', tlsMode: 'disable', tenantIsolation: 'column', enabled: true, passwordSet: true, etag: 'source-etag' }
const versions: SourceVersionOption[] = [
  { driver: 'mysql', versionLine: '26.7', label: 'MySQL 26.7', status: 'supported' },
  { driver: 'mysql', versionLine: '8.4', label: 'MySQL 8.4', status: 'pending' },
  { driver: 'postgresql', versionLine: '17', label: 'PostgreSQL 17', status: 'supported' },
]
const compatible: SourceCompatibility = { connected: true, compatibilityStatus: 'supported', detectedProduct: 'mysql', serverVersion: '26.7.0', versionLine: '26.7', capabilities: ['catalog', 'read', 'query'], checkedAt: '2026-11-07T08:00:00Z' }
const listSourceVersions = () => vi.fn().mockResolvedValue(versions)

describe('数据源配置', () => {
  it('新增 PostgreSQL 连接保存后清除输入密码，后续编辑省略密码并携带最新ETag', async () => {
    const created = { ...source, id: 'pg-source', name: '业务数据库', driver: 'postgresql' as const, etag: 'created-etag' }
    const createDataSource = vi.fn().mockResolvedValue(created)
    const updateDataSource = vi.fn().mockResolvedValue({ ...created, etag: 'updated-etag' })
    const api = { listDataSources: vi.fn().mockResolvedValue([]), listSourceVersions: listSourceVersions(), createDataSource, updateDataSource } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    await waitFor(() => expect((screen.getByRole('button', { name: '添加数据源' }) as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(screen.getByRole('button', { name: '添加数据源' }))
    fireEvent.change(screen.getByLabelText('数据源名称'), { target: { value: '业务数据库' } })
    fireEvent.change(screen.getByLabelText('数据库类型'), { target: { value: 'postgresql' } })
    expect((screen.getByLabelText('数据库端口') as HTMLInputElement).value).toBe('5432')
    for (const [label, value] of [['数据库主机', 'localhost'], ['数据库名称', 'business'], ['数据库用户名', 'reader'], ['数据库密码', 'fixture-password']]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } })
    }
    fireEvent.click(screen.getByRole('button', { name: '保存数据源' }))
    await screen.findByText('数据源已保存。可测试连接并查看表结构。')
    expect(createDataSource).toHaveBeenCalledWith(expect.objectContaining({ driver: 'postgresql', password: 'fixture-password', port: 5432 }))
    expect((screen.getByLabelText('数据库密码') as HTMLInputElement).value).toBe('')
    fireEvent.change(screen.getByLabelText('数据源名称'), { target: { value: '业务数据库改名' } })
    fireEvent.click(screen.getByRole('button', { name: '保存数据源' }))
    await waitFor(() => expect(updateDataSource).toHaveBeenCalledWith('pg-source', expect.objectContaining({ password: undefined, name: '业务数据库改名' }), 'created-etag'))
    expect(screen.queryByText('fixture-password')).toBeNull()
  })

  it('并发冲突保留本地编辑；目录授权拒绝清除连接配置和输入密码', async () => {
    const api = {
      listDataSources: vi.fn().mockResolvedValue([source]),
      listSourceVersions: listSourceVersions(),
      updateDataSource: vi.fn().mockRejectedValue(new ApiError(412, 'precondition_failed', '数据源已被其他人修改')),
      listSourceTables: vi.fn().mockRejectedValue(new ApiError(403, 'forbidden', '目录访问已被拒绝')),
    } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'unsaved-password' } })
    fireEvent.click(screen.getByRole('button', { name: '保存数据源' }))
    await screen.findByText('数据源已被其他人修改')
    expect((screen.getByLabelText('数据库密码') as HTMLInputElement).value).toBe('unsaved-password')
    fireEvent.click(screen.getByRole('button', { name: '查看数据库表' }))
    await screen.findByText('目录访问已被拒绝')
    expect(screen.queryByLabelText('数据库密码')).toBeNull()
    expect(screen.queryByRole('button', { name: /设备数据源/ })).toBeNull()
  })

  it('普通用户不读取管理员连接目录，页面离开后清除未保存密码', async () => {
    const listDataSources = vi.fn().mockResolvedValue([source])
    const api = { listDataSources, listSourceVersions: listSourceVersions() } as unknown as ApiClient
    const { rerender } = render(<SourcesPage api={api} administrator={false} />)
    expect(listDataSources).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: '添加数据源' })).toBeNull()
    rerender(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'discarded-secret' } })
    rerender(<SourcesPage api={api} administrator active={false} />)
    expect((screen.getByLabelText('数据库密码') as HTMLInputElement).value).toBe('')
  })

  it('旧配置默认自动识别，版本选项来自服务端，待适配系列禁用，切换类型清除原版本约束', async () => {
    const api = { listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: listSourceVersions() } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    expect((screen.getByLabelText('数据库版本') as HTMLSelectElement).value).toBe('auto')
    expect(screen.getByLabelText('兼容状态').textContent).toBe('待检测')
    expect((screen.getByRole('option', { name: 'MySQL 8.4（待适配）' }) as HTMLOptionElement).disabled).toBe(true)
    expect(screen.queryByRole('option', { name: 'PostgreSQL 17' })).toBeNull()
    fireEvent.change(screen.getByLabelText('数据库版本'), { target: { value: '26.7' } })
    fireEvent.change(screen.getByLabelText('数据库类型'), { target: { value: 'postgresql' } })
    expect((screen.getByLabelText('数据库版本') as HTMLSelectElement).value).toBe('auto')
    expect((screen.getByLabelText('数据库端口') as HTMLInputElement).value).toBe('5432')
    expect(screen.getByRole('option', { name: 'PostgreSQL 17' })).toBeTruthy()
    expect(screen.queryByRole('option', { name: 'MySQL 26.7' })).toBeNull()
  })

  it('测试当前编辑表单并保留服务端密码，显示真实版本与操作范围，连接修改立即清除旧检查', async () => {
    const testDataSource = vi.fn().mockResolvedValue({ success: true, serverVersion: compatible.serverVersion, compatibility: compatible })
    const api = { listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: listSourceVersions(), testDataSource } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库主机'), { target: { value: 'new-host.internal' } })
    fireEvent.change(screen.getByLabelText('数据库版本'), { target: { value: '26.7' } })
    fireEvent.click(screen.getByRole('button', { name: '测试当前配置' }))
    await screen.findByText('当前配置检查通过 · MySQL 26.7.0')
    expect(testDataSource).toHaveBeenCalledWith('source-a', expect.objectContaining({ host: 'new-host.internal', password: undefined, versionPolicy: 'pinned', expectedVersionLine: '26.7' }), 'source-etag')
    expect(screen.getByLabelText('实际数据库版本').textContent).toBe('26.7.0')
    expect(screen.getByLabelText('兼容状态').textContent).toBe('兼容')
    expect(screen.getByText('目录读取、实例读取、数据查询')).toBeTruthy()
    expect(screen.getByText('当前编辑配置；测试不会保存表单。检查结果仅适用于对应连接配置；具体操作仍需核验数据库账号权限和表结构。')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('连接加密'), { target: { value: 'require' } })
    expect(screen.getByLabelText('兼容状态').textContent).toBe('待检测')
    expect(screen.getByLabelText('实际数据库版本').textContent).toBe('尚未识别')
    expect(screen.queryByText('当前配置检查通过 · MySQL 26.7.0')).toBeNull()
  })

  it('连接成功但版本不匹配时显示实际版本和拒绝原因，清空密码显式提交空字符串', async () => {
    const result: SourceCompatibility = { ...compatible, compatibilityStatus: 'mismatch', serverVersion: '8.4.7', message: '预期 MySQL 26.7，实际为8.4' }
    const testDataSource = vi.fn().mockResolvedValue({ success: false, serverVersion: result.serverVersion, compatibility: result })
    const api = { listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: listSourceVersions(), testDataSource } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库版本'), { target: { value: '26.7' } })
    fireEvent.click(screen.getByLabelText('清空已保存密码'))
    fireEvent.click(screen.getByRole('button', { name: '测试当前配置' }))
    await screen.findByRole('alert')
    expect(testDataSource).toHaveBeenCalledWith('source-a', expect.objectContaining({ password: '' }), 'source-etag')
    expect(screen.getByLabelText('实际数据库版本').textContent).toBe('8.4.7')
    expect(screen.getByLabelText('兼容状态').textContent).toBe('版本不匹配')
    expect(screen.getByText('已连接')).toBeTruthy()
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.queryByText('版本适配范围')).toBeNull()
  })

  it('保存返回的版本检查显示在目录和详情，未保存的测试结果不覆盖目录', async () => {
    const checked = { ...source, versionPolicy: 'pinned' as const, expectedVersionLine: '26.7', compatibility: compatible }
    const updateDataSource = vi.fn().mockResolvedValue(checked)
    const api = { listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: listSourceVersions(), updateDataSource } as unknown as ApiClient
    render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库版本'), { target: { value: '26.7' } })
    fireEvent.click(screen.getByRole('button', { name: '保存数据源' }))
    await screen.findByText('数据源已保存。可测试连接并查看表结构。')
    expect(updateDataSource).toHaveBeenCalledWith('source-a', expect.objectContaining({ versionPolicy: 'pinned', expectedVersionLine: '26.7' }), 'source-etag')
    expect(screen.getByRole('button', { name: /设备数据源/ }).textContent).toContain('26.7.0 · 兼容')
    expect(screen.getByLabelText('实际数据库版本').textContent).toBe('26.7.0')
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'new-secret' } })
    expect(screen.getByLabelText('兼容状态').textContent).toBe('待检测')
    expect(screen.getByRole('button', { name: /设备数据源/ }).textContent).toContain('26.7.0 · 兼容')
  })

  it('页面离开后不接受迟到的连接检查结果', async () => {
    let complete: (value: unknown) => void = () => undefined
    const testDataSource = vi.fn().mockImplementation(() => new Promise((resolve) => { complete = resolve }))
    const api = { listDataSources: vi.fn().mockResolvedValue([source]), listSourceVersions: listSourceVersions(), testDataSource } as unknown as ApiClient
    const { rerender } = render(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.click(screen.getByRole('button', { name: '测试当前配置' }))
    rerender(<SourcesPage api={api} administrator active={false} />)
    complete({ success: true, serverVersion: compatible.serverVersion, compatibility: compatible })
    await waitFor(() => expect(screen.getByLabelText('兼容状态').textContent).toBe('待检测'))
    expect(screen.queryByText('当前配置检查通过 · MySQL 26.7.0')).toBeNull()
  })
})
