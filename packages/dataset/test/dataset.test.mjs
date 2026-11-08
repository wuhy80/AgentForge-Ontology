import assert from 'node:assert/strict';
import { test } from 'node:test';
import { DataSet, DataSetError } from '../dist/index.js';

const fields = [
  { id: 'code-id', name: '编码', dataType: 'string', required: true, readOnly: false },
  { id: 'amount-id', name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: true },
];
const names = { 编码: 'code-id', 金额: 'amount-id', 数量: 'count-id', 时间: 'time-id' };
const byID = { ...Object.fromEntries(fields.map((field) => [field.id, field])), 'count-id': { id: 'count-id', name: '数量', dataType: 'int64' }, 'time-id': { id: 'time-id', name: '时间', dataType: 'timestamp' } };
const source = { category: '业务', schema: '订单' };
function canonical(query) {
  const field = (name) => names[name] ?? name;
  const visit = (node) => node.args ? { ...node, args: node.args.map(visit) } : { ...node, field: field(node.field) };
  return { ...query, from: { schemaId: 'orders-id' }, select: query.select.map(field), ...(query.where ? { where: visit(query.where) } : {}), ...(query.orderBy ? { orderBy: query.orderBy.map((order) => ({ ...order, field: field(order.field) })) } : {}), ...(query.aggregate ? { aggregate: { ...query.aggregate, ...(query.aggregate.groupBy ? { groupBy: query.aggregate.groupBy.map(field) } : {}), measures: query.aggregate.measures.map((measure) => ({ ...measure, ...(measure.field ? { field: field(measure.field) } : {}) })) } } : {}) };
}
function outputFields(query) {
  return query.aggregate ? [...(query.aggregate.groupBy ?? []).map((id) => byID[id]), ...query.aggregate.measures.map((measure) => ({ id: measure.id, name: measure.name || measure.id, dataType: measure.op.startsWith('count') ? 'int64' : 'decimal', readOnly: true }))] : query.select.map((id) => byID[id]);
}
function entityResult(query, options = {}) {
  return { data: [{ id: 'record-id', schemaId: query.from.schemaId, etag: 'etag', values: Object.fromEntries(query.select.map((id) => [id, id === 'amount-id' ? '9007199254740993.000001' : id === 'count-id' ? '9223372036854775807' : id === 'time-id' ? '2026-11-08T15:01:02.123456+08:00' : 'A'])) }], fields: outputFields(query), hasMore: false, ...options };
}
function fixture(queryFunction = entityResult) {
  const calls = { bind: [], query: [] };
  const transport = {
    async bind(query, signal) { calls.bind.push({ query: structuredClone(query), signal }); const bound = canonical(query); return { query: bound, fields: outputFields(bound) }; },
    async query(query, signal) { calls.query.push({ query: structuredClone(query), signal }); return queryFunction(query, calls.query.length); },
  };
  return { transport, calls };
}
const errorCode = (code) => (error) => error instanceof DataSetError && error.code === code;

test('construction and branching perform no I/O, AND predicates detach inputs, select/order replace', () => {
  const { transport, calls } = fixture();
  const initialSource = { ...source };
  const condition = { op: 'in', field: '编码', value: ['A', 'B'] };
  const base = DataSet.from(transport, initialSource).select('编码');
  const left = base.where(condition).where({ op: 'gte', field: '金额', value: '9007199254740993.000001' }).orderBy('金额').orderBy('编码', 'desc').limit(10);
  const right = base.select('金额');
  initialSource.schema = '替换模型'; condition.value.push('MUTATED'); condition.field = 'MUTATED';
  assert.deepEqual(calls, { bind: [], query: [] });
  assert.deepEqual(base.toQuery(), { from: source, select: ['编码'], page: { limit: 25 } });
  assert.deepEqual(right.toQuery().select, ['金额']);
  assert.deepEqual(left.toQuery().where, { op: 'and', args: [{ op: 'in', field: '编码', value: ['A', 'B'] }, { op: 'gte', field: '金额', value: '9007199254740993.000001' }] });
  assert.deepEqual(left.toQuery().orderBy, [{ field: '编码', direction: 'desc' }]);
  const exported = left.toQuery(); exported.where.args[0].value.push('EXPORTED'); exported.from.schema = 'EXPORTED';
  assert.equal(left.toQuery().from.schema, '订单');
  assert.deepEqual(left.toQuery().where.args[0].value, ['A', 'B']);
  assert.equal(base.isBound, false);
});

