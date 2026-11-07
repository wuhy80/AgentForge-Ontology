import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from '../../lib/api'
import type { Organization } from '../../lib/types'
import { OrganizationsPage } from './OrganizationsPage'

const categories: Organization[] = [{ id: 'production', tenantId: 'tenant', name: '生产管理' }, { id: 'purchasing', tenantId: 'tenant', name: '采购管理' }]
const group: Organization = { id: 'equipment', tenantId: 'tenant', categoryId: categories[0].id, name: '设备资料' }
const noop = () => {}
const refresh = async () => {}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

describe('classification and definition-group management', () => {
  it('lists accessible categories and current groups, and synchronizes category selection', async () => {
    const listGroups = vi.fn().mockResolvedValue([group]), onSelectCategory = vi.fn()
    render(<OrganizationsPage api={{ listGroups } as unknown as ApiClient} categories={categories} categoryId={categories[0].id} onSelectCategory={onSelectCategory} onRefresh={refresh} />)
    await screen.findByText(group.name)
    expect(listGroups).toHaveBeenCalledWith(categories[0].id, expect.any(AbortSignal))
    expect(screen.getByRole('button', { name: /生产管理/ }).getAttribute('aria-pressed')).toBe('true')
    fireEvent.click(screen.getByRole('button', { name: /采购管理/ }))
    expect(onSelectCategory).toHaveBeenCalledWith(categories[1].id)
    expect(screen.queryByRole('button', { name: /重命名|删除/ })).toBeNull()
  })

  it('aborts an old category request and ignores its late result after a switch', async () => {
    const old = deferred<Organization[]>(), listGroups = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValueOnce([])
    const api = { listGroups } as unknown as ApiClient
    const { rerender } = render(<OrganizationsPage api={api} categories={categories} categoryId={categories[0].id} onSelectCategory={noop} onRefresh={refresh} />)
    const previousSignal = listGroups.mock.calls[0][1] as AbortSignal
    rerender(<OrganizationsPage api={api} categories={categories} categoryId={categories[1].id} onSelectCategory={noop} onRefresh={refresh} />)
    expect(previousSignal.aborted).toBe(true)
    await screen.findByText('当前分类还没有分组')
    await act(async () => { old.resolve([group]); await old.promise })
    expect(screen.queryByText(group.name)).toBeNull()
    expect(screen.getByRole('heading', { name: '采购管理的分组' })).toBeTruthy()
  })

  it('clears visible groups and the group-name draft immediately when the global category changes', async () => {
    const next = deferred<Organization[]>(), listGroups = vi.fn().mockResolvedValueOnce([group]).mockReturnValueOnce(next.promise)
    const api = { listGroups } as unknown as ApiClient
    const { rerender } = render(<OrganizationsPage api={api} categories={categories} categoryId={categories[0].id} onSelectCategory={noop} onRefresh={refresh} />)
    await screen.findByText(group.name)
    fireEvent.change(screen.getByLabelText('新分组名称'), { target: { value: '上一分类的草稿' } })
    rerender(<OrganizationsPage api={api} categories={categories} categoryId={categories[1].id} onSelectCategory={noop} onRefresh={refresh} />)
    expect(screen.queryByText(group.name)).toBeNull()
    expect((screen.getByLabelText('新分组名称') as HTMLInputElement).value).toBe('')
    expect(screen.getByText('正在读取当前分类的分组…')).toBeTruthy()
    await act(async () => { next.resolve([]); await next.promise })
  })

  it('refreshes the global directory before selecting a created category, even while new props are pending', async () => {
    const created = { ...categories[0], id: 'new-category', name: '质量管理' }
    const directory = deferred<void>(), calls: string[] = []
    const createCategory = vi.fn().mockResolvedValue(created)
    const api = { listGroups: vi.fn().mockResolvedValue([]), createCategory } as unknown as ApiClient
    const onRefresh = vi.fn(() => { calls.push('refresh'); return directory.promise })
    const onSelectCategory = vi.fn(() => { calls.push('select') })
    const { rerender } = render(<OrganizationsPage api={api} categories={categories} categoryId={categories[0].id} onSelectCategory={onSelectCategory} onRefresh={onRefresh} />)
    fireEvent.change(screen.getByLabelText('新分类名称'), { target: { value: '  质量管理  ' } })
    fireEvent.click(screen.getByRole('button', { name: '创建分类' }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
    expect(createCategory).toHaveBeenCalledWith('质量管理')
    expect(onSelectCategory).not.toHaveBeenCalled()
    await act(async () => { directory.resolve(); await directory.promise })
    expect(onSelectCategory).toHaveBeenCalledWith(created.id)
    expect(calls).toEqual(['refresh', 'select'])
    rerender(<OrganizationsPage api={api} categories={categories} categoryId={created.id} onSelectCategory={onSelectCategory} onRefresh={onRefresh} />)
    expect(screen.queryByText(group.name)).toBeNull()
    rerender(<OrganizationsPage api={api} categories={[...categories, created]} categoryId={created.id} onSelectCategory={onSelectCategory} onRefresh={onRefresh} />)
    expect(screen.getByRole('heading', { name: '质量管理的分组' })).toBeTruthy()
    expect((screen.getByLabelText('新分类名称') as HTMLInputElement).value).toBe('')
  })

  it('keeps a successful category creation distinct from a failed directory refresh', async () => {
    const created = { ...categories[0], id: 'created', name: '质量管理' }, onSelectCategory = vi.fn()
    const onRefresh = vi.fn().mockRejectedValueOnce(new Error('暂时不可读取')).mockResolvedValueOnce(undefined)
    const createCategory = vi.fn().mockResolvedValue(created)
    render(<OrganizationsPage api={{ listGroups: vi.fn().mockResolvedValue([group]), createCategory } as unknown as ApiClient} categories={categories} categoryId={categories[0].id} onSelectCategory={onSelectCategory} onRefresh={onRefresh} />)
    await screen.findByText(group.name)
    fireEvent.change(screen.getByLabelText('新分类名称'), { target: { value: created.name } })
    fireEvent.click(screen.getByRole('button', { name: '创建分类' }))
    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toContain('分类已创建，但目录刷新失败')
    expect(onSelectCategory).toHaveBeenCalledWith(created.id)
    expect(screen.queryByText(group.name)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '刷新目录' }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(2))
    expect(createCategory).toHaveBeenCalledTimes(1)
  })

  it('creates a group only under the selected category and reloads that group directory', async () => {
    const created = { ...group, id: 'new-group', name: '备件资料' }
    const listGroups = vi.fn().mockResolvedValueOnce([group]).mockResolvedValueOnce([group, created])
    const createGroup = vi.fn().mockResolvedValue(created)
    render(<OrganizationsPage api={{ listGroups, createGroup } as unknown as ApiClient} categories={categories} categoryId={categories[0].id} onSelectCategory={noop} onRefresh={refresh} />)
    await screen.findByText(group.name)
    fireEvent.change(screen.getByLabelText('新分组名称'), { target: { value: '  备件资料  ' } })
    fireEvent.click(screen.getByRole('button', { name: '创建分组' }))
    await screen.findByText(created.name)
    expect(createGroup).toHaveBeenCalledWith(categories[0].id, '备件资料')
    expect(listGroups).toHaveBeenCalledTimes(2)
    expect((screen.getByLabelText('新分组名称') as HTMLInputElement).value).toBe('')
    expect(screen.getByText('分组“备件资料”已创建。')).toBeTruthy()
  })

  it('clears cached groups on a failed reload and preserves a creation draft on a business conflict', async () => {
    const listGroups = vi.fn().mockResolvedValueOnce([group]).mockRejectedValueOnce(new ApiError(503, 'unavailable', '分组暂不可读取'))
    const createGroup = vi.fn().mockRejectedValue(new ApiError(409, 'name_conflict', '分组名称已经存在'))
    render(<OrganizationsPage api={{ listGroups, createGroup } as unknown as ApiClient} categories={categories} categoryId={categories[0].id} onSelectCategory={noop} onRefresh={refresh} />)
    await screen.findByText(group.name)
    fireEvent.click(screen.getByRole('button', { name: '刷新目录' }))
    await screen.findByText('分组暂不可读取')
    expect(screen.queryByText(group.name)).toBeNull()
    fireEvent.change(screen.getByLabelText('新分组名称'), { target: { value: '重复分组' } })
    fireEvent.click(screen.getByRole('button', { name: '创建分组' }))
    await screen.findByText('分组名称已经存在')
    expect((screen.getByLabelText('新分组名称') as HTMLInputElement).value).toBe('重复分组')
    expect(createGroup).toHaveBeenCalledTimes(1)
  })

  it('clears loaded directory data after a forbidden creation', async () => {
    const createGroup = vi.fn().mockRejectedValue(new ApiError(403, 'forbidden', '当前不可创建分组'))
    render(<OrganizationsPage api={{ listGroups: vi.fn().mockResolvedValue([group]), createGroup } as unknown as ApiClient} categories={categories} categoryId={categories[0].id} onSelectCategory={noop} onRefresh={refresh} />)
    await screen.findByText(group.name)
    fireEvent.change(screen.getByLabelText('新分组名称'), { target: { value: '新分组' } })
    fireEvent.click(screen.getByRole('button', { name: '创建分组' }))
    await screen.findByText('当前不可创建分组')
    expect(screen.queryByText(group.name)).toBeNull()
    expect(screen.queryByRole('button', { name: /生产管理/ })).toBeNull()
    expect(screen.queryByLabelText('新分组名称')).toBeNull()
    expect(screen.getByText('目录暂不可用')).toBeTruthy()
  })

  it('offers category creation with an empty directory and never requests groups without a selected category', () => {
    const listGroups = vi.fn()
    render(<OrganizationsPage api={{ listGroups } as unknown as ApiClient} categories={[]} categoryId="" onSelectCategory={noop} onRefresh={refresh} />)
    expect(listGroups).not.toHaveBeenCalled()
    expect(screen.getByText('还没有业务分类')).toBeTruthy()
    expect(screen.getByLabelText('新分类名称')).toBeTruthy()
    expect(screen.queryByLabelText('新分组名称')).toBeNull()
    expect((within(screen.getByRole('region', { name: '业务分类' })).getByRole('button', { name: '创建分类' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
