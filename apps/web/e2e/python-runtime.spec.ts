import { test, expect, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import type { Definition, DefinitionRecord, Invocation, Values } from '../src/lib/types'

// Every API endpoint is intercepted. This checks the browser UI contract;
// MySQL, current permissions and Python container execution use Go integration.
async function fixture(page: Page, kind: 'function' | 'action' = 'function') {
  const tenantId = 'python-runtime-fixture'
  const definitionId = `python-${kind}`
  let current: DefinitionRecord = { status: 'active', etag: 'current-original', definition: {
    id: definitionId, tenantId, categoryId: 'knowledge', groupId: 'computations', kind, name: '原文转换',
    [kind]: { sideEffects: 'none', runtime: { language: 'python', source: 'def main(inputs):\n    return {"转换结果": inputs["原文"]}\n', entrypoint: 'main', timeoutSeconds: 5 },
      input: [{ id: 'input-original', name: '原文', dataType: 'string', required: true }], output: [{ id: 'output-result', name: '转换结果', dataType: 'string' }] },
  } }
  let draft: DefinitionRecord | undefined
  let lastInvocation: Invocation | undefined
  const requests: { body: { definitionId: string; input: Values; draftEtag?: string }; key: string }[] = []
  const writes: Definition[] = []
  const unhandled: string[] = []
  let denyResult = false
  await page.route('**/api/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'author', tenantId, username: 'author', displayName: '计算作者', enabled: true, administrator: false, roleIds: [], etag: 'account' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'] })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'knowledge', tenantId, name: '个人知识库' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'computations', tenantId, categoryId: 'knowledge', name: '计算能力' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? draft ? [draft] : [] : [current], hasMore: false })
    if (path === `/api/metadata/definitions/${definitionId}/draft`) {
      if (method === 'PUT') {
        const definition = request.postDataJSON() as Definition
        writes.push(structuredClone(definition))
        draft = { definition, status: 'draft', hasCurrent: true, etag: `draft-updated-${writes.length}` }
        return reply(draft)
      }
      return draft ? reply(draft) : reply({ code: 'not_found', message: '没有草稿' }, 404)
    }
    if (path === `/api/metadata/definitions/${definitionId}`) return reply(current)
    if (path === `/api/metadata/definitions/${definitionId}/publish`) {
      current = { ...draft!, status: 'active', etag: 'current-updated' }
      draft = undefined
      return reply({ id: 'publication', tenantId, definitionId: current.definition.id, status: 'succeeded' })
    }
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python'], synchronous: true, actionInlineSource: true, draftInvocation: true })
    if (path === '/api/execution/invocations' && method === 'POST') {
      expect(request.headers()['x-csrf-token']).toBe('fixture-csrf')
      const body = request.postDataJSON() as { definitionId: string; input: Values; draftEtag?: string }
      requests.push({ body, key: request.headers()['idempotency-key'] })
      const text = String(body.input['input-original'])
      const implementation = body.draftEtag ? draft! : current
      if (body.draftEtag) expect(body.draftEtag).toBe(draft?.etag)
      const source = implementation.definition[kind]!.runtime!.source
      lastInvocation = { id: `invocation-${requests.length}`, definitionId: current.definition.id, definitionName: current.definition.name, kind, status: 'succeeded', output: { 'output-result': source.includes('.upper()') ? text.toUpperCase() : text }, createdAt: '2026-11-07T00:00:00Z', codeDigest: createHash('sha256').update(source).digest('hex'), mode: body.draftEtag ? 'draft' : 'current', draftEtag: body.draftEtag }
      if (source.includes('raise ValueError')) lastInvocation = { ...lastInvocation, status: 'failed', output: undefined, error: { code: 'python_failed', message: 'Python 执行失败，请核对代码、入口与参数' }, diagnostics: [{ code: 'python_runtime_error', message: '此行执行失败，请核对代码与输入', line: 2 }] }
      return reply(lastInvocation, 201)
    }
    if (path.startsWith('/api/execution/invocations/')) return denyResult ? reply({ code: 'forbidden', message: '当前无权读取运行结果' }, 403) : reply(lastInvocation)
    unhandled.push(`${method} ${path}`)
    return reply({ code: 'unhandled', message: '未定义的隔离接口' }, 404)
  })
  return { requests, writes, unhandled, denyResult: () => { denyResult = true } }
}

