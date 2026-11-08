import type { Expression, JsonValue, Property, QueryContextModel, QuerySpec, RowQuery, SQLParameter } from '../../lib/types';
import { availableOperators, compileQuery, newGroup, parseFieldValue } from './query';
import type { FilterGroup, FilterNode, QueryEditor } from './query';

export const quoteIdentifier = (value: string) => `"${value.replaceAll('"', '""')}"`;
export type RowQueryContext = {
  models: QueryContextModel[];
  links: { id: string; category: string; name: string; sourceSchemaId: string; targetSchemaId: string }[];
};

export function rowQueryModel(rows: RowQuery, context: RowQueryContext, allowIncompleteAliases = false): QueryContextModel {
  if (rows.nodes.length < 2 || rows.nodes.length > 5 || rows.joins.length !== rows.nodes.length - 1) throw new Error('关联查询需要 1 至 4 条 Link');
  const aliases = new Set<string>();
  const fields = rows.nodes.flatMap((node, index) => {
    const alias = node.alias.normalize('NFC').toLowerCase();
    if (node.id !== `n${index}`) throw new Error('关联节点标识无效');
    if (!allowIncompleteAliases && (!node.alias.trim() || Array.from(node.alias).length > 128 || /[\p{Cc}]/u.test(node.alias) || aliases.has(alias))) throw new Error('模型角色需要唯一名称，且不能包含控制字符');
    aliases.add(alias);
    const model = context.models.find((item) => item.schemaId === node.schemaId);
    if (!model) throw new Error('关联模型不可查询，请重新读取');
    if (index) {
      const join = rows.joins[index - 1];
      const from = rows.nodes.slice(0, index).find((item) => item.id === join.from);
      const link = context.links.find((item) => item.id === join.linkId);
      if (!from || join.to !== node.id || !link || !['out', 'in'].includes(join.direction)
        || (join.direction === 'out' ? link.sourceSchemaId !== from.schemaId || link.targetSchemaId !== node.schemaId : link.targetSchemaId !== from.schemaId || link.sourceSchemaId !== node.schemaId)) throw new Error('关联路径或方向已失效，请重新选择');
    }
    // The row executor supports primary-table attributes only. Context marks
    // those attributes filterable; external projection-only attributes stay out.
    return model.fields.filter((field) => field.filterable).map((field) => ({ ...field, id: `${node.id}.${field.id}`, name: `${node.alias}.${field.name}` }));
  });
  const root = context.models.find((model) => model.schemaId === rows.nodes[0].schemaId)!;
  return { ...root, fields };
}

export function compileRowQuery(rows: RowQuery, context: RowQueryContext, editor: QueryEditor): QuerySpec {
  const model = rowQueryModel(rows, context);
  const query = compileQuery(model.schemaId, model.fields, editor);
  if (editor.select.length > 100) throw new Error('返回字段最多 100 项');
  if (query.orderBy?.some((order) => !model.fields.find((field) => field.id === order.field)?.sortable)) throw new Error('排序字段当前不支持排序');
  return { ...query, rows };
}
export type ParameterDraft = { mode: 'missing' | 'value' | 'null'; text: string; signature: string };
export type ParameterDrafts = Record<string, ParameterDraft>;
export const parameterSignature = (parameter: SQLParameter) => JSON.stringify([parameter.dataType, parameter.nullable, parameter.precision ?? 0, parameter.scale ?? 0]);

export function parameterValues(parameters: SQLParameter[], drafts: ParameterDrafts): Record<string, JsonValue> {
  const result: Record<string, JsonValue> = Object.create(null);
  for (const parameter of parameters) {
    const draft = drafts[parameter.name];
    if (!draft || draft.mode === 'missing' || draft.signature !== parameterSignature(parameter)) continue;
    if (draft.mode === 'null') {
      if (!parameter.nullable) throw new Error(`参数 ${parameter.name} 不允许 NULL`);
      result[parameter.name] = null;
    } else {
      result[parameter.name] = parseFieldValue({ ...parameter, id: parameter.name }, draft.text);
    }
  }
  return result;
}

