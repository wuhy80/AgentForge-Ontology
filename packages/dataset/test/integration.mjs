// Opt-in public integration runner controlled by tests/integration Go fixtures.
// Credentials arrive over stdin only; stdout never contains service data,
// response bodies, cookies, CSRF values, query parameters or raw diagnostics.
import { strict as assert } from 'node:assert';
import { request } from 'node:https';
import { createInterface } from 'node:readline';
import { DataSet, DataSetError } from '../dist/index.js';

let fixture;
let stable;
let originalID;
let readerPage;
let readerTransport;
let readerBound;
let adminTransport;
let counts = { bind: 0, query: 0 };
let checkpoint;

function transport(auth) {
  const post = (suffix, query, signal) => new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    const body = Buffer.from(JSON.stringify(query));
    const pending = request(new URL(`/data${suffix}`, fixture.dataURL), {
      ca: fixture.ca,
      agent: false,
      // The fixture's signed certificate covers its loopback URL. The public
      // virtual Host is an application routing/origin value, not its TLS name.
      // Explicit empty SNI keeps certificate verification on the URL hostname.
      servername: '',
      minVersion: 'TLSv1.2',
      method: 'POST',
      signal,
      headers: {
        Host: fixture.host, Origin: fixture.origin,
        Cookie: auth.cookie, 'X-CSRF-Token': auth.csrf,
        'Content-Type': 'application/json', 'Content-Length': body.length,
      },
    }, response => {
      const pieces = [];
      let bytes = 0;
      response.on('data', piece => {
        bytes += piece.length;
        if (bytes > (8 << 20)) {
          pending.destroy();
          reject(new DataSetError('response budget', 'response_budget', 422));
        } else pieces.push(piece);
      });
      response.on('end', () => {
        try {
          assert.match(response.headers['cache-control'] ?? '', /no-store/);
          const result = JSON.parse(Buffer.concat(pieces).toString('utf8'));
          if (response.statusCode !== 200) {
            reject(new DataSetError('public query rejected', result.code ?? 'http_error', response.statusCode));
            return;
          }
          resolve(result);
        } catch { reject(new DataSetError('invalid public response', 'invalid_response', 502)); }
      });
      response.on('error', () => reject(new DataSetError('public response failed', 'transport_error')));
    });
    pending.setTimeout(15_000, () => pending.destroy());
    pending.on('error', () => reject(new DataSetError('public transport failed', 'transport_error')));
    pending.end(body);
  });
  return {
    bind(query, signal) { counts.bind++; return post('/query/bind', query, signal); },
    query(query, signal) { counts.query++; return post('/query', query, signal); },
  };
}

function compareResult(left, right) {
  const stripCursor = ({ nextCursor: _nextCursor, fields, ...rest }) => ({
    ...rest,
    // Compare the public logical descriptor on both sides, excluding physical
    // metadata that the SDK does not accept as a query field descriptor.
    fields: fields.map(field => Object.fromEntries(
      ['id', 'name', 'dataType', 'required', 'nullable', 'readOnly', 'precision', 'scale', 'enumId', 'enumValues', 'enumEtag']
        .filter(key => field[key] !== undefined).map(key => [key, field[key]]),
    )),
  });
  assert.deepEqual(stripCursor(left), stripCursor(right));
}

async function comparePages(bound, currentTransport, aggregate, expectedRows) {
  let page = await bound.execute();
  const spec = bound.toQuery();
  const seen = new Set();
  let all = 0;
  for (let i = 0; i < 10; i++) {
    const direct = await currentTransport.query(spec);
    compareResult(page.result, direct);
    const rows = aggregate ? page.result.aggregateRows : page.result.data;
    assert.equal(rows.length, 1);
    const key = aggregate ? JSON.stringify(rows[0].values) : rows[0].id;
    assert.equal(seen.has(key), false);
    seen.add(key);
    all += rows.length;
    if (!page.result.hasMore) {
      assert.equal(await page.nextPage(), undefined);
      break;
    }
    assert.equal(typeof page.result.nextCursor, 'string');
    spec.page.cursor = page.result.nextCursor;
    page = await page.nextPage();
    assert.ok(page);
  }
  assert.equal(all, expectedRows);
}