for (const kind of ['function', 'action'] as const) test(`${kind} 独立 Python 高亮补全、发布与运行、撤权清理及窄屏布局`, async ({ page }) => {
  const state = await fixture(page, kind)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto(`/#models/python-${kind}`)
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  const source = editor.getByRole('textbox', { name: 'Python 源码', exact: true })
  await expect(editor.getByRole('region', { name: kind === 'action' ? '动作源码' : '函数源码', exact: true })).toBeVisible()
  await expect(source).toContainText('def main(inputs):')
  await expect(source.locator('span').filter({ hasText: /^def$/ })).toBeVisible()
  await source.fill('def main(inputs):\n    return inputs["')
  await source.press('Control+Space')
  const inputOption = page.getByRole('option', { name: /原文/ })
  await expect(inputOption).toBeVisible()
  await inputOption.click()
  await expect(source).toContainText('inputs["原文')
  await source.fill('def main(inputs):\n    return {"')
  await source.press('Control+Space')
  await page.getByRole('option', { name: /转换结果/ }).click()
  await expect(source).toContainText('return {"转换结果')
  await source.fill('def main(inputs):\n    return {"转换结果": inputs["原文"].upper()}\n')
  await editor.getByRole('button', { name: '打开运行面板', exact: true }).click()
  const input = editor.getByRole('textbox', { name: /^原文/ })
  await input.fill('中文测试 mixedCase')
  await editor.getByRole('button', { name: '运行', exact: true }).click()
  const results = editor.getByRole('region', { name: '运行结果', exact: true })
  await expect(results.getByRole('heading', { name: '运行成功', exact: true })).toBeVisible()
  await expect(results.getByRole('definition')).toHaveText('中文测试 mixedCase')
  expect(state.requests[0].body).toEqual({ definitionId: `python-${kind}`, input: { 'input-original': '中文测试 mixedCase' } })
  expect(state.requests[0].key).toBeTruthy()
  await expect(source).toContainText('.upper()')
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(editor.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。', { exact: true })).toBeVisible()
  expect(state.writes[0][kind]!.runtime!.source).toContain('.upper()')
  await editor.getByRole('button', { name: '发布定义', exact: true }).click()
  await expect(editor.getByText('发布成功，当前定义已生效。', { exact: true })).toBeVisible()
  await editor.getByRole('button', { name: '打开运行面板', exact: true }).click()
  await editor.getByRole('textbox', { name: /^原文/ }).fill('中文测试 mixedCase')
  await editor.getByRole('button', { name: '运行', exact: true }).click()
  await expect(results.getByRole('definition')).toHaveText('中文测试 MIXEDCASE')
  await source.fill(`def main(inputs):\n    return {"转换结果": inputs["原文"].upper()}\n# ${'x'.repeat(600)}`)
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    await expect(results).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  }
  state.denyResult()
  await results.getByRole('button', { name: '核对运行结果', exact: true }).click()
  await expect(editor.getByRole('alert')).toHaveText('当前无权读取运行结果')
  await expect(results).toHaveCount(0)
  await expect(editor.getByRole('textbox', { name: /^原文/ })).toHaveCount(0)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})