test('runtime rejects imprecise/non-JSON values, unsupported combinations and malformed structure', () => {
  const { transport } = fixture();
  const dataset = DataSet.from(transport, source).select('金额');
  for (const value of [1, 9007199254740993, NaN, Infinity, 1n, undefined, {}, new Date(), ['nested']]) {
    assert.throws(() => dataset.where({ op: 'eq', field: '金额', value }), DataSetError);
  }
  for (const value of [[['nested']], [1], [undefined], [], new Array(1)]) assert.throws(() => dataset.where({ op: 'in', field: '金额', value }), DataSetError);
  const getter = []; Object.defineProperty(getter, '0', { get() { throw new Error('getter must not run'); }, enumerable: true });
  assert.throws(() => dataset.where({ op: 'in', field: '金额', value: getter }), DataSetError);
  for (const key of ['rows', 'via', 'execute', 'unknown']) assert.throws(() => DataSet.fromSpec(transport, { ...dataset.toQuery(), [key]: {} }), errorCode('unsupported_query'));
  for (const extra of [{ parameter: '金额' }, { valueRef: { source: 'user' } }]) assert.throws(() => dataset.where({ op: 'eq', field: '金额', value: '1', ...extra }), errorCode('unsupported_query'));
  assert.throws(() => DataSet.fromSpec(transport, { ...dataset.toQuery(), page: { limit: 25, cursor: 'external-cursor' } }), DataSetError);
  assert.throws(() => DataSet.from(transport, { ...source, schemaId: 'mixed' }), DataSetError);
  assert.throws(() => dataset.orderBy('金额', 'random'), DataSetError);
  for (const limit of [0, 101, 1.5, NaN, '10']) assert.throws(() => dataset.limit(limit), DataSetError);
});

test('execute requires explicit binding; bound IDs survive rename/replacement, derivatives require rebind except limit', async () => {
  const { transport, calls } = fixture();
  const unbound = DataSet.from(transport, source).select('金额').where({ op: 'gte', field: '金额', value: '0.000001' });
  await assert.rejects(unbound.execute(), errorCode('not_bound'));
  assert.equal(calls.query.length, 0);
  const bound = await unbound.bind();
  assert.equal(unbound.isBound, false); assert.equal(bound.isBound, true);
  assert.deepEqual(bound.toQuery().from, { schemaId: 'orders-id' });
  assert.deepEqual(bound.toQuery().select, ['amount-id']);
  assert.equal(bound.toQuery().where.field, 'amount-id');
  transport.bind = async () => { throw new Error('must not rebind during execute'); };
  const page = await bound.limit(10).execute();
  assert.equal(page.result.data[0].values['amount-id'], '9007199254740993.000001');
  assert.deepEqual(calls.query[0].query.from, { schemaId: 'orders-id' });
  assert.equal(calls.query[0].query.page.limit, 10);
  assert.equal(calls.bind.length, 1);
  for (const derived of [bound.select('编码'), bound.where({ op: 'isNull', field: 'amount-id' }), bound.orderBy('amount-id'), bound.aggregate({ measures: [{ id: 'm0', op: 'count' }] })]) {
    assert.equal(derived.isBound, false); assert.deepEqual(derived.toQuery().from, { schemaId: 'orders-id' });
    await assert.rejects(derived.execute(), errorCode('not_bound'));
  }
  assert.equal(DataSet.fromSpec(transport, bound.toQuery()).isBound, false);
});

test('binding/output snapshots preserve safe enum descriptors without physical metadata or mutable aliases', async () => {
  const response = { query: { from: { schemaId: 'orders-id' }, select: ['state-id'], page: { limit: 25 }, orderBy: [] }, fields: [{ id: 'state-id', name: '状态', dataType: 'string', required: true, nullable: false, readOnly: false, enumId: 'enum-id', enumEtag: 'enum-etag', enumValues: [{ code: 'READY', label: '就绪', deprecated: false }], mapping: { dataSourceId: 'private' }, default: 'private' }] };
  const transport = { async bind() { return response; }, async query(query) { return { data: [], fields: response.fields, hasMore: false }; } };
  const bound = await DataSet.from(transport, source).select('状态').bind();
  response.fields[0].enumValues[0].label = 'MUTATED';
  response.query.from.schemaId = 'MUTATED';
  const metadata = bound.fields; metadata[0].enumValues[0].label = 'EXPORTED';
  assert.equal(bound.fields[0].enumValues[0].label, '就绪');
  assert.equal(bound.fields[0].required, true); assert.equal(bound.fields[0].readOnly, false);
  assert.equal('mapping' in bound.fields[0], false); assert.equal('default' in bound.fields[0], false);
  assert.deepEqual(bound.toQuery().from, { schemaId: 'orders-id' });
  assert.equal(bound.toQuery().orderBy, undefined);
});

