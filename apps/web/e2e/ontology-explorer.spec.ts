import { test, expect, type Page } from '@playwright/test'
import type { DefinitionRecord, Organization } from '../src/lib/types'

// Every service request is intercepted. The fixture never uses a development
// account, queries business records, runs a callable or writes real definitions.
export async function ontologyFixture(page: Page) {
  const tenantId = 'ontology-explorer-fixture'
  const base = { tenantId, categoryId: 'knowledge', groupId: 'content' }
  const categories: Organization[] = [
    { id: 'knowledge', tenantId, name: '个人知识库' },
    { id: 'other', tenantId, name: '公共知识' },
  ]
  const groups: Organization[] = [
    { id: 'content', tenantId, categoryId: 'knowledge', name: '内容资产' },
    { id: 'organization', tenantId, categoryId: 'knowledge', name: '知识组织' },
    { id: 'computation', tenantId, categoryId: 'knowledge', name: '计算能力' },
    { id: 'shared', tenantId, categoryId: 'other', name: '公共能力' },
  ]
  const schema = (id: string, name: string): DefinitionRecord => ({ status: 'active', etag: `${id}-current`, definition: {
    ...base, id, kind: 'schema', name, description: id === 'notes' ? '记录知识内容与状态' : '整理知识主题',
    schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [
      { id: `${id}-title`, name: id === 'notes' ? '标题' : '标签名称', dataType: 'text' },
      ...(id === 'notes' ? [{ id: 'notes-status', name: '状态', dataType: 'string' as const, enumId: 'status' }] : []),
    ] },
  } })
  const link = (id: string, name: string, targetSchemaId: string, forwardName: string, reverseName: string): DefinitionRecord => ({ status: 'active', etag: `${id}-current`, definition: {
    ...base, groupId: 'organization', id, kind: 'link', name,
    link: { sourceSchemaId: 'notes', targetSchemaId, forwardName, reverseName, cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'junction', table: `kb_${id}`, sourceColumn: 'source_id', targetColumn: 'target_id' } },
  } })
  const current: DefinitionRecord[] = [
    schema('notes', '笔记'), schema('tags', '标签'),
    link('note-tags', '笔记标记标签', 'tags', '标记标签', '标记笔记'),
    link('recommended-tags', '笔记推荐标签', 'tags', '推荐标签', '被笔记推荐'),
    link('note-reference', '笔记引用笔记', 'notes', '引用笔记', '被笔记引用'),
    { status: 'active', etag: 'status-current', definition: { ...base, groupId: 'organization', id: 'status', kind: 'enum', name: '笔记状态', enum: { values: [{ code: '9007199254740993', label: '已整理' }, { code: 'old', label: '旧状态', deprecated: true }] } } },
    { status: 'active', etag: 'summarize-current', definition: { ...base, groupId: 'computation', id: 'summarize', kind: 'action', name: '生成笔记摘要', action: { sideEffects: 'none', targetSchemaIds: ['notes'], functionId: 'summary-function', input: [{ id: 'input-title', name: '标题', dataType: 'string' }], output: [{ id: 'output-summary', name: '摘要', dataType: 'longtext' }] } } },
    { status: 'active', etag: 'normalize-current', definition: { ...base, groupId: 'computation', id: 'normalize', kind: 'function', name: '规范标题', function: { sideEffects: 'none', runtime: { language: 'javascript', entrypoint: 'main', source: 'function main(input) { /* PRIVATE_SOURCE_MARKER */ return input; }', timeoutSeconds: 5 } } } },
    { status: 'active', etag: 'summary-function-current', definition: { ...base, categoryId: 'other', groupId: 'shared', id: 'summary-function', kind: 'function', name: '摘要函数', function: { sideEffects: 'none', input: [{ id: 'title', name: '标题', dataType: 'string' }], output: [{ id: 'summary', name: '摘要', dataType: 'longtext' }], runtime: { language: 'python', entrypoint: 'main', source: 'def main(input):\n    return input # PRIVATE_SOURCE_MARKER', timeoutSeconds: 5 } } } },
    { ...schema('teams', '团队'), definition: { ...schema('teams', '团队').definition, categoryId: 'other', groupId: 'shared' } },
  ]
  const draft: DefinitionRecord = { ...schema('draft-only', '未发布的秘密模型'), status: 'draft', etag: 'draft-only' }
  const state = {
    current, categories, groups,
    denyKnowledge: false,
    delayKnowledge: undefined as Promise<void> | undefined,
    heldKnowledge: 0,
    directoryRequests: [] as { categoryId: string | null; view: string | null; after: string | null }[],
    definitionReads: [] as string[],
    businessRequests: [] as string[],
    unexpected: [] as string[],
    errors: [] as string[],
  }
  page.on('pageerror', (error) => state.errors.push(error.message))
  await page.route('**/api/**', async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    const reply = async (json: unknown, status = 200) => { await route.fulfill({ status, json }).catch(() => {}) }
    if (method !== 'GET') {
      state.businessRequests.push(`${method} ${path}`)
      return reply({ error: { code: 'readonly_fixture', message: '只读浏览不应提交业务请求' } }, 405)
    }
    if (path === '/api/metadata/auth/session') return reply({ user: { id: 'reader', tenantId, username: 'reader', displayName: '本体浏览用户', enabled: true, administrator: false, roleIds: [], etag: 'reader-current' }, expiresAt: Math.floor(Date.now() / 1000) + 3600, csrfToken: 'fixture-csrf' })
    if (path === '/api/metadata/capabilities') return reply({ definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active'], schemaModes: ['schema_2_db'], storageLayouts: ['single'] })
    if (path === '/api/data/capabilities') return reply({ backend: 'mysql', maxPageSize: 100, maxSortFields: 1, operations: ['query'], links: true })
    if (path === '/api/execution/capabilities') return reply({ invocation: true, runtimes: ['python', 'javascript', 'go'], synchronous: true })
    if (path === '/api/data/sources') return reply({ data: [], hasMore: false })
    if (path === '/api/metadata/categories') return reply({ data: categories, hasMore: false })
    if (path === '/api/metadata/groups') return reply({ data: groups.filter((group) => !url.searchParams.get('categoryId') || group.categoryId === url.searchParams.get('categoryId')), hasMore: false })
    if (path === '/api/metadata/definitions') {
      const categoryId = url.searchParams.get('categoryId'), view = url.searchParams.get('view'), after = url.searchParams.get('after')
      state.directoryRequests.push({ categoryId, view, after })
      if (categoryId === 'knowledge' && state.denyKnowledge) return reply({ error: { code: 'forbidden', message: '本体目录权限已撤销' } }, 403)
      const records = (view === 'draft' ? [draft] : current).filter((record) => !categoryId || record.definition.categoryId === categoryId)
      const start = after ? Number(after.replace('page-', '')) : 0
      const data = structuredClone(records.slice(start, start + 3)), hasMore = start + 3 < records.length
      if (categoryId === 'knowledge' && state.delayKnowledge) { state.heldKnowledge++; await state.delayKnowledge }
      return reply({ data, hasMore, ...(hasMore ? { nextCursor: `page-${start + 3}` } : {}) })
    }
    const definition = /^\/api\/metadata\/definitions\/([^/]+)(\/draft)?$/.exec(path)
    if (definition) {
      const id = decodeURIComponent(definition[1])
      state.definitionReads.push(id)
      const record = definition[2] ? id === draft.definition.id ? draft : undefined : current.find((item) => item.definition.id === id)
      return record ? reply(structuredClone(record)) : reply({ error: { code: 'not_found', message: '当前定义不存在' } }, 404)
    }
    state.unexpected.push(`${method} ${path}`)
    return reply({ error: { code: 'unexpected_request', message: '隔离用例没有定义此接口' } }, 404)
  })
  return state
}

