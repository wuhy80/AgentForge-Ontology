import { test, expect, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import type { Definition, DefinitionRecord, Invocation, ScriptRuntime, Values } from '../src/lib/types'

type Language = 'javascript' | 'go'
const languageNames = { javascript: 'JavaScript', go: 'Go' }
const sourceFor = (language: Language, upper = false) => language === 'javascript'
  ? `function main(inputs) {\n  return { "转换结果": inputs["原文"]${upper ? '.toUpperCase()' : ''} };\n}\n`
  : `package main\n${upper ? '\nimport "strings"\n' : ''}\nfunc Main(inputs map[string]any) (map[string]any, error) {\n\treturn map[string]any{"转换结果": ${upper ? 'strings.ToUpper(inputs["原文"].(string))' : 'inputs["原文"].(string)'}}, nil\n}\n`

// Browser interactions use isolated API fixtures. Real compilers, containers,
// SQL persistence and current authorization are verified by Go integration.
async function fixture(page: Page, language: Language, kind: 'function' | 'action', runtimes = ['python', 'javascript', 'go'], dataWrite = false) {
  const tenantId = 'multilang-browser-fixture', id = `${language}-${kind}`
  let current: DefinitionRecord = { status: 'active', etag: 'active-original', definition: {
    id, tenantId, categoryId: 'knowledge', groupId: 'calculations', kind, name: '多语言原文转换',
    [kind]: { sideEffects: dataWrite ? 'dataWrite' : 'none', ...(dataWrite ? { dataWrite: [{ schemaId: 'schema', operations: ['update'] }] } : {}), runtime: { language, source: sourceFor(language), entrypoint: language === 'go' ? 'Main' : 'main', timeoutSeconds: language === 'go' ? 30 : 5 },
      input: [{ id: 'original', name: '原文', dataType: 'string', required: true }], output: [{ id: 'result', name: '转换结果', dataType: 'string' }],
      testCases: [{ id: 'mixed', name: '混合大小写', input: { original: '中文 mixedCase' } }] },
  } }
  let draft: DefinitionRecord | undefined, result: Invocation | undefined
  let denied = false
  const calls: { definitionId: string; input: Values; draftEtag?: string }[] = []
  const keys: string[] = []
  const writes: Definition[] = [], tools: { definitionId: string; operation: string; runtime: ScriptRuntime }[] = [], unhandled: string[] = []
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), method = request.method(), path = url.pathname
    const reply = (json: unknown, status = 200) => route.fulfill({ status, json })
    if (path === '/api/metadata/auth/connections') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'author', tenantId, username: 'author', displayName: '源码作者', enabled: true, administrator: false, roleIds: [], etag: 'user' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, operations: ['query'] })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: [{ id: 'knowledge', tenantId, name: '知识库' }], hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: [{ id: 'calculations', tenantId, categoryId: 'knowledge', name: '计算能力' }], hasMore: false })
    if (path === '/api/metadata/definitions') return reply({ data: url.searchParams.get('view') === 'draft' ? draft ? [draft] : [] : [current], hasMore: false })
    if (path === `/api/metadata/definitions/${id}/draft`) {
      if (method === 'PUT') {
        const definition = request.postDataJSON() as Definition
        writes.push(structuredClone(definition))
        draft = { definition, status: 'draft', hasCurrent: true, etag: createHash('sha256').update(JSON.stringify(definition)).digest('hex') }
        return reply(draft)
      }
      return draft ? reply(draft) : reply({ code: 'not_found', message: '没有草稿' }, 404)
    }
    if (path === `/api/metadata/definitions/${id}`) return reply(current)
    if (path === `/api/metadata/definitions/${id}/publish`) {
      current = { ...draft!, status: 'active', etag: 'active-updated' }; draft = undefined
      return reply({ id: 'publication', tenantId, definitionId: id, status: 'succeeded' })
    }
    if (path === '/api/execution/capabilities') return reply({ invocation: true, draftInvocation: true, dataWriteTransactions: true, sourceDiagnostics: true, testCases: true, runtimes, synchronous: true, actionInlineSource: true, sourceTools: ['go'] })
    if (path === '/api/execution/source-tools') {
      expect(request.headers()['x-csrf-token']).toBe('fixture-csrf')
      const body = request.postDataJSON() as typeof tools[number]
      tools.push(body)
      expect(body.definitionId).toBe(id)
      expect(body.runtime.language).toBe('go')
      if (body.runtime.source.includes('fixture_invalid')) return reply({ source: body.runtime.source, diagnostics: [{ code: 'syntax_error', message: 'Go语法错误，请检查此处的语句与符号', line: 2, column: 6 }] })
      return reply({ source: body.operation === 'format' ? sourceFor('go', body.runtime.source.includes('strings.ToUpper')) : body.runtime.source, diagnostics: [] })
    }
    if (path === '/api/execution/invocations' && method === 'POST') {
      expect(request.headers()['x-csrf-token']).toBe('fixture-csrf')
      expect(request.headers()['idempotency-key']).toBeTruthy()
      const body = request.postDataJSON() as typeof calls[number]
      calls.push(body)
      keys.push(request.headers()['idempotency-key'])
      const selected = body.draftEtag ? draft! : current
      if (body.draftEtag) expect(body.draftEtag).toBe(draft?.etag)
      const runtime = selected.definition[kind]!.runtime!, text = String(body.input.original)
      result = { id: `invocation-${calls.length}`, definitionId: id, definitionName: selected.definition.name, kind, mode: body.draftEtag ? 'draft' : 'current', draftEtag: body.draftEtag,
        status: 'succeeded', output: { result: /ToUpper|toUpperCase/.test(runtime.source) ? text.toUpperCase() : text }, codeDigest: createHash('sha256').update(runtime.source).digest('hex'), createdAt: '2026-11-08T00:00:00Z' }
      if (dataWrite) result = { ...result, status: calls.length === 1 ? 'committing' : 'succeeded', output: calls.length === 1 ? undefined : result.output,
        transaction: { status: calls.length === 1 ? 'pending' : 'committed', committed: calls.length > 1, operationCount: 1 } }
      return reply(result, 201)
    }
    if (path.startsWith('/api/execution/invocations/')) return denied ? reply({ code: 'forbidden', message: '当前无权读取运行结果' }, 403) : reply(result)
    unhandled.push(`${method} ${path}`)
    return reply({ code: 'unhandled', message: '未定义的隔离接口' }, 404)
  })
  return { id, calls, keys, writes, tools, unhandled, deny: () => { denied = true } }
}

