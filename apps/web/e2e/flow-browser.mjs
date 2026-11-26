import assert from 'node:assert/strict'
import { chromium } from 'playwright'

let input = ''
for await (const chunk of process.stdin) input += chunk
const fixture = JSON.parse(input)
const inbox = new URL(fixture.inbox)
assert.equal(inbox.hostname, '127.0.0.1')
assert.equal(inbox.protocol, 'https:')

const browser = await chromium.launch({ channel: 'msedge' })
try {
  const context = await browser.newContext({ ignoreHTTPSErrors: true })
  await context.addCookies([{
    name: fixture.cookieName, value: fixture.cookieValue, url: inbox.origin,
    httpOnly: true, secure: true, sameSite: 'Strict',
  }])
  const page = await context.newPage()
  const response = await page.goto(inbox.href)
  assert.equal(response.status(), 200)
  const { data } = await response.json()
  assert.equal(data.length, 1)
  assert.equal(data[0].id, fixture.run)
  assert.equal(data[0].recordId, fixture.record)
  assert.equal(data[0].kind, 'lifecycle_transition')
} finally {
  await browser.close()
}