const directory = (page: Page) => page.getByRole('complementary', { name: '本体目录', exact: true })
const inspector = (page: Page) => page.getByRole('article', { name: '本体资源详情', exact: true })
const relationGraph = (page: Page) => page.getByRole('region', { name: '本体对象关系画布', exact: true })
const dependencyGraph = (page: Page) => page.getByRole('region', { name: '本体定义依赖画布', exact: true })
const assertReadOnly = (state: Awaited<ReturnType<typeof ontologyFixture>>) => {
  expect(state.businessRequests).toEqual([])
  expect(state.unexpected).toEqual([])
  expect(state.errors).toEqual([])
}

test('工作空间下方提供本体全景，直达与刷新完整读取五类当前态分页目录', async ({ page }) => {
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  await expect(page.getByRole('heading', { name: '本体全景', exact: true })).toBeVisible()
  await expect(directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true })).toBeVisible()
  const navigation = page.getByRole('navigation', { name: '工作台导航' }).getByRole('group', { name: '工作台', exact: true })
  expect((await navigation.getByRole('link').allTextContents()).map((value) => value.trim())).toEqual(['工作空间', '本体全景'])
  await expect(navigation.getByRole('link', { name: '本体全景' })).toHaveAttribute('aria-current', 'page')
  for (const name of ['查看关系：笔记标记标签', '查看枚举：笔记状态', '查看动作：生成笔记摘要', '查看函数：规范标题']) await expect(directory(page).getByRole('button', { name, exact: true })).toBeVisible()
  await expect(directory(page).getByText('未发布的秘密模型', { exact: true })).toHaveCount(0)
  expect(state.directoryRequests.some((request) => request.categoryId === 'knowledge' && request.after === 'page-6')).toBe(true)
  expect(state.definitionReads).toContain('summary-function')
  await page.reload()
  await expect(directory(page).getByRole('button', { name: '查看动作：生成笔记摘要', exact: true })).toBeVisible()
  await expect(page).toHaveURL(/#ontology$/)
  assertReadOnly(state)
})

