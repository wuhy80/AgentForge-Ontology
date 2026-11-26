import assert from 'node:assert/strict'
import { resolve, sep } from 'node:path'
import { readFile } from 'node:fs/promises'
import { chromium } from 'playwright'

let input = ''
for await (const chunk of process.stdin) input += chunk
const fixture = JSON.parse(input)
const inbox = new URL(fixture.inbox)
assert.equal(inbox.hostname, '127.0.0.1')
assert.equal(inbox.protocol, 'https:')

let stage = 'launch'
const browser = await chromium.launch({ channel: 'msedge' })
try {
  const context = await browser.newContext({ ignoreHTTPSErrors: true })
  await context.addCookies([{
    name: fixture.cookieName, value: fixture.cookieValue, url: inbox.origin,
    httpOnly: true, secure: true, sameSite: 'Strict',
  }])
  const page = await context.newPage()
  stage = 'read Data inbox'
  const response = await page.goto(inbox.href)
  stage = 'Data inbox HTTP ' + response.status()
  assert.equal(response.status(), 200)
  const { data } = await response.json()
  assert.equal(data.length, 1)
  assert.equal(data[0].id, fixture.run)
  assert.equal(data[0].recordId, fixture.record)
  assert.equal(data[0].kind, 'lifecycle_transition')
  stage = 'read OntoApp build'
  if (fixture.appDist) {
    const dist = resolve(fixture.appDist)
    const html = await readFile(resolve(dist, 'index.html'), 'utf8')
    const base = html.match(/src="([^"]*)assets\/index-[^"]+\.js"/)?.[1]
    assert.ok(base?.startsWith('/') && !base.startsWith('//'))
    const metadata = new URL(fixture.metadata)
    assert.equal(metadata.hostname, '127.0.0.1')
    assert.equal(metadata.protocol, 'https:')
    stage = 'fixture configuration'
    const app = { format: 'ontoapp/v1', id: 'flow-app', name: 'Flow', description: '', pages: [{
      id: 'flow-page', name: 'Flow', description: '', blocks: [{
        id: 'flow-detail', kind: 'detail', title: 'Flow details', width: 'full', schemaId: fixture.schema,
        fieldIds: [fixture.field], content: '', pageSize: 10, sortField: '', sortDirection: 'asc', filterField: '', filterValue: '', detailOptions: { sourceBlockId: '', editFieldIds: [], allowDelete: false, relations: [] },
      }],
    }] }
    await page.route(url => url.origin === inbox.origin && url.pathname.startsWith(base), async route => {
      const path = new URL(route.request().url()).pathname.slice(base.length)
      if (path.includes('api/')) return route.fallback()
      const file = resolve(dist, path || 'index.html')
      assert.ok(file.startsWith(dist + sep))
      await route.fulfill({ path: file })
    })
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path.endsWith('/workspace')) {
        return route.fulfill({ json: { workspace: { draft: app, published: app }, revision: 'fixture' } })
      }
      const match = /\/api\/(?:ontoapp-ontology\/)?((?:data|metadata)\/.*)$/.exec(path)
      if (!match || route.request().method() !== 'GET') return route.fulfill({ status: 404 })
      const url = new URL('/' + match[1], match[1].startsWith('metadata/') ? metadata : inbox)
      url.search = new URL(route.request().url()).search
      await route.fulfill({ response: await route.fetch({ url: url.href, maxRedirects: 0, headers: { cookie: fixture.cookieName + '=' + fixture.cookieValue } }) })
    })
    stage = 'load OntoApp'
    await page.goto(new URL(base + '#/pages/flow-page?mode=run', inbox).href)
    stage = 'open inbox'
    await page.getByRole('button', { name: /\u6536\u4ef6\u7bb1/ }).click()
    const notice = page.getByRole('link', { name: '\u8bb0\u5f55\u5df2\u6d41\u8f6c' })
    stage = 'open notice'
    await notice.click()
    assert.ok(page.url().includes(fixture.record))
    stage = 'display target record'
    await page.getByText(fixture.title, { exact: true }).waitFor()
  }
} catch {
  console.log(stage)
  process.exitCode = 1
} finally {
  await browser.close()
}
