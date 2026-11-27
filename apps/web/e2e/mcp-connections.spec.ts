import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'
import { createHash, randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'

const accountFile = process.env.MCP_TEST_ACCOUNT_FILE ?? ''
const gateway = process.env.MCP_TEST_URL ?? 'http://127.0.0.1:18084/mcp'

test('真实服务：个人连接一次性显示、撤销与浏览器 OAuth PKCE', async ({ page, request }) => {
  test.skip(!accountFile || !existsSync(accountFile), '设置 MCP_TEST_ACCOUNT_FILE 并启动真实服务后验证外部连接')
  const account = JSON.parse(readFileSync(accountFile, 'utf8').replace(/^\uFEFF/, '')) as { Tenant: string; Username: string; Password: string }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/#settings')
  await page.getByLabel('租户', { exact: true }).fill(account.Tenant)
  await page.getByLabel('用户名', { exact: true }).fill(account.Username)
  await page.getByLabel('密码', { exact: true }).fill(account.Password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('main', { name: '设置', exact: true })).toBeVisible()
  const name = `浏览器连接${Date.now()}`
  await page.getByLabel('Key 名称', { exact: true }).fill(name)
  await expect(page.getByRole('checkbox', { name: /发现本体/ })).toBeDisabled()
  await page.getByRole('button', { name: '生成 Key', exact: true }).click()
  const secret = page.getByRole('textbox', { name: '新生成的 Key', exact: true })
  await expect(secret).toHaveValue(/^mcp_[a-f0-9]{64}$/)
  // Keep the token in test memory; traces/screenshots remain disabled.
  const token = await secret.inputValue()
  const apiURL = new URL('/api/metadata/categories', page.url())
  const authorizedAPI = await request.get(apiURL.href, { headers: { Authorization: `Bearer ${token}` } })
  expect(authorizedAPI.status()).toBe(200)
  await expect(page.getByLabel('MCP 服务地址', { exact: true })).toHaveValue(gateway)
  await page.getByRole('button', { name: '关闭 Key 显示', exact: true }).click()
  await expect(secret).toHaveCount(0)
  await page.reload()
  await expect(page.getByText(name, { exact: true })).toBeVisible()
  await expect(secret).toHaveCount(0)
  await page.getByRole('button', { name: `撤销连接：${name}`, exact: true }).click()
  await page.getByRole('button', { name: '确认撤销', exact: true }).click()
  await expect(page.getByText('连接已撤销，后续访问将被拒绝。', { exact: true })).toBeVisible()
  const rejected = await request.post(gateway, { headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, data: { jsonrpc: '2.0', id: 1, method: 'tools/list' } })
  expect(rejected.status()).toBe(401)
  const rejectedAPI = await request.get(apiURL.href, { headers: { Authorization: `Bearer ${token}` } })
  expect(rejectedAPI.status()).toBe(401)

  const issuer = new URL(gateway).origin
  const callback = 'http://127.0.0.1:49174/callback'
  const registered = await request.post(`${issuer}/oauth/register`, { data: { client_name: '浏览器 OAuth 验证', redirect_uris: [callback], token_endpoint_auth_method: 'none', grant_types: ['authorization_code'], response_types: ['code'] } })
  expect(registered.status()).toBe(201)
  const client = await registered.json() as { client_id: string }
  const verifier = randomUUID().replaceAll('-', '') + randomUUID().replaceAll('-', '')
  const query = new URLSearchParams({ client_id: client.client_id, redirect_uri: callback, response_type: 'code', code_challenge_method: 'S256', code_challenge: createHash('sha256').update(verifier).digest('base64url'), scope: 'ontology:discover ontology:query', state: 'browser-smoke', resource: gateway })
  await page.route(`${callback}**`, route => route.fulfill({ contentType: 'text/html', body: '<p>callback</p>' }))
  await page.goto(`${issuer}/oauth/authorize?${query}`)
  await expect(page.getByRole('heading', { name: '授权外部应用', exact: true })).toBeVisible()
  await expect(page.getByText('浏览器 OAuth 验证', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '允许连接', exact: true }).click()
  await page.waitForURL(`${callback}**`)
  const result = new URL(page.url())
  expect(result.searchParams.get('state')).toBe('browser-smoke')
  const code = result.searchParams.get('code')
  expect(code).toBeTruthy()
  const form = { grant_type: 'authorization_code', client_id: client.client_id, redirect_uri: callback, code: code!, code_verifier: verifier, resource: gateway }
  const exchange = await request.post(`${issuer}/oauth/token`, { form })
  expect(exchange.status()).toBe(200)
  const authorized = await exchange.json() as { access_token: string }
  expect(authorized.access_token.startsWith('mcp_')).toBe(true)
  const replay = await request.post(`${issuer}/oauth/token`, { form })
  expect(replay.status()).toBe(400)
  const revoke = await request.post(`${issuer}/oauth/revoke`, { form: { token: authorized.access_token } })
  expect(revoke.status()).toBe(200)
  expect(errors).toEqual([])
})

test('设置中的第三方 Key 在双主题和窄屏可生成、撤销，离开页面后隐藏 Key', async ({ page }) => {
  await ontologyFixture(page)
  const connection = {
    id: 'fixture-key', name: '报表服务', kind: 'manual', scopes: ['ontology:discover', 'ontology:query'],
    createdAt: Math.floor(Date.now() / 1000), expiresAt: Math.floor(Date.now() / 1000) + 3600,
  }
  let created = false, revoked = false
  await page.route('**/api/metadata/auth/connections**', async route => {
    const method = route.request().method()
    if (method === 'POST') {
      expect(route.request().postDataJSON()).toMatchObject({ name: connection.name, scopes: connection.scopes })
      created = true
      return route.fulfill({ status: 201, json: { connection, accessToken: 'mcp_' + 'a'.repeat(64) } })
    }
    if (method === 'DELETE') {
      expect(new URL(route.request().url()).pathname).toMatch(/\/fixture-key$/)
      revoked = true
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({ json: { data: created ? [{ ...connection, ...(revoked ? { revokedAt: connection.createdAt } : {}) }] : [], resource: 'http://127.0.0.1:18084/mcp' } })
  })
  await page.goto('/')
  const settings = page.getByRole('link', { name: '设置', exact: true })
  await settings.click()
  await expect(page).toHaveURL(/#settings$/)
  await expect(page.getByRole('heading', { name: '第三方 Key', exact: true })).toBeVisible()
  await expect(page.getByLabel('Ontology API 调用示例')).toContainText('/api/metadata/categories')
  const keysTab = page.getByRole('tab', { name: '第三方 Key', exact: true })
  const oauthTab = page.getByRole('tab', { name: 'OAuth 授权', exact: true })
  await keysTab.focus()
  await page.keyboard.press('ArrowRight')
  await expect(oauthTab).toBeFocused()
  await expect(oauthTab).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('heading', { name: '已授权的应用' })).toBeVisible()
  await expect(page.getByRole('button', { name: '生成 Key', exact: true })).toHaveCount(0)
  await page.keyboard.press('ArrowLeft')
  await expect(keysTab).toBeFocused()

  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 })
    for (const name of ['深空蓝', '精密白']) {
      await page.getByRole('group', { name: '界面主题', exact: true }).getByRole('button', { name, exact: true }).click()
      const keyName = page.getByLabel('Key 名称', { exact: true })
      await keyName.focus()
      await expect(keyName).toBeFocused()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await oauthTab.click()
      await expect(page.getByRole('tabpanel', { name: 'OAuth 授权', exact: true })).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      await keysTab.click()
    }
  }
  await page.getByLabel('Key 名称', { exact: true }).fill(connection.name)
  await page.getByRole('button', { name: '生成 Key', exact: true }).click()
  const key = page.getByRole('textbox', { name: '新生成的 Key', exact: true })
  await expect(key).toHaveValue(/^mcp_[a-f0-9]{64}$/)
  await oauthTab.click()
  await expect(key).toHaveCount(0)
  await expect(page.getByText(connection.name, { exact: true })).toHaveCount(0)
  await keysTab.click()
  await expect(key).toHaveCount(0)
  await page.getByRole('link', { name: '工作空间', exact: true }).click()
  await settings.click()
  await expect(page.getByText(connection.name, { exact: true })).toBeVisible()
  await expect(key).toHaveCount(0)
  await page.getByRole('button', { name: '撤销连接：报表服务', exact: true }).click()
  await page.getByRole('button', { name: '确认撤销', exact: true }).click()
  await expect(page.getByText('已撤销', { exact: true })).toBeVisible()
  expect(revoked).toBe(true)
  await page.goto('/#connections')
  await expect(page.getByRole('main', { name: '设置', exact: true })).toBeVisible()
})
