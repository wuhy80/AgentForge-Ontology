import { DataSet, createBrowserTransport, type QuerySpec, type QueryTransport } from '../dist/index.js';

declare const transport: QueryTransport;
const query = DataSet.from(transport, { category: '业务', schema: '工单' })
  .select('代码', '金额')
  .where({ op: 'gte', field: '金额', value: '9007199254740993.001' })
  .orderBy('代码')
  .limit(25);

const plan: QuerySpec = query.toQuery();
void plan;
void query.bind().then((bound) => bound.execute());
void createBrowserTransport({ csrfToken: () => 'memory-token' });

// @ts-expect-error Values must retain exact decimal/int64 strings.
query.where({ op: 'eq', field: '金额', value: 9007199254740993 });
// @ts-expect-error A relationship query must declare nodes and INNER Link joins.
DataSet.fromSpec(transport, { from: { schemaId: 'id' }, rows: {}, select: [], page: { limit: 10 } });
const rows = DataSet.from(transport, { schemaId: 'root-id' }).rows({
  nodes: [{ id: 'n0', schemaId: 'root-id', alias: '父' }, { id: 'n1', schemaId: 'root-id', alias: '子' }],
  joins: [{ from: 'n0', to: 'n1', linkId: 'link-id', direction: 'out' }],
}).select('n1.field-id');
void rows.bind().then(async (bound) => {
  const page = await bound.execute();
  const entity: string | undefined = page.result.rows?.[0]?.entities[0]?.instanceId;
  void entity;
  // @ts-expect-error A query row has multiple origins, no object ETag.
  void page.result.rows?.[0]?.etag;
});
// @ts-expect-error Credentials must be read from the current session.
createBrowserTransport({ basePath: '/api/data' });
