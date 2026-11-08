import test from 'node:test';
import assert from 'node:assert/strict';
import { createBrowserTransport, DataSetError } from '../dist/index.js';

const query = { from: { schemaId: 'schema' }, select: ['code'], page: { limit: 2 } };
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
const success = { data: [], fields: [], hasMore: false };

test('browser transport uses only public same-origin POST with current memory CSRF and no redirects', async () => {
  let csrf = 'first-csrf';
  const calls = [];
  const transport = createBrowserTransport({ basePath: '/api/ontoapp-ontology/data/', csrfToken: () => csrf, fetch: async (path, options) => {
    calls.push({ path, options });
    return json(path.endsWith('/bind') ? { query, fields: [] } : success);
  } });
  assert.deepEqual(await transport.bind(query), { query, fields: [] });
  csrf = 'new-csrf';
  assert.deepEqual(await transport.query(query), success);
  assert.equal(calls[0].path, '/api/ontoapp-ontology/data/query/bind');
  assert.equal(calls[1].path, '/api/ontoapp-ontology/data/query');
  for (const { options } of calls) {
    assert.equal(options.method, 'POST');
    assert.equal(options.credentials, 'same-origin');
    assert.equal(options.mode, 'same-origin');
    assert.equal(options.redirect, 'error');
    assert.equal(options.cache, 'no-store');
    assert.equal(options.headers.Authorization, undefined);
    assert.equal(options.headers['X-Tenant-ID'], undefined);
    assert.deepEqual(JSON.parse(options.body), query);
  }
  assert.equal(calls[0].options.headers['X-CSRF-Token'], 'first-csrf');
  assert.equal(calls[1].options.headers['X-CSRF-Token'], 'new-csrf');
});

test('external/ambiguous gateway paths and missing credentials are rejected before fetch', async () => {
  let calls = 0;
  const options = { csrfToken: () => 'csrf', fetch: async () => { calls++; return json(success); } };
  for (const basePath of ['https://attacker.test/api', '//attacker.test', '/api/data?next=evil', '/api/../data', '/api/%2fdata', '/api/data#fragment', '/api\\data', 'data']) {
    assert.throws(() => createBrowserTransport({ ...options, basePath }), { code: 'invalid_base_path' });
  }
  for (const csrf of ['', 'invalid token', 'csrf\r\nheader']) {
    await assert.rejects(createBrowserTransport({ ...options, csrfToken: () => csrf }).query(query), { code: 'csrf_required' });
  }
  for (const timeoutMs of [0, 60_001, 1.5, NaN]) assert.throws(() => createBrowserTransport({ ...options, timeoutMs }), { code: 'invalid_timeout' });
  assert.equal(calls, 0);
});

test('HTTP errors support public flat and nested envelopes and never retry', async () => {
  for (const payload of [{ code: 'forbidden_field', message: '字段不可读' }, { error: { code: 'forbidden_field', message: '字段不可读' } }]) {
    let calls = 0;
    const transport = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => { calls++; return json(payload, 403); } });
    await assert.rejects(transport.query(query), (error) => error instanceof DataSetError && error.status === 403 && error.code === 'forbidden_field' && error.message === '字段不可读');
    assert.equal(calls, 1);
  }
  const malformed = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => new Response('<html>not found</html>', { status: 404 }) });
  await assert.rejects(malformed.bind(query), { code: 'http_404', status: 404 });
});

test('a concurrent 401 invalidates pending responses, future queries, and calls the callback once', async () => {
  let resolveFirst;
  let calls = 0, expired = 0;
  const transport = createBrowserTransport({ csrfToken: () => 'csrf', onUnauthorized: () => { expired++; }, fetch: () => {
    calls++;
    return calls === 1 ? new Promise((resolve) => { resolveFirst = resolve; }) : Promise.resolve(json({ code: 'unauthenticated' }, 401));
  } });
  const pending = transport.query(query);
  const firstRejected = assert.rejects(pending, { code: 'unauthenticated', status: 401 });
  await assert.rejects(transport.query(query), { code: 'unauthenticated', status: 401 });
  resolveFirst(json(success));
  await firstRejected;
  await assert.rejects(transport.query(query), { code: 'unauthenticated', status: 401 });
  assert.equal(calls, 2);
  assert.equal(expired, 1);
});

test('logout invalidation discards pending output without issuing another request', async () => {
  let complete;
  const transport = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => new Promise((resolve) => { complete = resolve; }) });
  const result = transport.query(query);
  const rejection = assert.rejects(result, { status: 401, code: 'unauthenticated' });
  transport.invalidate();
  complete(json(success));
  await rejection;
  await assert.rejects(transport.bind(query), { status: 401 });
});

test('abort and timeout cancel actual requests and preserve their reasons', async () => {
  let calls = 0;
  const fetcher = (_url, { signal }) => new Promise((_resolve, reject) => {
    calls++;
    signal.addEventListener('abort', () => reject(signal.reason), { once: true });
  });
  const transport = createBrowserTransport({ csrfToken: () => 'csrf', fetch: fetcher, timeoutMs: 10 });
  const already = AbortSignal.abort(new DOMException('cancel', 'AbortError'));
  await assert.rejects(transport.query(query, already), { name: 'AbortError' });
  assert.equal(calls, 0);
  const controller = new AbortController();
  const pending = transport.query(query, controller.signal);
  controller.abort(new DOMException('cancel', 'AbortError'));
  await assert.rejects(pending, { name: 'AbortError' });
  await assert.rejects(transport.query(query), { name: 'TimeoutError' });
  assert.equal(calls, 2);
});

test('HTTP response size and malformed JSON are rejected without partial output', async () => {
  for (const response of [
    new Response('small', { headers: { 'Content-Length': String((8 << 20) + 1) } }),
    new Response(new Uint8Array((8 << 20) + 1)),
  ]) {
    const transport = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => response });
    await assert.rejects(transport.query(query), { code: 'response_budget', status: 422 });
  }
  const malformed = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => new Response('{partial') });
  await assert.rejects(malformed.query(query), { code: 'invalid_response', status: 502 });
});

test('transport errors redact connection details and do not retry', async () => {
  let calls = 0;
  const transport = createBrowserTransport({ csrfToken: () => 'csrf', fetch: async () => { calls++; throw new Error('private-connection-detail'); } });
  await assert.rejects(transport.query(query), (error) => error.code === 'network_error' && !error.message.includes('private-connection-detail'));
  assert.equal(calls, 1);
});

test('session callbacks cannot lose synchronous aborts or send requests after invalidation', async () => {
  let calls = 0;
  const controller = new AbortController();
  const fetcher = async () => { calls++; return json(success); };
  const cancelled = createBrowserTransport({ csrfToken: () => { controller.abort(new DOMException('cancel', 'AbortError')); return 'csrf'; }, fetch: fetcher });
  await assert.rejects(cancelled.query(query, controller.signal), { name: 'AbortError' });
  const expired = createBrowserTransport({ csrfToken: () => { expired.invalidate(); return 'csrf'; }, fetch: fetcher });
  await assert.rejects(expired.query(query), { code: 'unauthenticated', status: 401 });
  assert.equal(calls, 0);
});

test('the deadline also cancels a stalled response body', async () => {
  let cancelled = false;
  const response = new Response(new ReadableStream({ cancel() { cancelled = true; } }));
  const transport = createBrowserTransport({ csrfToken: () => 'csrf', timeoutMs: 10, fetch: async () => response });
  await assert.rejects(transport.query(query), { name: 'TimeoutError' });
  assert.equal(cancelled, true);
});