test('事务写入正式调用：草稿禁用、提交中隐藏结果，查询后以原参数和键恢复', async ({ page }) => {
  const state = await fixture(page, 'javascript', 'function', ['python', 'javascript', 'go'], true)
  await page.goto(`/#models/${state.id}`)
  await expect(page.getByRole('button', { name: '打开草稿试运行', exact: true })).toBeDisabled()
  await expect(page.getByText('数据写入草稿不允许试运行；请发布后通过当前用户会话正式调用。', { exact: true })).toBeVisible()
  const runner = page.getByRole('region', { name: '运行当前定义', exact: true })
  await runner.getByRole('button', { name: '打开运行面板', exact: true }).click()
  await runner.getByRole('textbox', { name: '原文 string', exact: true }).fill('事务输出样本')
  await runner.getByRole('button', { name: '运行', exact: true }).click()
  await expect(runner.getByText('正在确认事务提交', { exact: true })).toBeVisible()
  await expect(runner.locator('dd')).toHaveCount(0)
  await expect(runner.getByRole('button', { name: '关闭运行', exact: true })).toBeDisabled()
  await expect(runner.getByRole('textbox', { name: '原文 string', exact: true })).toBeDisabled()
  state.deny()
  await runner.getByRole('button', { name: '核对运行结果', exact: true }).click()
  await expect(runner.getByRole('alert')).toContainText('当前无权读取运行结果')
  await expect(runner.getByText('正在确认事务提交', { exact: true })).toBeVisible()
  await expect(runner.getByRole('textbox', { name: '原文 string', exact: true })).toBeDisabled()
  await expect(runner.getByRole('button', { name: '重新载入运行参数', exact: true })).toHaveCount(0)
  await runner.getByRole('button', { name: '恢复本次提交', exact: true }).click()
  await expect(runner.getByText('运行成功', { exact: true })).toBeVisible()
  await expect(runner.locator('dd')).toHaveText('事务输出样本')
  await expect(runner.getByText('已提交 1 条业务写入。', { exact: true })).toBeVisible()
  expect(state.calls).toHaveLength(2)
  expect(state.calls[0]).toEqual(state.calls[1])
  expect(state.keys[0]).toBe(state.keys[1])
  expect(state.unhandled).toEqual([])
})

