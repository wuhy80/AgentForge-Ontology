import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'

test('从函数运行面板提交后台任务', async ({ page }) => {
  await ontologyFixture(page)
  await page.route('**/api/execution/capabilities', route => route.fulfill({ json: {
    invocation: true, async: true, synchronous: true, runtimes: ['javascript'],
  } }))
  let submitted: unknown
  await page.route('**/api/execution/invocations', async (route) => {
    submitted = route.request().postDataJSON()
    return route.fulfill({ status: 202, json: { id: 'job-run', definitionId: 'normalize', status: 'queued', mode: 'current' } })
  })
  await page.goto('/#models/normalize')
  await page.getByRole('button', { name: '打开运行面板' }).click()
  await page.getByRole('button', { name: '后台运行', exact: true }).click()
  await expect(page.getByText('已排队，可在我的任务查看')).toBeVisible()
  expect(submitted).toEqual({ definitionId: 'normalize', input: {}, async: true })
})

for (const width of [1440, 390]) test(`任务列表在 ${width}px 下可分页、查看并取消`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const fixture = await ontologyFixture(page)
  let status = 'queued'
  const job = () => ({ id: 'job-one', definitionName: '计算摘要', kind: 'function', status, attempt: 0, createdAt: '2026-11-22T10:00:00Z' })
  await page.route('**/api/execution/invocations**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/job-one')) {
      if (route.request().method() === 'DELETE') status = 'canceled'
      return route.fulfill({ json: job() })
    }
    if (url.searchParams.has('cursor')) return route.fulfill({ json: { data: [{ ...job(), id: 'job-two', definitionName: '旧任务', status: 'succeeded' }] } })
    return route.fulfill({ json: { data: [job()], nextCursor: 'cursor' } })
  })
  await page.goto('/#jobs')
  const row = page.getByRole('listitem').filter({ hasText: '计算摘要' })
  await expect(row).toContainText('排队中')
  await page.getByRole('button', { name: '加载更多' }).click()
  await expect(page.getByText('旧任务', { exact: true })).toBeVisible()
  for (const theme of ['精密白', '深空蓝']) {
    await page.getByRole('group', { name: '界面主题' }).getByRole('button', { name: theme, exact: true }).click()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await row.getByRole('button', { name: '查看详情' }).focus()
    await expect(row.getByRole('button', { name: '查看详情' })).toBeFocused()
    await page.keyboard.press('Enter')
    const detail = page.getByRole('dialog', { name: '计算摘要' })
    await expect(detail).toBeVisible()
    await detail.getByRole('button', { name: '关闭对话框' }).click()
  }
  await row.getByRole('button', { name: '取消任务' }).click()
  await page.getByRole('button', { name: '确认取消任务' }).click()
  await expect(row).toContainText('已取消')
  await page.reload()
  await expect(row).toContainText('已取消')
  expect(fixture.errors).toEqual([])
  expect(fixture.unexpected).toEqual([])
})