test('业务关系、自关联和平行Link可选择，五类依赖与跨分类函数详情按稳定引用联动', async ({ page }) => {
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  for (const name of ['笔记标记标签', '笔记推荐标签', '笔记引用笔记']) await expect(relationGraph(page).getByRole('button', { name: `选择关系：${name}`, exact: true })).toBeVisible()
  await relationGraph(page).getByRole('button', { name: '选择关系：笔记标记标签', exact: true }).click()
  await expect(inspector(page).getByRole('heading', { name: '笔记标记标签', exact: true })).toBeVisible()
  await expect(inspector(page).getByText('标记笔记', { exact: true })).toBeVisible()
  await expect(inspector(page).getByText('N:M', { exact: true })).toBeVisible()
  await directory(page).getByRole('button', { name: '查看枚举：笔记状态', exact: true }).click()
  await expect(page.getByRole('tab', { name: '定义依赖图', exact: true })).toHaveAttribute('aria-selected', 'true')
  await expect(inspector(page).getByRole('table', { name: '枚举成员' }).getByText('9007199254740993', { exact: true })).toBeVisible()
  await expect(dependencyGraph(page).getByLabel('枚举：笔记状态', { exact: true })).toBeVisible()
  await directory(page).getByRole('button', { name: '查看动作：生成笔记摘要', exact: true }).click()
  await expect(inspector(page).getByText('绑定函数', { exact: true }).first()).toBeVisible()
  await expect(dependencyGraph(page).getByRole('button', { name: '选择引用来源：生成笔记摘要，绑定函数', exact: true })).toBeVisible()
  await inspector(page).getByRole('button', { name: '查看本体资源：摘要函数', exact: true }).first().click()
  await expect(inspector(page).getByRole('heading', { name: '摘要函数', exact: true })).toBeVisible()
  await expect(inspector(page).getByText('公共知识', { exact: true })).toBeVisible()
  await expect(inspector(page).getByText('Python', { exact: true })).toBeVisible()
  await expect(dependencyGraph(page).getByLabel('函数：摘要函数', { exact: true })).toBeVisible()
  await expect(page.getByText('PRIVATE_SOURCE_MARKER', { exact: false })).toHaveCount(0)
  assertReadOnly(state)
})