test('binding rejects changed stable identity/mode/budget and mismatched field output', async () => {
  const expected = { from: { schemaId: 'orders-id' }, select: ['amount-id'], page: { limit: 25 } };
  for (const query of [{ ...expected, from: { schemaId: 'replacement' } }, { ...expected, page: { limit: 100 } }, { ...expected, select: [], aggregate: { measures: [{ id: 'm0', op: 'count' }] } }]) {
    const transport = { async bind() { return { query, fields: outputFields(query) }; }, async query() { throw new Error(); } };
    await assert.rejects(DataSet.from(transport, { schemaId: 'orders-id' }).select('amount-id').bind(), errorCode('invalid_binding'));
  }
  const transport = { async bind() { return { query: expected, fields: [fields[0]] }; }, async query() { throw new Error(); } };
  await assert.rejects(DataSet.from(transport, source).select('金额').bind(), errorCode('invalid_response'));
});

test('pagination uses exact bound plan and private cursor despite exported result mutation; each cursor is consumed once', async () => {
  const { transport, calls } = fixture((query, number) => entityResult(query, number === 1 ? { hasMore: true, nextCursor: 'signed-cursor' } : {}));
  const bound = await DataSet.from(transport, source).select('金额').where({ op: 'gte', field: '金额', value: '1.000001' }).limit(1).bind();
  const first = await bound.execute();
  const exported = first.result; exported.nextCursor = 'attacker'; exported.hasMore = false; exported.data[0].values['amount-id'] = 'MUTATED'; exported.fields[0].id = 'MUTATED';
  assert.equal(first.result.nextCursor, 'signed-cursor');
  assert.equal(first.result.data[0].values['amount-id'], '9007199254740993.000001');
  const second = await first.nextPage();
  assert.deepEqual(calls.query[1].query, { ...bound.toQuery(), page: { limit: 1, cursor: 'signed-cursor' } });
  assert.equal(calls.bind.length, 1); assert.equal(await second.nextPage(), undefined);
  await assert.rejects(first.nextPage(), errorCode('duplicate_cursor'));
});

test('repeated cursors and malformed pagination/modes never trigger hidden requests', async () => {
  const { transport, calls } = fixture((query) => entityResult(query, { hasMore: true, nextCursor: 'same-cursor' }));
  const bound = await DataSet.from(transport, source).select('金额').bind();
  const first = await bound.execute(); await assert.rejects(first.nextPage(), errorCode('duplicate_cursor'));
  assert.equal(calls.query.length, 2);
  for (const extra of [{ hasMore: true }, { hasMore: false, nextCursor: 'unexpected' }, { hasMore: 'true' }, { rowMode: true }, { rows: [] }, { aggregateMode: true }, { fields: [fields[0]] }]) {
    transport.query = async (query) => entityResult(query, extra);
    await assert.rejects(bound.execute(), DataSetError);
  }
});

test('aggregate builder detaches arrays, supports no-group empty response, and switches back to entity projection', async () => {
  const input = { groupBy: ['编码'], measures: [{ id: 'm0', op: 'sum', field: '金额', name: '金额合计' }] };
  const { transport, calls } = fixture((query) => ({ data: [], fields: outputFields(query), aggregateMode: true, aggregateRows: [{ values: { 'code-id': 'A', m0: '9007199254740993.000001' } }], hasMore: false }));
  const dataset = DataSet.from(transport, source).select('金额').aggregate(input);
  input.groupBy.push('MUTATED'); input.measures[0].name = 'MUTATED';
  assert.deepEqual(dataset.toQuery().aggregate, { groupBy: ['编码'], measures: [{ id: 'm0', op: 'sum', field: '金额', name: '金额合计' }] });
  assert.deepEqual(dataset.toQuery().select, []);
  assert.equal(dataset.select('金额').toQuery().aggregate, undefined);
  const bound = await dataset.bind(); const page = await bound.execute();
  assert.equal(page.result.aggregateRows[0].values.m0, '9007199254740993.000001');
  assert.equal(calls.query[0].query.aggregate.measures[0].field, 'amount-id');
  const total = await DataSet.from(transport, source).aggregate({ measures: [{ id: 'm0', op: 'count' }] }).bind();
  transport.query = async (query) => ({ data: [], fields: outputFields(query), aggregateMode: true, hasMore: false });
  assert.deepEqual((await total.execute()).result.aggregateRows, []);
  assert.throws(() => dataset.aggregate({ measures: [{ id: 'invalid-id', op: 'count' }] }), DataSetError);
  assert.throws(() => dataset.aggregate({ groupBy: ['a', 'b', 'c', 'd'], measures: [{ id: 'm0', op: 'count' }] }), DataSetError);
});

