import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'

for (const kind of ['schema', 'link', 'enum', 'managed-schema', 'managed-link']) for (const width of [1440, 390]) test(`草稿删除在 ${kind} ${width}px 下需要预览与确认`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const fixture = await ontologyFixture(page)
  if (kind === 'link') {
    const definition = { ...fixture.current.find(item => item.definition.id === 'note-tags')!.definition, id: 'draft-only' }
    await page.route('**/api/metadata/definitions/draft-only/draft', route => route.fulfill({ json: { definition, status: 'draft', etag: 'draft-only' } }))
  }
  const publishedId = { enum: 'status', 'managed-schema': 'tags', 'managed-link': 'note-tags' }[kind]
  const id = publishedId ?? 'draft-only'
  const entryName = { enum: '删除枚举', 'managed-schema': '删除模型', 'managed-link': '删除关系' }[kind] ?? '删除草稿'
  const current = fixture.current.find(item => item.definition.id === publishedId)
  if (current) {
    current.status = 'disabled'
    if (current.definition.link) current.definition.link.storageBinding = { mode: 'managed' }
  }
  const etag = current?.etag ?? 'draft-only'
  let deleted = false
  await page.route('**/api/metadata/capabilities', route => route.fulfill({ json: { unpublishedDraftDelete: true, disabledEnumDelete: true, disabledManagedStorageDelete: true } }))
  await page.route(`**/api/metadata/definitions/${id}/delete-preview`, async route => {
    expect(route.request().method()).toBe('GET')
    expect(route.request().headers()['if-match']).toBe(`"${etag}"`)
    await route.fulfill({ status: 204 })
  })
  await page.route(`**/api/metadata/definitions/${id}`, async route => {
    if (route.request().method() !== 'DELETE') return route.fallback()
    if (current) fixture.current.splice(fixture.current.indexOf(current), 1)
    expect(route.request().headers()['if-match']).toBe(`"${etag}"`)
    deleted = true
    await route.fulfill({ status: 204 })
  })
  await page.route('**/api/metadata/definitions?*', route => {
    if (deleted && new URL(route.request().url()).searchParams.get('view') === 'draft') return route.fulfill({ json: { data: [], hasMore: false } })
    return route.fallback()
  })
  await page.goto(`/#models/${id}`)
  for (const theme of ['精密白', '深空蓝']) {
    await page.getByRole('group', { name: '界面主题' }).getByRole('button', { name: theme, exact: true }).click()
    const entry = page.getByRole('button', { name: entryName, exact: true })
    await entry.focus()
    await page.keyboard.press('Enter')
    const cancel = page.getByRole('button', { name: '取消', exact: true })
    await expect(cancel).toBeFocused()
    await expect(page.getByRole('button', { name: '确认删除', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await cancel.click()
    expect(deleted).toBe(false)
  }
  await page.getByRole('button', { name: entryName, exact: true }).click()
  await page.getByRole('button', { name: '确认删除', exact: true }).click()
  await expect(page).not.toHaveURL(new RegExp(`/models/${id}(?:$|[?])`))
  await expect(page.getByRole('heading', { name: '未发布的秘密模型' })).toHaveCount(0)
  expect(deleted).toBe(true)
})
