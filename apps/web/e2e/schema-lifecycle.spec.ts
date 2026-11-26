import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'

for (const width of [1440, 390]) test(`记录流程在 ${width}px 下可配置并随草稿保存`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  await ontologyFixture(page)
  let saved: { schema?: { lifecycle?: { initialStageId: string; stages: { id: string; name: string; notifyAccountIds?: string[] }[]; transitions?: { name: string; from: string; to: string }[] } } } | undefined
  await page.route('**/api/metadata/definitions/draft-only/draft', async (route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    saved = route.request().postDataJSON()
    await route.fulfill({ json: {
      definition: { ...saved, id: 'draft-only', tenantId: 'ontology-explorer-fixture', categoryId: 'knowledge', groupId: 'content', kind: 'schema' },
      status: 'draft', etag: 'saved',
    } })
  })
  await page.goto('/#models/draft-only')
  await page.getByRole('tab', { name: '记录流程' }).click()
  await page.getByRole('button', { name: '添加记录流程' }).click()
  await expect(page.getByLabel('阶段1名称')).toHaveValue('草稿')
  await page.getByRole('button', { name: '添加阶段' }).click()
  await page.getByLabel('阶段2名称').fill('审核中')
  const account = 'f147fbc8-ba4d-455f-a158-b68d533941a3'
  await page.getByText('阶段2通知账号', { exact: true }).click()
  await page.getByRole('button', { name: '添加通知账号' }).click()
  const recipient = page.getByLabel('阶段2通知账号1')
  await recipient.focus()
  await expect(recipient).toBeFocused()
  await recipient.fill(account)
  await page.getByRole('button', { name: '添加流转' }).click()
  await page.getByLabel('流转1名称').fill('提交审核')

  for (const theme of ['精密白', '深空蓝']) {
    await page.getByRole('group', { name: '界面主题' }).getByRole('button', { name: theme, exact: true }).click()
    await expect(page.getByLabel('阶段2名称')).toHaveValue('审核中')
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(1)
  }

  await page.getByRole('button', { name: '保存草稿' }).click()
  await expect.poll(() => saved?.schema?.lifecycle?.stages?.length).toBe(2)
  const lifecycle = saved!.schema!.lifecycle!
  expect(lifecycle.stages[1].notifyAccountIds).toEqual([account])
  expect(lifecycle.initialStageId).toBe(lifecycle.stages[0].id)
  expect(lifecycle.transitions).toHaveLength(1)
  expect(lifecycle.transitions![0]).toMatchObject({ name: '提交审核', from: lifecycle.stages[0].id, to: lifecycle.stages[1].id })
})