test('result scalar precision and timestamp microseconds survive unchanged; numbers are rejected', async () => {
  const { transport } = fixture();
  const bound = await DataSet.from(transport, source).select('金额', '数量', '时间').bind();
  const page = await bound.execute();
  assert.deepEqual(page.result.data[0].values, { 'amount-id': '9007199254740993.000001', 'count-id': '9223372036854775807', 'time-id': '2026-11-08T15:01:02.123456+08:00' });
  for (const value of [1, NaN, 1n, ['nested'], {}]) {
    transport.query = async (query) => { const result = entityResult(query); result.data[0].values['amount-id'] = value; return result; };
    await assert.rejects(bound.execute(), errorCode('invalid_response'));
  }
});

test('entity and aggregate values must exactly match stable output IDs, never names, missing or extra fields', async () => {
  const { transport } = fixture();
  const entity = await DataSet.from(transport, source).select('金额').bind();
  for (const values of [{}, { 'amount-id': '1', unexpected: 'private' }, { 金额: '1' }]) {
    transport.query = async (query) => entityResult(query, { data: [{ id: 'id', schemaId: 'orders-id', etag: 'etag', values }] });
    await assert.rejects(entity.execute(), errorCode('invalid_response'));
  }
  const grouped = await DataSet.from(transport, source).aggregate({ measures: [{ id: 'm0', op: 'count' }] }).bind();
  for (const values of [{}, { m0: '1', unexpected: 'private' }, { 数量: '1' }]) {
    transport.query = async (query) => ({ data: [], fields: outputFields(query), aggregateMode: true, aggregateRows: [{ values }], hasMore: false });
    await assert.rejects(grouped.execute(), errorCode('invalid_response'));
  }
});

test('abort before and after a transport that ignores the signal prevents binding/result acceptance', async () => {
  const { transport, calls } = fixture();
  const unbound = DataSet.from(transport, source).select('金额');
  const canceled = AbortSignal.abort();
  await assert.rejects(unbound.bind(canceled), { name: 'AbortError' }); assert.equal(calls.bind.length, 0);
  const bound = await unbound.bind();
  await assert.rejects(bound.execute(canceled), { name: 'AbortError' }); assert.equal(calls.query.length, 0);
  let finish;
  transport.query = async (query, signal) => new Promise((resolve) => { finish = () => resolve(entityResult(query)); });
  const controller = new AbortController(); const pending = bound.execute(controller.signal);
  controller.abort(); finish(); await assert.rejects(pending, { name: 'AbortError' });
  let finishBind;
  transport.bind = async (query) => new Promise((resolve) => { finishBind = () => { const normalized = canonical(query); resolve({ query: normalized, fields: outputFields(normalized) }); }; });
  const bindController = new AbortController(); const pendingBind = unbound.bind(bindController.signal); bindController.abort(); finishBind();
  await assert.rejects(pendingBind, { name: 'AbortError' });
});

test('explicit page budgets stop without an extra request; early break, abort and invalid budgets are bounded', async () => {
  const { transport, calls } = fixture((query, number) => entityResult(query, { hasMore: true, nextCursor: `cursor-${number}` }));
  const bound = await DataSet.from(transport, source).select('金额').bind();
  const collected = [];
  for await (const page of bound.pages({ maxPages: 3 })) collected.push(page.result);
  assert.equal(collected.length, 3); assert.equal(calls.query.length, 3);
  for await (const _page of bound.pages({ maxPages: 100 })) break;
  assert.equal(calls.query.length, 4);
  const mutableBudget = { maxPages: 1 };
  const bounded = bound.pages(mutableBudget);
  await bounded.next(); mutableBudget.maxPages = Infinity;
  assert.equal((await bounded.next()).done, true);
  assert.equal(calls.query.length, 5);
  for (const options of [undefined, {}, { maxPages: 0 }, { maxPages: 101 }, { maxPages: 1.5 }]) await assert.rejects(bound.pages(options).next(), errorCode('invalid_budget'));
  assert.equal(calls.query.length, 5);
  const controller = new AbortController(); const iterator = bound.pages({ maxPages: 2, signal: controller.signal });
  await iterator.next(); controller.abort(); await assert.rejects(iterator.next(), { name: 'AbortError' });
  assert.equal(calls.query.length, 6);
});

test('custom transport exceptions are sanitized, DataSetError and cancellation retain identity', async () => {
  const { transport } = fixture(); const dataset = DataSet.from(transport, source).select('金额');
  transport.bind = async () => { throw new Error('PRIVATE_TOKEN'); };
  await assert.rejects(dataset.bind(), (error) => error instanceof DataSetError && error.code === 'transport_error' && !error.message.includes('PRIVATE_TOKEN'));
  const forbidden = new DataSetError('当前无权查询', 'forbidden', 403);
  transport.bind = async () => { throw forbidden; };
  await assert.rejects(dataset.bind(), (error) => error === forbidden);
});