for (const kind of ['function', 'action'] as const) test(`${kind} 本地语法诊断、保留注释格式化、样例保存与草稿隔离试跑`, async ({ page }) => {
  const state = await fixture(page, kind)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.goto(`/#models/python-${kind}`)
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  const sourceSection = editor.getByRole('region', { name: kind === 'action' ? '动作源码' : '函数源码', exact: true })
  const source = sourceSection.getByRole('textbox', { name: 'Python 源码', exact: true })
  await source.fill('def main(inputs)\n    return {}\n')
  await expect(sourceSection.getByRole('list', { name: '源码诊断' })).toContainText('第 1 行')
  await expect(sourceSection.locator('.cm-lint-marker-error')).not.toHaveCount(0)
  await source.fill('# 保留这条注释\ndef main(inputs):\n return {"转换结果":inputs["原文"].upper()}\n')
  await sourceSection.getByRole('button', { name: '格式化代码', exact: true }).click()
  await expect(sourceSection.getByText('已格式化，可撤销。', { exact: true })).toBeVisible()
  await expect(source).toContainText('# 保留这条注释')
  await expect(source).toContainText('    return {"转换结果": inputs["原文"].upper()}')
  const parameter = editor.getByLabel('输入参数第1项名称', { exact: true })
  await parameter.fill('新原文')
  await expect(sourceSection.getByRole('list', { name: '参数改名引用提醒' })).toContainText('「原文」已改为「新原文」')
  await parameter.fill('原文')
  await expect(sourceSection.getByRole('list', { name: '参数改名引用提醒' })).toHaveCount(0)

  const samples = editor.getByRole('region', { name: '常用测试样例', exact: true })
  await samples.getByText('添加测试样例', { exact: true }).click()
  await samples.getByLabel('样例名称', { exact: true }).fill('混合大小写')
  await samples.getByLabel('原文的传入方式', { exact: true }).selectOption('value')
  await samples.getByRole('textbox', { name: /^原文/ }).fill('中文 mixedCase')
  await samples.getByRole('button', { name: '加入草稿样例', exact: true }).click()
  const draftRunner = editor.getByRole('region', { name: '草稿试运行', exact: true })
  await expect(draftRunner.getByRole('button', { name: '打开草稿试运行', exact: true })).toBeDisabled()
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(editor.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。', { exact: true })).toBeVisible()
  expect(state.writes[0][kind]!.testCases).toEqual([{ id: expect.any(String), name: '混合大小写', input: { 'input-original': '中文 mixedCase' } }])
  await draftRunner.getByRole('button', { name: '打开草稿试运行', exact: true }).click()
  await draftRunner.getByRole('combobox', { name: '载入测试样例', exact: true }).selectOption({ label: '混合大小写' })
  await draftRunner.getByRole('button', { name: '试运行草稿', exact: true }).click()
  await expect(draftRunner.getByRole('definition')).toHaveText('中文 MIXEDCASE')
  expect(state.requests[0].body.draftEtag).toBe('draft-updated-1')
  const currentRunner = editor.getByRole('region', { name: '运行当前定义', exact: true })
  await currentRunner.getByRole('button', { name: '打开运行面板', exact: true }).click()
  await currentRunner.getByRole('textbox', { name: /^原文/ }).fill('中文 mixedCase')
  await currentRunner.getByRole('button', { name: '运行', exact: true }).click()
  await expect(currentRunner.getByRole('definition')).toHaveText('中文 mixedCase')
  expect(state.requests[1].body.draftEtag).toBeUndefined()

  await source.fill('def main(inputs):\n    raise ValueError("异常文本不公开")\n')
  await expect(draftRunner.getByRole('region', { name: '草稿试运行结果', exact: true })).toHaveCount(0)
  await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(editor.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。', { exact: true })).toBeVisible()
  await draftRunner.getByRole('button', { name: '打开草稿试运行', exact: true }).click()
  await draftRunner.getByRole('combobox', { name: '载入测试样例', exact: true }).selectOption({ label: '混合大小写' })
  await draftRunner.getByRole('button', { name: '试运行草稿', exact: true }).click()
  await expect(draftRunner.getByRole('heading', { name: '运行失败', exact: true })).toBeVisible()
  await expect(sourceSection.getByRole('list', { name: '源码诊断' })).toContainText('第 2 行')
  await draftRunner.getByRole('button', { name: /第 2 行/ }).click()
  await expect(source).toBeFocused()
  await expect(source.locator('.cm-activeLine')).toContainText('raise ValueError')
  await page.setViewportSize({ width: 390, height: 1000 })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  expect(state.unhandled).toEqual([])
  expect(errors).toEqual([])
})