export function modelTemplate(model: QueryContextModel, kind: 'read' | 'filter' | 'parameter' = 'read'): string {
  const fields = model.fields.filter((field) => field.dataType !== 'longtext').slice(0, 4);
  const selected = fields.length ? fields : model.fields.slice(0, 1);
  if (!selected.length) throw new Error('此模型没有可读取的属性');
  const alias = quoteIdentifier(model.name);
  const reference = (field: Property) => `${alias}.${quoteIdentifier(field.name)}`;
  let predicate = '';
  if (kind !== 'read') {
    const field = model.fields.find((item) => item.filterable && ['string', 'text'].includes(item.dataType))
      ?? model.fields.find((item) => item.filterable && item.dataType === 'boolean')
      ?? model.fields.find((item) => item.filterable);
    if (!field) throw new Error('此模型没有可筛选的主表属性');
    if (kind === 'parameter') {
      const name = `参数${Array.from(field.name.replace(/[^\p{L}\p{N}_]/gu, '')).slice(0, 100).join('')}${['string', 'text', 'longtext'].includes(field.dataType) ? '前缀' : '值'}`;
      predicate = ['string', 'text', 'longtext'].includes(field.dataType)
        ? `\nWHERE STARTS_WITH(${reference(field)}, :${name})`
        : `\nWHERE ${reference(field)} = :${name}`;
    } else if (['string', 'text', 'longtext'].includes(field.dataType)) predicate = `\nWHERE STARTS_WITH(${reference(field)}, '示例')`;
    else if (field.dataType === 'boolean') predicate = `\nWHERE ${reference(field)} = TRUE`;
    else if (field.dataType === 'int64' || field.dataType === 'decimal') predicate = `\nWHERE ${reference(field)} >= 0`;
    else predicate = `\nWHERE ${reference(field)} IS NOT NULL`;
  }
  return `SELECT ${selected.map(reference).join(', ')}\nFROM ${quoteIdentifier(model.category)}.${quoteIdentifier(model.name)} AS ${alias}${predicate}\nLIMIT 25;`;
}

function literal(value: JsonValue, field: Property): string {
  if (value === null) throw new Error('比较值 NULL 必须使用 IS NULL 或 IS NOT NULL');
  if (field.dataType === 'boolean' && typeof value === 'boolean') return value ? 'TRUE' : 'FALSE';
  if (typeof value !== 'string') throw new Error('查询值必须遵循字段的精确类型协议');
  parseFieldValue(field, value);
  return ['int64', 'decimal'].includes(field.dataType) ? value : `'${value.replaceAll("'", "''")}'`;
}

export function queryToSQL(query: QuerySpec, model: QueryContextModel, bindValue: (value: JsonValue, field: Property, op: string) => string = literal, context?: RowQueryContext): string {
  if (query.via || query.from.schemaId !== model.schemaId || query.page.cursor || (query.orderBy?.length ?? 0) > 1) throw new Error('此查询不属于可转换的查询');
  if (query.rows) {
    if (!context || query.rows.nodes[0]?.schemaId !== model.schemaId) throw new Error('关联查询缺少完整上下文');
    rowQueryModel(query.rows, context);
  }
  const reference = (id?: string) => {
    const node = query.rows?.nodes.find((item) => id?.startsWith(`${item.id}.`));
    const target = node ? context!.models.find((item) => item.schemaId === node.schemaId)! : model;
    const field = target.fields.find((item) => item.id === (node ? id!.slice(node.id.length + 1) : id));
    if (!field) throw new Error('查询包含当前不可见的属性，请重新选择');
    if (query.rows && (!node || !field.filterable)) throw new Error('关联查询只支持各模型的主表字段');
    return { field, text: `${quoteIdentifier(node?.alias ?? model.name)}.${quoteIdentifier(field.name)}` };
  };
  function expression(node: Expression, depth = 0): string {
    if (depth > 10) throw new Error('查询条件嵌套过深');
    if (node.op === 'and' || node.op === 'or') {
      if (!node.args?.length || node.field || node.value !== undefined) throw new Error('条件组不完整');
      return `(${node.args.map((child) => expression(child, depth + 1)).join(node.op === 'and' ? ' AND ' : ' OR ')})`;
    }
    if (node.op === 'not') {
      if (node.args?.length !== 1 || node.field || node.value !== undefined) throw new Error('NOT 条件不完整');
      return `(NOT ${expression(node.args[0], depth + 1)})`;
    }
    const { field, text } = reference(node.field);
    if (!field.filterable || node.args?.length) throw new Error(`${field.name} 当前不能用于筛选`);
    if (node.op === 'isNull' || node.op === 'isNotNull') return `${text} IS ${node.op === 'isNotNull' ? 'NOT ' : ''}NULL`;
    if (node.value === undefined) throw new Error('筛选条件缺少值');
    if (node.op === 'startsWith') return `STARTS_WITH(${text}, ${bindValue(node.value, field, node.op)})`;
    if (node.op === 'in' || node.op === 'notIn') {
      if (!Array.isArray(node.value) || !node.value.length) throw new Error('集合条件不能为空');
      return `${text} ${node.op === 'notIn' ? 'NOT IN' : 'IN'} (${node.value.map((value) => bindValue(value, field, node.op)).join(', ')})`;
    }
    const operator = { eq: '=', ne: '<>', lt: '<', lte: '<=', gt: '>', gte: '>=' }[node.op];
    if (!operator || !availableOperators(field).some((item) => item.value === node.op)) throw new Error('条件无法完整转换为 OntologySQL');
    return `${text} ${operator} ${bindValue(node.value, field, node.op)}`;
  }
  if (!query.select.length || new Set(query.select).size !== query.select.length) throw new Error('请先选择返回字段');
  const columns = query.select.map((id) => reference(id).text).join(', ');
  const order = query.orderBy?.[0];
  if (order && !reference(order.field).field.sortable) throw new Error('排序字段当前不支持排序');
  const joins = query.rows?.joins.map((join) => {
    const from = query.rows!.nodes.find((node) => node.id === join.from)!;
    const to = query.rows!.nodes.find((node) => node.id === join.to)!;
    const link = context!.links.find((item) => item.id === join.linkId)!;
    return `\nJOIN LINK ${quoteIdentifier(from.alias)} VIA ${quoteIdentifier(link.category)}.${quoteIdentifier(link.name)} DIRECTION ${join.direction.toUpperCase()} AS ${quoteIdentifier(to.alias)}`;
  }).join('') ?? '';
  return `SELECT ${columns}\nFROM ${quoteIdentifier(model.category)}.${quoteIdentifier(model.name)} AS ${quoteIdentifier(query.rows?.nodes[0].alias ?? model.name)}${joins}${query.where ? `\nWHERE ${expression(query.where)}` : ''}${order ? `\nORDER BY ${reference(order.field).text} ${order.direction.toUpperCase()}` : ''}\nLIMIT ${query.page.limit};`;
}

