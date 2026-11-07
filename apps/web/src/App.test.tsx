import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError } from './lib/api'
import { App } from './App'

const mocks = vi.hoisted(() => ({ getSession: vi.fn(), login: vi.fn(), logout: vi.fn(), listCategories: vi.fn(), listDefinitions: vi.fn(), clients: [] as unknown[] }))
vi.mock('./lib/api', async (original) => {
  const actual = await original<typeof import('./lib/api')>()
  return { ...actual, ApiClient: class {
    constructor(_token: unknown, public onUnauthorized: () => void) { mocks.clients.push(this) }
    getSession = mocks.getSession
    login = mocks.login
    logout = mocks.logout
    metadataCapabilities = async () => ({ definitions: [], lifecycle: [], schemaModes: [], storageLayouts: [] })
    dataCapabilities = async () => ({ backend: 'mysql', operations: [] })
    listCategories = mocks.listCategories
    listDefinitions = mocks.listDefinitions
  } }
})
vi.mock('./features/model/ModelsPage', () => ({ ModelsPage: ({ categoryId }: { categoryId: string }) => <p data-testid="model-category">{categoryId}</p> }))
vi.mock('./features/query/QueryPage', () => ({ QueryPage: ({ categoryId, schemas }: { categoryId: string; schemas: { definition: { id: string } }[] }) => <p data-testid="query-category">{categoryId}:{schemas.map((item) => item.definition.id).join(',')}</p> }))
vi.mock('./features/admin/AdminPage', () => ({ AdminPage: () => <p>用户管理页面</p> }))
vi.mock('./features/organizations/OrganizationsPage', () => ({ OrganizationsPage: ({ categoryId }: { categoryId: string }) => <p data-testid="organization-category">{categoryId || '暂无分类'}</p> }))
vi.mock('./features/sources/SourcesPage', () => ({ SourcesPage: () => <p>数据源配置页面</p> }))

const session = { user: { id: 'user-id', tenantId: 'demo-tenant', username: 'admin', displayName: '管理员', administrator: true, enabled: true, roleIds: [], etag: 'user-etag' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'csrf-test' }
beforeEach(() => {
  vi.clearAllMocks(); mocks.clients.length = 0
  window.location.hash = ''
  sessionStorage.clear()
  mocks.getSession.mockRejectedValue(new ApiError(401, 'unauthenticated', '请登录'))
  mocks.login.mockResolvedValue(session)
  mocks.logout.mockResolvedValue(undefined)
  mocks.listCategories.mockResolvedValue([])
  mocks.listDefinitions.mockResolvedValue([])
})

it('uses a fresh client after a failed login and opens account management after success', async () => {
  mocks.login.mockRejectedValueOnce(new ApiError(401, 'invalid_login', '用户名或密码错误'))
  render(<App />)
  await screen.findByLabelText('用户名')
  expect(screen.queryByLabelText('访问凭证')).toBeNull()
  fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'admin' } })
  fireEvent.change(screen.getByLabelText('密码', { exact: true }), { target: { value: 'test-password-123' } })
  fireEvent.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByRole('alert')
  const previous = mocks.clients.length
  fireEvent.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByRole('navigation', { name: '工作台导航' })
  expect(mocks.clients.length).toBe(previous + 1)
  expect(mocks.login).toHaveBeenLastCalledWith({ tenant: 'demo-tenant', username: 'admin', password: 'test-password-123' })
  expect(screen.getByRole('link', { name: '用户与权限' })).toBeTruthy()
})

it('restores the server session and only reports logout success after revocation', async () => {
  mocks.getSession.mockResolvedValue(session)
  mocks.logout.mockRejectedValueOnce(new ApiError(0, 'network_error', '网络连接失败'))
  render(<App />)
  await screen.findByRole('navigation', { name: '工作台导航' })
  fireEvent.click(screen.getByRole('button', { name: '退出工作空间' }))
  await screen.findByText('网络连接失败')
  expect(screen.getByRole('navigation', { name: '工作台导航' })).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: '退出工作空间' }))
  await screen.findByRole('heading', { name: '登录你的工作空间' })
  expect(mocks.logout).toHaveBeenCalledTimes(2)
})

it('an obsolete client cannot clear a newly established session', async () => {
  mocks.getSession.mockResolvedValue(session)
  render(<App />)
  await screen.findByRole('navigation', { name: '工作台导航' })
  const old = mocks.clients[0] as { onUnauthorized: () => void }
  fireEvent.click(screen.getByRole('button', { name: '退出工作空间' }))
  await screen.findByLabelText('用户名')
  fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'admin' } })
  fireEvent.change(screen.getByLabelText('密码', { exact: true }), { target: { value: 'test-password-123' } })
  fireEvent.click(screen.getByRole('button', { name: '登录' }))
  await screen.findByRole('navigation', { name: '工作台导航' })
  old.onUnauthorized()
  await waitFor(() => expect(screen.getByRole('navigation', { name: '工作台导航' })).toBeTruthy())
})

