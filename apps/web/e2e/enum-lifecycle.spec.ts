import { test, expect } from '@playwright/test'
import { ontologyFixture } from './fixtures/ontology'

for (const kind of ['enum', 'link', 'junction', 'foreign_key', 'schema', 'mapped_schema', 'forward_schema', 'write_action', 'write_function'] as const) for (const width of [1440, 390]) test(`${kind} 生命周期在 ${width}px 下可预览、停用和恢复`, async ({ page }) => {
  await page.setViewportSize({ width, height: 844 })
  const fixture = await ontologyFixture(page)
  const id = kind === 'write_action' ? 'summarize' : kind === 'write_function' ? 'normalize' : kind === 'enum' ? 'status' : kind === 'schema' || kind === 'mapped_schema' || kind === 'forward_schema' ? 'tags' : 'note-tags'
  const mapped = kind === 'junction' || kind === 'foreign_key'
  const record = fixture.current.find((item) => item.definition.id === id)!
  const mappedSchema = kind === 'mapped_schema' || kind === 'forward_schema'
  if (mappedSchema) {
    const schema = record.definition.schema!
    schema.mode = kind === 'mapped_schema' ? 'db_2_schema' : 'schema_2_db'
    schema.storageBinding = { dataSourceId: 'source', table: 'tags', idColumn: 'id' }
    for (const property of schema.properties) property.mapping = { dataSourceId: 'source', column: 'name' }
  }
  const callable = record.definition.action ?? record.definition.function
  if (callable) {
    callable.sideEffects = 'dataWrite'
    callable.dataWrite = [{ schemaId: 'notes', operations: ['create'] }]
  }
  if (record.definition.link) {
    record.definition.link.storageBinding = kind === 'junction'
      ? { mode: kind, dataSourceId: 'source', table: 'edges', sourceColumn: 'note_id', targetColumn: 'tag_id' }
      : kind === 'foreign_key' ? { mode: kind, dataSourceId: 'source', foreignKeyColumn: 'tag_id', foreignKeySide: 'source' }
      : { mode: 'managed' }
  }
  await page.route('**/api/metadata/capabilities', route => route.fulfill({ json: {
    definitions: ['schema', 'enum', 'link', 'action', 'function'], lifecycle: ['draft', 'active', 'disabled'], enumLifecycle: true, managedLinkLifecycle: true, mappedLinkLifecycle: true, disabledMappedLinkDelete: true, managedSchemaLifecycle: true, mappedSchemaLifecycle: true, disabledMappedSchemaDelete: true, callableLifecyclePreview: true, writeCallableLifecycle: true,
  } }))
  await page.route(`**/api/metadata/definitions/${id}/*`, (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/draft')) return route.fallback()
    if (path.endsWith('/delete-preview')) return route.fulfill({ status: 204 })
    const targetStatus = path.includes('restore') ? 'active' : 'disabled'
    if (path.endsWith('-preview')) return route.fulfill({ json: { definitionId: id, targetStatus } })
    record.status = targetStatus
    return route.fulfill({ json: record })
  })
  await page.goto(`/#models/${id}`)
  for (const theme of ['精密白', '深空蓝']) {
    await page.getByRole('group', { name: '界面主题' }).getByRole('button', { name: theme, exact: true }).click()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const disable = page.getByRole('button', { name: '停用定义', exact: true })
    await disable.focus()
    await expect(disable).toBeFocused()
    await page.keyboard.press('Enter')
    await page.getByRole('button', { name: '确认停用', exact: true }).click()
    await expect(page.getByText('定义已停用。')).toBeVisible()
    if (mappedSchema) {
      await expect(page.getByRole('button', { name: '删除模型', exact: true })).toBeVisible()
    }
    if (mapped) {
      await page.getByRole('button', { name: '删除关系', exact: true }).click()
      await expect(page.getByText('确认删除此已停用的关系？删除后无法恢复。')).toBeVisible()
      const cancel = page.getByRole('button', { name: '取消', exact: true })
      await expect(cancel).toBeFocused()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      if (theme === '深空蓝') await cancel.click()
    }
    await page.getByRole('button', { name: '恢复定义', exact: true }).click()
    await page.getByRole('button', { name: '确认恢复', exact: true }).click()
    await expect(page.getByText('定义已恢复。')).toBeVisible()
  }
  if (mapped) {
    await page.route(`**/api/metadata/definitions/${id}`, route => {
      if (route.request().method() !== 'DELETE') return route.fallback()
      fixture.current.splice(fixture.current.indexOf(record), 1)
      return route.fulfill({ status: 204 })
    })
    await page.getByRole('button', { name: '停用定义', exact: true }).click()
    await page.getByRole('button', { name: '确认停用', exact: true }).click()
    await page.getByRole('button', { name: '删除关系', exact: true }).click()
    const deleted = page.waitForResponse(response => response.request().method() === 'DELETE' && response.url().endsWith(`/definitions/${id}`))
    await page.getByRole('button', { name: '确认删除', exact: true }).click()
    expect((await deleted).status()).toBe(204)
    await expect(page.getByRole('button', { name: '恢复定义', exact: true })).toHaveCount(0)
  }
})
