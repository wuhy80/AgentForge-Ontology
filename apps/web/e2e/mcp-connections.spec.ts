import { test, expect } from '@playwright/test'
import { createHash, randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'

const accountFile = process.env.MCP_TEST_ACCOUNT_FILE ?? ''
const gateway = process.env.MCP_TEST_URL ?? 'http://127.0.0.1:18084/mcp'
test.skip(!accountFile || !existsSync(accountFile), '设置 MCP_TEST_ACCOUNT_FILE 并启动真实服务后验证外部连接')

test('真实服务：个人连接一次性显示、撤销与浏览器 OAuth PKCE', async ({ page, request }) => {
  const account = JSON.parse(readFileSync(accountFile, 'utf8').replace(/^\uFEFF/, '')) as { Tenant: string; Username: string; Password: string }
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/#connections')
  await page.getByLabel('租户', { exact: true }).fill(account.Tenant)
  await page.getByLabel('用户名', { exact: true }).fill(account.Username)
  await page.getByLabel('密码', { exact: true }).fill(account.Password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '外部连接', exact: true })).toBeVisible()
  const name = `浏览器连接${Date.now()}`
  await page.getByLabel('连接名称', { exact: true }).fill(name)
  await expect(page.getByRole('checkbox', { name: /发现本体/ })).toBeDisabled()
  await page.getByRole('button', { name: '创建连接凭据', exact: true }).click()
  const secret = page.getByRole('textbox', { name: '新连接凭据', exact: true })
  await expect(secret).toHaveValue(/^mcp_[a-f0-9]{64}$/)
  // Keep the token in test memory; traces/screenshots remain disabled.
  const token = await secret.inputValue()
  await expect(page.getByLabel('MCP 服务地址', { exact: true })).toHaveValue(gateway)
  await page.getByRole('button', { name: '关闭凭据显示', exact: true }).click()
  await expect(secret).toHaveCount(0)
  await page.reload()
  await expect(page.getByText(name, { exact: true })).toBeVisible()
  await expect(secret).toHaveCount(0)
  await page.getByRole('button', { name: `撤销连接：${name}`, exact: true }).click()
  await page.getByRole('button', { name: '确认撤销', exact: true }).click()
  await expect(page.getByText('连接已撤销，后续访问将被拒绝。', { exact: true })).toBeVisible()
  const rejected = await request.post(gateway, { headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, data: { jsonrpc: '2.0', id: 1, method: 'tools/list' } })
  expect(rejected.status()).toBe(401)

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
