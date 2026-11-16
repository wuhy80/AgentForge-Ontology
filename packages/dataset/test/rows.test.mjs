import assert from 'node:assert/strict';
import { test } from 'node:test';
import { DataSet, DataSetError } from '../dist/index.js';

const model = '1b74bd75-135a-4f2f-8f10-7ac08918ef01';
const link = '1b74bd75-135a-4f2f-8f10-7ac08918ef02';
const field = '1b74bd75-135a-4f2f-8f10-7ac08918ef03';
const scope = { nodes: [{ id: 'n0', schemaId: model, alias: '父' }, { id: 'n1', schemaId: model, alias: '子' }], joins: [{ from: 'n0', to: 'n1', linkId: link, direction: 'out' }] };
const property = { id: `n1.${field}`, name: '子.金额', dataType: 'decimal', precision: 24, scale: 6 };
const makeResult = (hasMore = false) => ({ rowMode: true, data: [], fields: [property], rows: [{ id: 'tuple-1', values: { [property.id]: '9007199254740993.000001' }, entities: [{ nodeId: 'n0', schemaId: model, instanceId: '1' }, { nodeId: 'n1', schemaId: model, instanceId: '9223372036854775807' }] }], hasMore, ...(hasMore ? { nextCursor: 'row-cursor' } : {}) });
function fixture() {
  const calls = [];
  const transport = { async bind(query) { calls.push(['bind', structuredClone(query)]); return { query, fields: [property] }; }, async query(query) { calls.push(['query', structuredClone(query)]); return makeResult(!query.page.cursor); } };
  const dataset = () => DataSet.from(transport, { schemaId: model }).rows(scope).select(property.id).where({ op: 'gte', field: property.id, value: '0.000001' }).limit(1);
  return { transport, calls, dataset };
}

test('INNER row construction detaches scopes, retains self/branch occurrences and performs no IO', () => {
  const { dataset, calls } = fixture(); const query = dataset();
  const exported = query.toQuery(); exported.rows.nodes[1].alias = 'changed'; exported.rows.joins[0].linkId = model;
  assert.equal(query.toQuery().rows.nodes[1].alias, '子'); assert.equal(query.toQuery().rows.joins[0].linkId, link);
  assert.equal(calls.length, 0);
  assert.equal(DataSet.fromSpec(fixture().transport, query.toQuery()).isBound, false);
  const branch = structuredClone(scope);
  branch.nodes.push({ id: 'n2', schemaId: model, alias: '第二子' }); branch.joins.push({ from: 'n0', to: 'n2', linkId: link, direction: 'in' });
  assert.equal(query.rows(branch).toQuery().rows.nodes.length, 3);
});

test('INNER row execution and pagination preserve tuples, exact values, origins and private cursor', async () => {
  const { dataset, calls } = fixture(); const bound = await dataset().bind();
  const first = await bound.execute();
  assert.equal(first.result.data.length, 0); assert.equal(first.result.rowMode, true);
  assert.equal(first.result.rows[0].values[property.id], '9007199254740993.000001');
  assert.equal(first.result.rows[0].entities[1].instanceId, '9223372036854775807');
  const exported = first.result; exported.rows[0].entities[1].schemaId = 'changed'; exported.rows[0].values[property.id] = 'changed'; exported.nextCursor = 'changed';
  assert.equal(first.result.rows[0].entities[1].schemaId, model);
  const next = await first.nextPage(); assert.equal(next.result.rows[0].values[property.id], '9007199254740993.000001');
  assert.deepEqual(calls[2][1], { ...bound.toQuery(), page: { limit: 1, cursor: 'row-cursor' } });
  await assert.rejects(first.nextPage(), (error) => error.code === 'duplicate_cursor');
  assert.equal(calls.filter(([kind]) => kind === 'bind').length, 1);
});

test('row results retain distinct tuples with equal projections', async () => {
  const { dataset, transport } = fixture();
  transport.query = async () => { const result = makeResult(); const other = structuredClone(result.rows[0]); other.id = 'tuple-2'; other.entities[1].instanceId = '2'; result.rows.push(other); return result; };
  const bound = await dataset().limit(2).bind(); const page = await bound.execute();
  assert.equal(page.result.rows.length, 2); assert.notEqual(page.result.rows[0].id, page.result.rows[1].id);
});

test('rows reject unsupported graphs, aggregates, names, shapes and injected binding scopes', async () => {
  const { dataset, transport } = fixture();
  for (const edit of [
    (s) => { s.joins[0].direction = 'left'; }, (s) => { s.joins[0].from = 'n1'; },
    (s) => { s.nodes[1].id = 'n0'; }, (s) => { s.nodes[1].alias = '父'; },
    (s) => { s.nodes[1].schemaId = '中文名'; }, (s) => { s.joins[0].linkId = '名称'; },
    (s) => { s.joins[0].on = 'condition'; },
  ]) { const s = structuredClone(scope); edit(s); assert.throws(() => dataset().rows(s), DataSetError); }
  assert.throws(() => dataset().aggregate({ measures: [{ id: 'm0', op: 'count' }] }), DataSetError);
  for (const name of ['子.金额', field, `n4.${field}`]) assert.throws(() => dataset().select(name), DataSetError);
  transport.bind = async (query) => { query.rows.nodes[1].alias = 'replacement'; return { query, fields: [property] }; };
  await assert.rejects(dataset().bind(), (error) => error.code === 'invalid_binding');
});

test('row result rejects wrong mode, leaked object shape, missing/changed origins and mismatched values', async () => {
  for (const edit of [
    (r) => { r.rowMode = false; }, (r) => { delete r.rows; },
    (r) => { r.data = [{ id: 'object', schemaId: model, etag: 'etag', values: { [property.id]: '0' } }]; },
    (r) => { r.aggregateRows = []; }, (r) => { r.rows[0].etag = 'object-etag'; },
    (r) => { r.rows[0].entities.pop(); }, (r) => { r.rows[0].entities[1].nodeId = 'n0'; },
    (r) => { r.rows[0].entities[1].schemaId = link; }, (r) => { r.rows[0].entities[1].instanceId = ''; },
    (r) => { r.rows[0].values = {}; }, (r) => { r.rows[0].values[property.id] = 9007199254740993; },
  ]) {
    const { dataset, transport } = fixture(); const bound = await dataset().bind();
    transport.query = async () => { const response = makeResult(); edit(response); return response; };
    await assert.rejects(bound.execute(), DataSetError);
  }
});
