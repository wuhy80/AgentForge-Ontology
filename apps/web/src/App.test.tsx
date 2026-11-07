import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError } from './lib/api'
import { App } from './App'

const mocks = vi.hoisted(() => ({ getSession: vi.fn(), login: vi.fn(), logout: vi.fn(), clients: [] as unknown[] }))
vi.mock('./lib/api', async (original) => {
  const actual = await original<typeof import('./lib/api')>()
  return { ...actual, ApiClient: class {
    constructor(_token: unknown, public onUnauthorized: () => void) { mocks.clients.push(this) }
    getSession = mocks.getSession
    login = mocks.login
    logout = mocks.logout
    metadataCapabilities = async () => ({ definitions: [], lifecycle: [], schemaModes: [], storageLayouts: [] })
    dataCapabilities = async () => ({ backend: 'mysql', operations: [] })
    listCategories = async () => []
    listDefinitions = async () => []
  } }
})
vi.mock('./features/model/ModelsPage', () => ({ ModelsPage: () => null }))
vi.mock('./features/query/QueryPage', () => ({ QueryPage: () => null }))
vi.mock('./features/admin/AdminPage', () => ({ AdminPage: () => <p>用户管理页面</p> }))

const session = { user: { id: 'user-id', tenantId: 'demo-tenant', username: 'admin', displayName: '管理员', administrator: true, enabled: true, roleIds: [], etag: 'user-etag' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'csrf-test' }
beforeEach(() => {
  vi.clearAllMocks(); mocks.clients.length = 0
  window.location.hash = ''
  mocks.getSession.mockRejectedValue(new ApiError(401, 'unauthenticated', '请登录'))
  mocks.login.mockResolvedValue(session)
  mocks.logout.mockResolvedValue(undefined)
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
