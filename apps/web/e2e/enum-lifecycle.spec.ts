import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'

for (const width of [1440, 390]) test(`枚举生命周期在 ${width}px 下可预览、停用和恢复`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const fixture = await ontologyFixture(page)
  const record = fixture.current.find((item) => item.definition.id === 'status')!
  await page.route('**/api/metadata/capabilities', route => route.fulfill({ json: {
    definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active', 'disabled'], enumLifecycle: true, callableLifecyclePreview: true,
  } }))
  await page.route('**/api/metadata/definitions/status/*', (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/draft')) return route.fallback()
    const targetStatus = path.includes('restore') ? 'active' : 'disabled'
    if (path.endsWith('-preview')) return route.fulfill({ json: { definitionId: 'status', targetStatus } })
    record.status = targetStatus
    return route.fulfill({ json: record })
  })
  await page.goto('/#models/status')
  for (const theme of ['精密白', '深空蓝']) {
    await page.getByRole('group', { name: '界面主题' }).getByRole('button', { name: theme, exact: true }).click()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const disable = page.getByRole('button', { name: '停用定义', exact: true })
    await disable.focus()
    await expect(disable).toBeFocused()
    await page.keyboard.press('Enter')
    await page.getByRole('button', { name: '确认停用', exact: true }).click()
    await expect(page.getByText('定义已停用。')).toBeVisible()
    await page.getByRole('button', { name: '恢复定义', exact: true }).click()
    await page.getByRole('button', { name: '确认恢复', exact: true }).click()
    await expect(page.getByText('定义已恢复。')).toBeVisible()
  }
})
