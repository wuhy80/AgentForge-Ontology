import { DataSetError } from './errors.js';
import type { AggregateMeasure, AggregateOperation, AggregateQuery, DataSetPage, Expression, Instance, Property, QueryResult, QuerySpec, QueryTransport, Source, Value, Values } from './types.js';

type RecordValue = Record<string, unknown>;
const own = (value: object, key: string) => Object.hasOwn(value, key);
function fail(message: string, code = 'invalid_query'): never { throw new DataSetError(message, code); }

function record(value: unknown, code = 'invalid_query'): RecordValue {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) fail('需要普通JSON对象', code);
  const prototype: unknown = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) fail('查询不支持带自定义原型的对象', code);
  if (Object.getOwnPropertySymbols(value).length || Object.values(Object.getOwnPropertyDescriptors(value)).some((property) => !('value' in property) || !property.enumerable)) fail('查询不支持访问器或隐藏属性', code);
  return value as RecordValue;
}
function keys(value: RecordValue, allowed: readonly string[], code = 'unsupported_query') {
  if (Object.keys(value).some((key) => !allowed.includes(key))) fail('查询包含当前SDK不支持的属性', code);
}
function text(value: unknown, label: string, code = 'invalid_query'): string {
  if (typeof value !== 'string' || !value.trim()) fail(`${label}必须为非空字符串`, code);
  return value;
}
function array(value: unknown, code = 'invalid_query'): unknown[] {
  if (!Array.isArray(value) || Object.getPrototypeOf(value) !== Array.prototype || Object.getOwnPropertySymbols(value).length || Object.keys(value).length !== value.length) fail('需要不含空项的JSON数组', code);
  for (let index = 0; index < value.length; index++) {
    const descriptor = Object.getOwnPropertyDescriptor(value, String(index));
    if (!descriptor || !('value' in descriptor) || !descriptor.enumerable) fail('数组不能包含空项或访问器', code);
  }
  return value;
}
function scalar(value: unknown, code = 'invalid_query'): Value {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
  return fail('值仅支持字符串、布尔值或NULL；精确数值请使用字符串', code);
}
function expression(input: unknown, budget = { nodes: 0 }, depth = 0): Expression {
  if (++budget.nodes > 100 || depth > 10) fail('查询条件超过节点或嵌套预算', 'query_budget');
  const value = record(input);
  keys(value, ['op', 'field', 'value', 'args']);
  const op = text(value.op, '条件操作符');
  if (['and', 'or', 'not'].includes(op)) {
    if (own(value, 'field') || own(value, 'value')) fail('逻辑条件不能携带字段或值');
    const children = array(value.args);
    if (!children.length || op === 'not' && children.length !== 1) fail('逻辑条件需要有效子条件');
    return { op, args: children.map((item) => expression(item, budget, depth + 1)) };
  }
  if (!['eq', 'ne', 'gt', 'gte', 'lt', 'lte', 'startsWith', 'in', 'notIn', 'isNull', 'isNotNull'].includes(op)) fail('条件操作符尚不支持', 'unsupported_query');
  const field = text(value.field, '条件字段');
  if (own(value, 'args')) fail('比较条件不能携带子条件');
  if (op === 'isNull' || op === 'isNotNull') {
    if (own(value, 'value')) fail('空值操作符不能携带比较值');
    return { op, field };
  }
  if (!own(value, 'value')) fail('比较条件需要值');
  if (op === 'in' || op === 'notIn') {
    const values = array(value.value);
    if (!values.length || values.length > 100) fail('集合须包含1..100个标量值');
    return { op, field, value: values.map((item) => scalar(item)) };
  }
  return { op, field, value: scalar(value.value) };
}
function source(input: unknown): Source {
  const value = record(input);
  keys(value, ['schemaId', 'category', 'schema']);
  if (value.schemaId !== undefined) {
    if (value.category !== undefined || value.schema !== undefined) fail('查询来源不能混用稳定ID与名称');
    return { schemaId: text(value.schemaId, 'schemaId') };
  }
  return { category: text(value.category, '业务分类'), schema: text(value.schema, '业务模型') };
}
function aggregate(input: unknown): AggregateQuery {
  const value = record(input);
  keys(value, ['groupBy', 'measures']);
  const groupBy = value.groupBy === undefined ? undefined : array(value.groupBy).map((id) => text(id, '分组字段'));
  if (groupBy && (groupBy.length > 3 || new Set(groupBy).size !== groupBy.length)) fail('分组最多包含3个不同字段');
  const entries = array(value.measures);
  if (!entries.length || entries.length > 20) fail('汇总指标须为1..20项');
  const ids = new Set(groupBy);
  const measures: AggregateMeasure[] = entries.map((entry) => {
    const item = record(entry);
    keys(item, ['id', 'op', 'field', 'name']);
    const id = text(item.id, '指标标识'), op = text(item.op, '汇总方式');
    if (!/^m[A-Za-z0-9_]{0,63}$/.test(id) || ids.has(id)) fail('指标标识无效或重复');
    ids.add(id);
    if (!['count', 'countDistinct', 'sum', 'avg', 'min', 'max'].includes(op)) fail('汇总方式不支持', 'unsupported_query');
    const field = item.field === undefined ? undefined : text(item.field, '汇总字段');
    if (op !== 'count' && !field) fail('此汇总方式需要字段');
    if (item.name !== undefined && typeof item.name !== 'string') fail('汇总名称必须为字符串');
    return { id, op: op as AggregateOperation, ...(field ? { field } : {}), ...(item.name !== undefined ? { name: item.name as string } : {}) };
  });
  return { ...(groupBy ? { groupBy } : {}), measures };
}
function plan(input: unknown, allowCursor = false): QuerySpec {
  const value = record(input);
  keys(value, ['from', 'where', 'select', 'orderBy', 'aggregate', 'page']);
  const from = source(value.from);
  const select = array(value.select).map((field) => text(field, '返回字段'));
  if (select.length > 100 || new Set(select).size !== select.length) fail('返回字段最多100项且不能重复');
  const page = record(value.page);
  keys(page, ['limit', 'cursor']);
  if (typeof page.limit !== 'number' || !Number.isInteger(page.limit) || page.limit < 1 || page.limit > 100) fail('每页条数须为1..100');
  if (page.cursor !== undefined && !allowCursor) fail('构造或绑定查询不能携带游标');
  const cursor = page.cursor === undefined ? undefined : text(page.cursor, '分页游标');
  const grouped = value.aggregate === undefined ? undefined : aggregate(value.aggregate);
  if (grouped && select.length) fail('汇总查询select必须为空');
  const where = value.where === undefined ? undefined : expression(value.where);
  const orderBy = value.orderBy === undefined ? undefined : array(value.orderBy).map((input) => {
    const order = record(input);
    keys(order, ['field', 'direction']);
    if (order.direction !== 'asc' && order.direction !== 'desc') fail('排序方向必须为asc或desc');
    return { field: text(order.field, '排序字段'), direction: order.direction as 'asc' | 'desc' };
  });
  if (orderBy && orderBy.length > 1) fail('当前最多支持一个排序字段');
  return { from, select, ...(where ? { where } : {}), ...(orderBy?.length ? { orderBy } : {}), ...(grouped ? { aggregate: grouped } : {}), page: { limit: page.limit, ...(cursor ? { cursor } : {}) } };
}
function properties(input: unknown): Property[] {
  const fields = array(input, 'invalid_response');
  if (fields.length > 100) fail('字段目录超过预算', 'invalid_response');
  const ids = new Set<string>();
  return fields.map((input) => {
    const value = record(input, 'invalid_response');
    const id = text(value.id, '字段标识', 'invalid_response');
    if (ids.has(id)) fail('字段目录含重复标识', 'invalid_response');
    ids.add(id);
    const output: Property = { id, name: text(value.name, '字段名称', 'invalid_response'), dataType: text(value.dataType, '字段类型', 'invalid_response') };
    const extra: RecordValue = {};
    for (const key of ['nullable', 'required', 'readOnly']) if (value[key] !== undefined) { if (typeof value[key] !== 'boolean') fail('字段约束无效', 'invalid_response'); extra[key] = value[key]; }
    for (const key of ['precision', 'scale']) if (value[key] !== undefined) { if (typeof value[key] !== 'number' || !Number.isInteger(value[key]) || (value[key] as number) < 0 || (value[key] as number) > 65) fail('字段精度无效', 'invalid_response'); extra[key] = value[key]; }
    for (const key of ['enumId', 'enumEtag']) if (value[key] !== undefined) extra[key] = text(value[key], '枚举引用', 'invalid_response');
    if (value.enumValues !== undefined) extra.enumValues = array(value.enumValues, 'invalid_response').map((input) => {
      const entry = record(input, 'invalid_response');
      if (entry.deprecated !== undefined && typeof entry.deprecated !== 'boolean') fail('枚举选项状态无效', 'invalid_response');
      return { code: text(entry.code, '枚举编码', 'invalid_response'), label: text(entry.label, '枚举标签', 'invalid_response'), ...(entry.deprecated !== undefined ? { deprecated: entry.deprecated } : {}) };
    });
    return { ...output, ...extra };
  });
}
function fieldIDs(query: QuerySpec): readonly string[] {
  return query.aggregate ? [...query.aggregate.groupBy ?? [], ...query.aggregate.measures.map((measure) => measure.id)] : query.select;
}
function sameFields(fields: readonly Property[], query: QuerySpec) {
  const ids = fieldIDs(query);
  if (fields.length !== ids.length || fields.some((field, index) => field.id !== ids[index])) fail('响应字段与绑定计划不一致', 'invalid_response');
}
function result(input: unknown, query: QuerySpec): QueryResult {
  const value = record(input, 'invalid_response');
  if (value.rowMode !== undefined && value.rowMode !== false || value.rows !== undefined) fail('SDK暂不支持关联行结果', 'invalid_response');
  if (value.aggregateMode !== undefined && typeof value.aggregateMode !== 'boolean' || !!value.aggregateMode !== !!query.aggregate) fail('查询结果模式与请求不一致', 'invalid_response');
  if (typeof value.hasMore !== 'boolean') fail('查询结果缺少分页状态', 'invalid_response');
  const fields = properties(value.fields);
  sameFields(fields, query);
  const outputIDs = new Set(fieldIDs(query));
  const values = (input: unknown): Values => {
    const row = record(input, 'invalid_response');
    if (Object.keys(row).length !== outputIDs.size || Object.keys(row).some((key) => !outputIDs.has(key))) fail('查询结果值必须完整匹配已绑定输出字段', 'invalid_response');
    return Object.fromEntries(Object.entries(row).map(([key, value]) => [key, scalar(value, 'invalid_response')]));
  };
  const data: Instance[] = array(value.data, 'invalid_response').map((input) => {
    const entity = record(input, 'invalid_response');
    if (entity.schemaId !== query.from.schemaId) fail('查询实例来源与绑定模型不一致', 'invalid_response');
    return { id: text(entity.id, '实例标识', 'invalid_response'), schemaId: entity.schemaId as string, etag: text(entity.etag, '实例凭证', 'invalid_response'), values: values(entity.values) };
  });
  if (query.aggregate && data.length || !query.aggregate && value.aggregateRows !== undefined) fail('查询返回了不一致的结果形态', 'invalid_response');
  const aggregateRows = query.aggregate ? array(value.aggregateRows === undefined ? [] : value.aggregateRows, 'invalid_response').map((input) => ({ values: values(record(input, 'invalid_response').values) })) : undefined;
  if ((aggregateRows?.length ?? data.length) > query.page.limit) fail('查询结果超过每页预算', 'invalid_response');
  const cursor = value.nextCursor === undefined ? undefined : text(value.nextCursor, '分页游标', 'invalid_response');
  if (value.hasMore && !cursor || !value.hasMore && cursor) fail('分页状态与游标不一致', 'invalid_response');
  return { data, fields, ...(query.aggregate ? { aggregateMode: true, aggregateRows } : {}), hasMore: value.hasMore, ...(cursor ? { nextCursor: cursor } : {}) };
}
function transport(input: QueryTransport): QueryTransport {
  if (!input || typeof input.bind !== 'function' || typeof input.query !== 'function') fail('需要有效的查询传输');
  return input;
}
async function invoke<T>(call: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  signal?.throwIfAborted();
  try { const response = await call(); signal?.throwIfAborted(); return response; }
  catch (cause) { signal?.throwIfAborted(); if (cause instanceof DataSetError || cause instanceof Error && ['AbortError', 'TimeoutError'].includes(cause.name)) throw cause; throw new DataSetError('查询传输未能完成请求', 'transport_error'); }
}