test('切换语言须确认替换已编辑源码，取消保留原文，确认后使用对应模板与入口', async ({ page }) => {
  const state = await fixture(page, 'javascript', 'function')
  await page.goto(`/#models/${state.id}`)
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  const section = editor.getByRole('region', { name: '函数源码', exact: true })
  const language = section.getByRole('combobox', { name: '执行语言', exact: true })
  const before = await section.getByRole('textbox', { name: 'JavaScript 源码', exact: true }).innerText()
  await language.selectOption('go')
  await expect(section.getByRole('alert')).toContainText('切换语言')
  await section.getByRole('button', { name: '保留当前源码', exact: true }).click()
  await expect(language).toHaveValue('javascript')
  await expect(section.getByRole('textbox', { name: 'JavaScript 源码', exact: true })).toHaveText(before, { useInnerText: true })
  await language.selectOption('go')
  await section.getByRole('button', { name: '确认切换语言', exact: true }).click()
  await expect(language).toHaveValue('go')
  await expect(section.getByRole('textbox', { name: 'Go 源码', exact: true })).toContainText('func Main')
  await expect(section.getByRole('textbox', { name: '入口函数', exact: true })).toHaveValue('Main')
  await expect(section.getByRole('spinbutton', { name: '执行超时（秒）', exact: true })).toHaveValue('30')
  expect(state.calls).toHaveLength(0)
})

test('所选语言运行器缺失时明确拒绝，保留源码且不调用其他语言', async ({ page }) => {
  const state = await fixture(page, 'javascript', 'function', ['python', 'go'])
  await page.goto(`/#models/${state.id}`)
  const editor = page.getByRole('region', { name: '模型编辑', exact: true })
  const runner = editor.getByRole('region', { name: '运行当前定义', exact: true })
  await runner.getByRole('button', { name: '打开运行面板', exact: true }).click()
  await expect(runner.getByRole('alert')).toContainText('JavaScript')
  await expect(editor.getByRole('textbox', { name: 'JavaScript 源码', exact: true })).toContainText('function main')
  expect(state.calls).toHaveLength(0)
  expect(state.unhandled).toEqual([])
})

