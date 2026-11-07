import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource } from '../../lib/types'
import { SourcesPage } from './SourcesPage'

const source: DataSource = { id: 'source-a', tenantId: 'tenant', name: '设备数据源', driver: 'mysql', host: 'localhost', port: 3306, database: 'equipment', username: 'reader', tlsMode: 'disable', tenantIsolation: 'column', enabled: true, passwordSet: true, etag: 'source-etag' }

describe('数据源配置', () => {
  it('新增 PostgreSQL 连接保存后清除输入密码，后续编辑省略密码并携带最新ETag', async () => {
    const created = { ...source, id: 'pg-source', name: '业务数据库', driver: 'postgresql' as const, etag: 'created-etag' }
    const createDataSource = vi.fn().mockResolvedValue(created)
    const updateDataSource = vi.fn().mockResolvedValue({ ...created, etag: 'updated-etag' })
    const api = { listDataSources: vi.fn().mockResolvedValue([]), createDataSource, updateDataSource } as unknown as ApiClient
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
    const api = { listDataSources } as unknown as ApiClient
    const { rerender } = render(<SourcesPage api={api} administrator={false} />)
    expect(listDataSources).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: '添加数据源' })).toBeNull()
    rerender(<SourcesPage api={api} administrator />)
    fireEvent.click(await screen.findByRole('button', { name: /设备数据源/ }))
    fireEvent.change(screen.getByLabelText('数据库密码'), { target: { value: 'discarded-secret' } })
    rerender(<SourcesPage api={api} administrator active={false} />)
    expect((screen.getByLabelText('数据库密码') as HTMLInputElement).value).toBe('')
  })
})