const categories = [
  { id: 'category-a', tenantId: 'demo-tenant', name: '生产管理' },
  { id: 'category-b', tenantId: 'demo-tenant', name: '质量管理' },
]
const records = categories.map((category, index) => ({ definition: { id: `schema-${index}`, tenantId: 'demo-tenant', categoryId: category.id, groupId: `group-${index}`, kind: 'schema', name: `设备${index}` }, etag: `etag-${index}`, status: 'active' }))

it('shares the header category across overview, models, query and directory management', async () => {
  mocks.getSession.mockResolvedValue(session)
  mocks.listCategories.mockResolvedValue(categories)
  mocks.listDefinitions.mockImplementation(async (filters: { view: string }) => filters.view === 'current' ? records : [])
  render(<App />)
  const selection = await screen.findByRole('combobox', { name: '当前分类' })
  await waitFor(() => expect((selection as HTMLSelectElement).value).toBe('category-a'))
  expect(screen.getByRole('button', { name: '设备0', hidden: false })).toBeTruthy()
  expect(screen.queryByRole('button', { name: '设备1', hidden: false })).toBeNull()
  fireEvent.change(selection, { target: { value: 'category-b' } })
  expect(screen.getByTestId('model-category').textContent).toBe('category-b')
  expect(screen.getByTestId('query-category').textContent).toBe('category-b:schema-1')
  expect(screen.getByTestId('organization-category').textContent).toBe('category-b')
  expect(screen.getByRole('button', { name: '设备1', hidden: false })).toBeTruthy()
  expect(screen.queryByRole('button', { name: '设备0', hidden: false })).toBeNull()
  fireEvent.click(screen.getByRole('link', { name: '分类与分组' }))
  await waitFor(() => expect(screen.getByRole('link', { name: '分类与分组' }).getAttribute('aria-current')).toBe('page'))
  expect((selection as HTMLSelectElement).value).toBe('category-b')
})

it('restores a valid preference after reload and isolates it by tenant and account', async () => {
  const key = 'ontology.category.demo-tenant.user-id'
  sessionStorage.setItem(key, 'category-b')
  mocks.getSession.mockResolvedValue(session)
  mocks.listCategories.mockResolvedValue(categories)
  const view = render(<App />)
  await waitFor(() => expect((screen.getByRole('combobox', { name: '当前分类' }) as HTMLSelectElement).value).toBe('category-b'))
  view.unmount()
  mocks.getSession.mockResolvedValue({ ...session, user: { ...session.user, id: 'other-user' } })
  render(<App />)
  await waitFor(() => expect((screen.getByRole('combobox', { name: '当前分类' }) as HTMLSelectElement).value).toBe('category-a'))
  expect(sessionStorage.getItem(key)).toBe('category-b')
})

it('validates a saved category against the authorized directory and provides an empty setup entry', async () => {
  sessionStorage.setItem('ontology.category.demo-tenant.user-id', 'unavailable-category')
  mocks.getSession.mockResolvedValue(session)
  mocks.listCategories.mockResolvedValue(categories)
  const view = render(<App />)
  await waitFor(() => expect((screen.getByRole('combobox', { name: '当前分类' }) as HTMLSelectElement).value).toBe('category-a'))
  view.unmount()
  mocks.listCategories.mockResolvedValue([])
  render(<App />)
  await screen.findByRole('heading', { name: '先建立业务目录' })
  expect((screen.getByRole('combobox', { name: '当前分类' }) as HTMLSelectElement).disabled).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: '管理分类与分组' }))
  await waitFor(() => expect(screen.getByRole('link', { name: '分类与分组' }).getAttribute('aria-current')).toBe('page'))
})

it('keeps category management available when the model directory is denied', async () => {
  mocks.getSession.mockResolvedValue(session)
  mocks.listCategories.mockResolvedValue(categories)
  mocks.listDefinitions.mockRejectedValue(new ApiError(403, 'forbidden', '无权读取模型目录'))
  render(<App />)
  await screen.findByText('无权读取模型目录')
  expect((screen.getByRole('combobox', { name: '当前分类' }) as HTMLSelectElement).value).toBe('category-a')
  fireEvent.click(screen.getByRole('link', { name: '分类与分组' }))
  await waitFor(() => expect(screen.getByTestId('organization-category').closest('section')?.hidden).toBe(false))
  expect(screen.getByTestId('organization-category').textContent).toBe('category-a')
})