test('搜索和分组筛选关联时保留两端对象，定义跳转沿用稳定资源地址', async ({ page }) => {
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  await directory(page).getByRole('searchbox', { name: '搜索本体定义' }).fill('笔记标记标签')
  await directory(page).getByRole('combobox', { name: '本体类型' }).selectOption('link')
  await directory(page).getByRole('combobox', { name: '本体分组' }).selectOption('organization')
  await expect(directory(page).getByRole('button', { name: /^查看/ })).toHaveCount(1)
  await expect(relationGraph(page).getByLabel('对象模型：笔记', { exact: true })).toBeVisible()
  await expect(relationGraph(page).getByLabel('对象模型：标签', { exact: true })).toBeVisible()
  await relationGraph(page).getByLabel('对象模型：标签', { exact: true }).click()
  await expect(inspector(page).getByRole('heading', { name: '标签', exact: true })).toBeVisible()
  await directory(page).getByRole('searchbox', { name: '搜索本体定义' }).fill('')
  await directory(page).getByRole('combobox', { name: '本体类型' }).selectOption('enum')
  await directory(page).getByRole('button', { name: '查看枚举：笔记状态', exact: true }).click()
  await inspector(page).getByRole('button', { name: '查看定义', exact: true }).click()
  await expect(page).toHaveURL(/#models\/status$/)
  await expect(page.getByRole('region', { name: '模型编辑', exact: true })).toBeVisible()
  assertReadOnly(state)
})

test('分类切换拒绝旧目录响应，刷新403清除已显示目录、详情和画布', async ({ page }) => {
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  await directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true }).click()
  let release!: () => void
  state.delayKnowledge = new Promise<void>((resolve) => { release = resolve })
  await page.getByRole('button', { name: '刷新本体', exact: true }).click()
  await expect.poll(() => state.heldKnowledge).toBeGreaterThan(0)
  await page.getByRole('combobox', { name: '当前分类', exact: true }).selectOption('other')
  await expect(directory(page).getByRole('button', { name: '查看对象模型：团队', exact: true })).toBeVisible()
  state.delayKnowledge = undefined
  release()
  await expect(directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true })).toHaveCount(0)
  await expect(inspector(page)).toHaveCount(0)
  await page.getByRole('combobox', { name: '当前分类', exact: true }).selectOption('knowledge')
  await directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true }).click()
  await expect(inspector(page)).toBeVisible()
  state.denyKnowledge = true
  await page.getByRole('button', { name: '刷新本体', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: '本体目录权限已撤销' })).toBeVisible()
  await expect(directory(page)).toHaveCount(0)
  await expect(inspector(page)).toHaveCount(0)
  await expect(relationGraph(page)).toHaveCount(0)
  assertReadOnly(state)
})

test('390px目录、详情对话框和画布聚焦可用且页面无横向溢出', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  await directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '本体详情', exact: true })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('heading', { name: '笔记', exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '在画布中聚焦', exact: true }).click()
  await expect(dialog).toHaveCount(0)
  await expect(relationGraph(page)).toBeVisible()
  await expect(relationGraph(page).getByText('已聚焦相邻定义', { exact: true })).toBeVisible()
  await page.getByRole('group', { name: '浏览区域', exact: true }).getByRole('button', { name: '目录', exact: true }).click()
  await expect(directory(page)).toBeVisible()
  await directory(page).getByRole('button', { name: '查看动作：生成笔记摘要', exact: true }).click()
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('heading', { name: '生成笔记摘要', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  assertReadOnly(state)
})

test('改名保留稳定选择和个人位置，同名新定义不接管旧选择或布局身份', async ({ page }) => {
  const state = await ontologyFixture(page)
  await page.goto('/#ontology')
  const node = relationGraph(page).locator('.react-flow__node[data-id="notes"]')
  await expect(node).toBeVisible()
  await directory(page).getByRole('button', { name: '查看对象模型：笔记', exact: true }).click()
  const box = await node.boundingBox()
  expect(box).not.toBeNull()
  await page.mouse.move(box!.x + box!.width / 2, box!.y + 20)
  await page.mouse.down()
  await page.mouse.move(box!.x + box!.width / 2 + 45, box!.y + 50, { steps: 12 })
  await page.mouse.up()
  const storedPosition = () => page.evaluate(() => {
    const key = Object.keys(localStorage).find((value) => value.startsWith('agentforge:ontology-layout:') && value.endsWith(':relations'))
    return key ? JSON.parse(localStorage.getItem(key)!).positions.notes : null
  })
  await expect.poll(storedPosition).not.toBeNull()
  const position = await storedPosition()
  state.current.find((record) => record.definition.id === 'notes')!.definition.name = '知识笔记'
  await page.getByRole('button', { name: '刷新本体', exact: true }).click()
  await expect(inspector(page).getByRole('heading', { name: '知识笔记', exact: true })).toBeVisible()
  await expect.poll(storedPosition).toEqual(position)
  const index = state.current.findIndex((record) => record.definition.id === 'notes')
  state.current[index] = { ...state.current[index], definition: { ...state.current[index].definition, id: 'replacement-notes' } }
  await page.getByRole('button', { name: '刷新本体', exact: true }).click()
  await expect(directory(page).getByRole('button', { name: '查看对象模型：知识笔记', exact: true })).toBeVisible()
  await expect(inspector(page)).toHaveCount(0)
  await expect(relationGraph(page).locator('.react-flow__node[data-id="notes"]')).toHaveCount(0)
  await expect(relationGraph(page).locator('.react-flow__node[data-id="replacement-notes"]')).toBeVisible()
  await expect.poll(storedPosition).toBeFalsy()
  assertReadOnly(state)
})
