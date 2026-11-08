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
// @ts-expect-error The first SDK does not expose arbitrary relationship queries.
DataSet.fromSpec(transport, { from: { schemaId: 'id' }, rows: {}, select: [], page: { limit: 10 } });
// @ts-expect-error Credentials must be read from the current session.
createBrowserTransport({ basePath: '/api/data' });