/** Immutable query construction. Only bind and execution methods perform I/O. */
export class DataSet {
  readonly #transport: QueryTransport;
  readonly #plan: QuerySpec;
  readonly #fields?: readonly Property[];
  private constructor(transport: QueryTransport, query: QuerySpec, fields?: readonly Property[]) {
    this.#transport = transport;
    this.#plan = plan(query);
    this.#fields = fields === undefined ? undefined : properties(fields);
    Object.freeze(this);
  }
  static from(queryTransport: QueryTransport, querySource: Source): DataSet {
    return new DataSet(transport(queryTransport), { from: source(querySource), select: [], page: { limit: 25 } });
  }
  /** Imported plans are always unbound and never accept a saved cursor. */
  static fromSpec(queryTransport: QueryTransport, query: QuerySpec): DataSet { return new DataSet(transport(queryTransport), plan(query)); }
  get isBound(): boolean { return this.#fields !== undefined; }
  get fields(): readonly Property[] { return this.#fields ? properties(this.#fields) : []; }
  toQuery(): QuerySpec { return plan(this.#plan); }
  select(...fields: string[]): DataSet {
    const { aggregate: _aggregate, ...base } = this.#plan;
    return new DataSet(this.#transport, { ...base, select: fields });
  }
  where(predicate: Expression): DataSet {
    const detached = expression(predicate);
    const where: Expression = this.#plan.where ? { op: 'and', args: [this.#plan.where, detached] } : detached;
    return new DataSet(this.#transport, { ...this.#plan, where });
  }
  orderBy(field: string, direction: 'asc' | 'desc' = 'asc'): DataSet { return new DataSet(this.#transport, { ...this.#plan, orderBy: [{ field, direction }] }); }
  limit(limit: number): DataSet { return new DataSet(this.#transport, { ...this.#plan, page: { limit } }, this.#fields); }
  aggregate(grouped: AggregateQuery): DataSet { return new DataSet(this.#transport, { ...this.#plan, select: [], aggregate: grouped }); }
  async bind(signal?: AbortSignal): Promise<DataSet> {
    const request = this.toQuery();
    const binding: unknown = await invoke(() => this.#transport.bind(request, signal), signal);
    const response = record(binding, 'invalid_binding');
    const query = plan(response.query);
    if (!query.from.schemaId || this.#plan.from.schemaId && this.#plan.from.schemaId !== query.from.schemaId || !!query.aggregate !== !!this.#plan.aggregate || query.page.limit !== this.#plan.page.limit) fail('绑定响应改变了查询来源、模式或预算', 'invalid_binding');
    const fields = properties(response.fields);
    sameFields(fields, query);
    return new DataSet(this.#transport, query, fields);
  }
  async execute(signal?: AbortSignal): Promise<DataSetPage> {
    if (!this.isBound) fail('请先bind查询后再execute', 'not_bound');
    const response = await invoke(() => this.#transport.query(this.toQuery(), signal), signal);
    return this.makePage(response, new Set());
  }
  private makePage(response: QueryResult, seen: ReadonlySet<string>): DataSetPage {
    const snapshot = result(response, this.#plan);
    if (snapshot.nextCursor && seen.has(snapshot.nextCursor)) fail('服务端返回了重复分页游标', 'duplicate_cursor');
    let consumed = false;
    const queryPlan = this.#plan;
    const nextPage = async (signal?: AbortSignal): Promise<DataSetPage | undefined> => {
      signal?.throwIfAborted();
      if (!snapshot.hasMore) return undefined;
      if (consumed) fail('同一页不能重复使用分页游标', 'duplicate_cursor');
      consumed = true;
      const cursor = snapshot.nextCursor!;
      const query = plan({ ...queryPlan, page: { limit: queryPlan.page.limit, cursor } }, true);
      const next = await invoke(() => this.#transport.query(query, signal), signal);
      return this.makePage(next, new Set(seen).add(cursor));
    };
    return Object.freeze({ get result() { return result(snapshot, queryPlan); }, nextPage });
  }
  async *pages(options: { maxPages: number; signal?: AbortSignal }): AsyncGenerator<DataSetPage, void, undefined> {
    if (!options || !Number.isInteger(options.maxPages) || options.maxPages < 1 || options.maxPages > 100) fail('pages必须显式指定1..100的maxPages', 'invalid_budget');
    const { maxPages, signal } = options;
    let page: DataSetPage | undefined = await this.execute(signal);
    for (let count = 0; page && count < maxPages; count++) {
      signal?.throwIfAborted();
      yield page;
      if (count + 1 >= maxPages) return;
      page = await page.nextPage(signal);
    }
  }
}