for (const language of ['javascript', 'go'] as const) for (const kind of ['function', 'action'] as const) {
  test(`${language} ${kind} 源码高亮补全、格式化、草稿与当前态隔离及发布`, async ({ page }) => {
    const state = await fixture(page, language, kind), errors: string[] = []
    page.on('pageerror', (error) => errors.push(error.message))
    await page.goto(`/#models/${state.id}`)
    const editor = page.getByRole('region', { name: '模型编辑', exact: true })
    const sourceSection = editor.getByRole('region', { name: kind === 'action' ? '动作源码' : '函数源码', exact: true })
    const source = sourceSection.getByRole('textbox', { name: `${languageNames[language]} 源码`, exact: true })
    await expect(source).toContainText(language === 'go' ? 'func Main' : 'function main')
    await expect(source.locator('span').filter({ hasText: language === 'go' ? /^func$/ : /^function$/ })).toBeVisible()
    if (language === 'javascript') {
      await source.fill('function main(inputs {\n  return {};\n}\n')
      await expect(sourceSection.getByRole('list', { name: '源码诊断' })).toContainText('第 1 行')
    } else {
      await source.fill('package main\nfunc fixture_invalid(\n')
      await sourceSection.getByRole('button', { name: '检查语法', exact: true }).click()
      await expect(sourceSection.getByRole('list', { name: '源码诊断' })).toContainText('第 2 行')
      expect(state.tools.at(-1)?.operation).toBe('check')
    }
    await expect(sourceSection.locator('.cm-lint-marker-error')).not.toHaveCount(0)
    const incomplete = language === 'go' ? 'package main\nfunc Main(inputs map[string]any) (map[string]any, error) {\n return map[string]any{"转换结果": inputs["' : 'function main(inputs) {\n return {"转换结果": inputs["'
    await source.fill(incomplete)
    await source.press('Control+Space')
    await page.getByRole('option', { name: /原文/ }).click()
    await expect(source).toContainText('inputs["原文')
    const uppercase = sourceFor(language, true)
    await source.fill(uppercase.replace(language === 'go' ? '\treturn' : '  return', 'return'))
    await sourceSection.getByRole('button', { name: '格式化代码', exact: true }).click()
    await expect(sourceSection.getByText('已格式化，可撤销。', { exact: true })).toBeVisible()
    await expect(source).toContainText(language === 'go' ? '\treturn map[string]any' : 'return {')
    if (language === 'go') expect(state.tools.some((item) => item.operation === 'format')).toBe(true)

    const current = editor.getByRole('region', { name: '运行当前定义', exact: true })
    await current.getByRole('button', { name: '打开运行面板', exact: true }).click()
    await current.getByRole('combobox', { name: '载入测试样例', exact: true }).selectOption('mixed')
    await current.getByRole('button', { name: '运行', exact: true }).click()
    await expect(current.getByRole('definition')).toHaveText('中文 mixedCase')
    expect(state.calls[0].draftEtag).toBeUndefined()

    await editor.getByRole('button', { name: '保存草稿', exact: true }).click()
    await expect(editor.getByText('草稿已保存。可以继续配置索引、预览存储计划或发布。', { exact: true })).toBeVisible()
    expect(state.writes[0][kind]!.runtime!.language).toBe(language)
    expect(state.writes[0][kind]!.testCases?.[0].input).toEqual({ original: '中文 mixedCase' })
    const draft = editor.getByRole('region', { name: '草稿试运行', exact: true })
    await draft.getByRole('button', { name: '打开草稿试运行', exact: true }).click()
    await draft.getByRole('combobox', { name: '载入测试样例', exact: true }).selectOption('mixed')
    await draft.getByRole('button', { name: '试运行草稿', exact: true }).click()
    await expect(draft.getByRole('definition')).toHaveText('中文 MIXEDCASE')
    expect(state.calls[1].draftEtag).toMatch(/^[a-f0-9]{64}$/)

    await editor.getByRole('button', { name: '发布定义', exact: true }).click()
    await expect(editor.getByText('发布成功，当前定义已生效。', { exact: true })).toBeVisible()
    await current.getByRole('button', { name: '打开运行面板', exact: true }).click()
    await current.getByRole('combobox', { name: '载入测试样例', exact: true }).selectOption('mixed')
    await current.getByRole('button', { name: '运行', exact: true }).click()
    await expect(current.getByRole('definition')).toHaveText('中文 MIXEDCASE')
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1000 })
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    }
    state.deny()
    await current.getByRole('button', { name: '核对运行结果', exact: true }).click()
    await expect(current.getByRole('alert')).toHaveText('当前无权读取运行结果')
    await expect(current.getByRole('definition')).toHaveCount(0)
    expect(state.unhandled).toEqual([])
    expect(errors).toEqual([])
  })
}