export function queryToSQLDraft(query: QuerySpec, model: QueryContextModel, context?: RowQueryContext): { text: string; drafts: ParameterDrafts; parameters: Record<string, JsonValue> } {
  const drafts: ParameterDrafts = {};
  const parameters: Record<string, JsonValue> = {};
  let index = 0;
  const text = queryToSQL(query, model, (value, field, op) => {
    literal(value, field);
    const name = `条件${++index}`;
    parameters[name] = value;
    drafts[name] = { mode: 'value', text: String(value), signature: parameterSignature({ ...field, nullable: ['in', 'notIn'].includes(op) && !!field.nullable, name, ranges: [], provided: true }) };
    return `:${name}`;
  }, context);
  return { text, drafts, parameters };
}

export function queryToEditor(query: QuerySpec, model: QueryContextModel, context?: RowQueryContext): QueryEditor {
  if (query.rows && context) {
    if (query.from.schemaId !== query.rows.nodes[0]?.schemaId || query.via) throw new Error('关联查询起点无效');
    return queryToEditor({ ...query, rows: undefined }, rowQueryModel(query.rows, context));
  }
  if (query.rows || query.via || query.from.schemaId !== model.schemaId || query.page.cursor || (query.orderBy?.length ?? 0) > 1) throw new Error('此查询无法完整表示为当前的可视化单模型查询');
  let count = 0;
  function visit(expression: Expression, depth = 0): FilterNode {
    if (++count > 85 || depth > 6) throw new Error('条件数量或嵌套超过可视化编辑器的完整表达范围，请继续使用 OntologySQL');
    if (expression.op === 'and' || expression.op === 'or') {
      if (!expression.args?.length || expression.field || expression.value !== undefined) throw new Error('条件组无法完整转换');
      return { ...newGroup(), join: expression.op, children: expression.args.map((child) => visit(child, depth + 1)) };
    }
    if (expression.op === 'not') {
      if (expression.args?.length !== 1 || expression.field || expression.value !== undefined) throw new Error('NOT 条件无法完整转换');
      const child = visit(expression.args[0], depth + 1);
      return child.kind === 'group' ? { ...child, negated: !child.negated } : { ...newGroup(), negated: true, children: [child] };
    }
    const field = model.fields.find((item) => item.id === expression.field);
    if (!field?.filterable || !availableOperators(field).some((item) => item.value === expression.op) || expression.args?.length) throw new Error('查询包含可视化编辑器无法表达的条件');
    let raw = '';
    if (!['isNull', 'isNotNull'].includes(expression.op)) {
      if (expression.value === undefined || expression.value === null) throw new Error('比较条件包含无法转换的空值');
      if (expression.op === 'in' || expression.op === 'notIn') {
        if (!Array.isArray(expression.value) || !expression.value.length) throw new Error('集合条件无法完整转换');
        if (expression.value.some((value) => typeof value === 'string' && /[\r\n]/.test(value))) throw new Error('集合内包含换行值，可视化的逐行输入无法无损表示，请保留 SQL');
        raw = expression.value.map((value) => { literal(value, field); return String(value); }).join('\n');
      } else {
        literal(expression.value, field);
        if (field.dataType === 'string' && typeof expression.value === 'string' && /[\r\n]/.test(expression.value)) throw new Error('字符串条件包含换行，可视化单行输入无法无损表示，请保留 SQL');
        raw = String(expression.value);
      }
    }
    return { id: crypto.randomUUID(), kind: 'condition', field: field.id, op: expression.op, raw };
  }
  const node = query.where ? visit(query.where) : newGroup();
  const filter: FilterGroup = node.kind === 'group' ? node : { ...newGroup(), children: [node] };
  const editor: QueryEditor = { filter, select: [...query.select], sort: query.orderBy?.[0]?.field ?? '', direction: query.orderBy?.[0]?.direction ?? 'asc', limit: String(query.page.limit) };
  // Revalidate the complete draft through the existing visual compiler before switching.
  compileQuery(model.schemaId, model.fields, editor);
  return editor;
}