async function initial(message) {
  checkpoint = 'initial-plan';
  fixture = message;
  assert.ok(process.versions.node.split('.').map(Number)[0] >= 22);
  adminTransport = transport(message.auth);
  const base = DataSet.from(adminTransport, { category: '模型演进', schema: '工单' });
  const chosen = base.select('代码', '优先级').where({ op: 'eq', field: '代码', value: 'new' }).orderBy('代码', 'asc').limit(1);
  assert.deepEqual(counts, { bind: 0, query: 0 });
  assert.equal(chosen.isBound, false);
  checkpoint = 'initial-bind';
  stable = await chosen.bind();
  assert.equal(chosen.isBound, false);
  assert.equal(stable.isBound, true);
  assert.equal(stable.toQuery().from.schemaId, fixture.schemaId);
  assert.equal(stable.toQuery().from.schema, undefined);
  assert.deepEqual(stable.toQuery().select, [fixture.code, fixture.priority]);
  assert.equal(stable.toQuery().where.field, fixture.code);
  checkpoint = 'initial-entity';
  const first = await stable.execute();
  compareResult(first.result, await adminTransport.query(stable.toQuery()));
  assert.equal(first.result.data.length, 1);
  assert.equal(first.result.data[0].values[fixture.priority], '9007199254740993');
  originalID = first.result.data[0].id;
  assert.equal(await first.nextPage(), undefined);

  checkpoint = 'entity-pages';
  const entities = await base.select('代码', '状态', '优先级').orderBy('代码', 'asc').limit(1).bind();
  await comparePages(entities, adminTransport, false, 3);
  checkpoint = 'summary-bind';
  const summary = await base.aggregate({ measures: [
    { id: 'mCount', op: 'count', name: '数量' },
    { id: 'mSum', op: 'sum', field: '优先级', name: '合计' },
    { id: 'mAvg', op: 'avg', field: '优先级', name: '平均' },
  ] }).limit(10).bind();
  checkpoint = 'summary-result';
  const sum = await summary.execute();
  compareResult(sum.result, await adminTransport.query(summary.toQuery()));
  assert.equal(sum.result.aggregateMode, true);
  assert.equal(sum.result.data.length, 0);
  assert.deepEqual(sum.result.aggregateRows[0].values, {
    mCount: '3', mSum: '27021597764222979', mAvg: '9007199254740993.0000',
  });
  checkpoint = 'group-pages';
  const grouped = await base.aggregate({ groupBy: ['状态'], measures: [{ id: 'mCount', op: 'count' }] }).limit(1).bind();
  await comparePages(grouped, adminTransport, true, 3);
  // Caller mutations of a descriptor or fields cannot alter a bound identity.
  checkpoint = 'descriptor-copy';
  const snapshot = stable.toQuery();
  snapshot.from.schemaId = 'caller-mutated';
  assert.equal(stable.toQuery().from.schemaId, fixture.schemaId);
}

async function renamed() {
  checkpoint = 'renamed-existing';
  const result = await stable.execute();
  compareResult(result.result, await adminTransport.query(stable.toQuery()));
  assert.equal(result.result.data.length, 1);
  assert.equal(result.result.data[0].id, originalID);
  assert.equal(result.result.data[0].schemaId, fixture.schemaId);
  assert.equal(result.result.data[0].values[fixture.priority], '9007199254740993');
  checkpoint = 'renamed-fresh';
  const newName = await DataSet.from(adminTransport, { category: '模型演进', schema: '工单' }).select('代码', '优先级').where({ op: 'eq', field: '代码', value: 'new' }).limit(1).bind();
  assert.notEqual(newName.toQuery().from.schemaId, fixture.schemaId);
  const fresh = await newName.execute();
  assert.equal(fresh.result.data.length, 1);
  assert.notEqual(fresh.result.data[0].id, originalID);
  const freshPriority = newName.fields.find(field => field.name === '优先级').id;
  assert.equal(fresh.result.data[0].values[freshPriority], '1');
}

async function reader(message) {
  checkpoint = 'reader-page';
  readerTransport = transport(message.auth);
  readerBound = await DataSet.from(readerTransport, { schemaId: fixture.schemaId }).select(fixture.code).orderBy(fixture.code, 'asc').limit(1).bind();
  readerPage = await readerBound.execute();
  compareResult(readerPage.result, await readerTransport.query(readerBound.toQuery()));
  assert.equal(readerPage.result.hasMore, true);
  assert.deepEqual(Object.keys(readerPage.result.data[0].values), [fixture.code]);
  checkpoint = 'reader-hidden';
  await assert.rejects(() => DataSet.from(readerTransport, { schemaId: fixture.schemaId }).select(fixture.priority).limit(1).bind(), error => error instanceof DataSetError && error.status === 403);
}

async function revoked() {
  checkpoint = 'revoked-page';
  const before = counts.query;
  await assert.rejects(() => readerPage.nextPage(), error => error instanceof DataSetError && error.status === 403);
  assert.equal(counts.query, before + 1);
  checkpoint = 'revoked-execute';
  await assert.rejects(() => readerBound.execute(), error => error instanceof DataSetError && error.status === 403);
  await assert.rejects(() => readerTransport.query(readerBound.toQuery()), error => error instanceof DataSetError && error.status === 403);
}

for await (const line of createInterface({ input: process.stdin, crlfDelay: Infinity })) {
  let stage = 'invalid-input';
  try {
    const message = JSON.parse(line);
    stage = message.stage;
    if (stage === 'stop') break;
    if (stage === 'initial') await initial(message);
    else if (stage === 'renamed') await renamed();
    else if (stage === 'reader') await reader(message);
    else if (stage === 'revoked') await revoked();
    else throw new Error('unknown stage');
    process.stdout.write(`${JSON.stringify({ stage, ok: true })}\n`);
  } catch {
    process.stdout.write(`${JSON.stringify({ stage, ok: false, checkpoint })}\n`);
    process.exitCode = 1;
    break;
  }
}
