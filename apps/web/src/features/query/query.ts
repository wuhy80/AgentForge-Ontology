import type { Expression, JsonValue, Property, QuerySpec, Values } from '../../lib/types';

export type FilterCondition = { id: string; kind: 'condition'; field: string; op: string; raw: string };
export type FilterGroup = { id: string; kind: 'group'; join: 'and' | 'or'; negated: boolean; children: FilterNode[] };
export type FilterNode = FilterCondition | FilterGroup;
export type QueryEditor = { filter: FilterGroup; select: string[]; sort: string; direction: 'asc' | 'desc'; limit: string };
export type FieldDraft = { mode: 'unchanged' | 'omit' | 'value' | 'null'; text: string };
export type FieldDrafts = Record<string, FieldDraft>;

export function newGroup(): FilterGroup {
  return { id: crypto.randomUUID(), kind: 'group', join: 'and', negated: false, children: [] };
}

export function newCondition(properties: Property[]): FilterCondition {
  return { id: crypto.randomUUID(), kind: 'condition', field: properties[0]?.id ?? '', op: 'eq', raw: properties[0]?.dataType === 'boolean' ? 'true' : '' };
}

export const operators = [
  { value: 'eq', label: '等于' }, { value: 'ne', label: '不等于' },
  { value: 'gt', label: '大于' }, { value: 'gte', label: '大于或等于' },
  { value: 'lt', label: '小于' }, { value: 'lte', label: '小于或等于' },
  { value: 'startsWith', label: '开头是' },
  { value: 'in', label: '属于集合 IN' }, { value: 'notIn', label: '不属于集合' },
  { value: 'isNull', label: '为空值' }, { value: 'isNotNull', label: '不为空值' },
];

export function availableOperators(property?: Property) {
  return operators.filter(({ value }) => {
    if (value === 'startsWith') return property?.dataType === 'string' || property?.dataType === 'text' || property?.dataType === 'longtext';
    return property?.dataType !== 'boolean' || !['gt', 'gte', 'lt', 'lte'].includes(value);
  });
}

export function parseFieldValue(property: Property, text: string): JsonValue {
  const fail = (message: string): never => { throw new Error(`${property.name}：${message}`); };
  switch (property.dataType) {
    case 'string':
      if (Array.from(text).length > 255) fail('最多 255 个字符');
      return text;
    case 'text':
      if (new TextEncoder().encode(text).length > 65535) fail('内容超过 65535 字节');
      return text;
    case 'longtext':
      if (new TextEncoder().encode(text).length > 4 * 1024 * 1024) fail('内容超过 4 MiB（UTF-8 字节）');
      return text;
    case 'uuid':
      if (!/^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(text)) fail('请输入完整 UUID');
      return text;
    case 'int64': {
      if (!/^-?(0|[1-9]\d*)$/.test(text) || text === '-0') fail('请输入不含前导零的整数');
      const value = BigInt(text);
      if (value < -9223372036854775808n || value > 9223372036854775807n) fail('超出 int64 范围');
      return text;
    }
    case 'decimal': {
      if (!/^-?(0|[1-9]\d*)(\.\d+)?$/.test(text)) fail('请输入十进制数，不支持指数记法');
      const [whole, fraction = ''] = text.replace(/^-/, '').split('.');
      if (whole.replace(/^0+/, '').length > (property.precision ?? 0) - (property.scale ?? 0) || fraction.length > (property.scale ?? 0)) {
        fail(`超出 decimal(${property.precision}, ${property.scale}) 精度`);
      }
      return text;
    }
    case 'boolean':
      if (text !== 'true' && text !== 'false') fail('请选择是或否');
      return text === 'true';
    case 'timestamp': {
      // Validate the calendar without serializing through Date (which loses microseconds).
      const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(text);
      if (!match) fail('使用带时区的时间，例如 2026-11-06T14:30:00.123456+08:00');
      const [, yearText, monthText, dayText, hourText, minuteText, secondText, fraction = '', zone] = match!;
      const year = Number(yearText), month = Number(monthText), day = Number(dayText);
      const days = [31, year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
      if (month < 1 || month > 12 || day < 1 || day > days[month - 1] || Number(hourText) > 23 || Number(minuteText) > 59 || Number(secondText) > 59 || /[1-9]/.test(fraction.slice(6))) fail('时间无效或超过微秒精度');
      if (zone !== 'Z' && (Number(zone.slice(1, 3)) > 23 || Number(zone.slice(4)) > 59)) fail('时区偏移无效');
      const instant = new Date(text);
      if (!Number.isFinite(instant.getTime()) || instant.getUTCFullYear() < 1000 || instant.getUTCFullYear() > 9999) fail('UTC 年份必须在 1000 至 9999 之间');
      return text;
    }
    default:
      return fail('当前页面不支持此类型');
  }
}

export function compileFilter(group: FilterGroup, properties: Property[]): Expression | undefined {
  let count = 0;
  function visit(node: FilterNode, depth: number, root = false): Expression | undefined {
    count += node.kind === 'group' && node.negated ? 2 : 1;
    if (count > 90 || depth > 8) throw new Error('筛选条件过多或嵌套过深，请简化条件');
    if (node.kind === 'group') {
      if (!node.children.length) {
        if (root && !node.negated) return undefined;
        throw new Error('条件组不能为空，请添加条件或移除空组');
      }
      const args = node.children.map((child) => visit(child, depth + (node.negated ? 2 : 1))!);
      const expression: Expression = { op: node.join, args };
      return node.negated ? { op: 'not', args: [expression] } : expression;
    }
    const property = properties.find((item) => item.id === node.field);
    if (!property) throw new Error('筛选字段不存在，请重新选择');
    if (!availableOperators(property).some((item) => item.value === node.op)) throw new Error(`${property.name} 不支持此操作符`);
    if (node.op === 'isNull' || node.op === 'isNotNull') return { op: node.op, field: property.id };
    let value: JsonValue;
    if (node.op === 'in' || node.op === 'notIn') {
      const rows = node.raw.replace(/\r\n/g, '\n').split('\n');
      if (rows.length > 100) throw new Error('集合最多包含 100 个值');
      value = rows.map((raw) => parseFieldValue(property, raw));
    } else {
      value = parseFieldValue(property, node.raw);
    }
    return { op: node.op, field: property.id, value };
  }
  return visit(group, 0, true);
}

export function compileQuery(schemaId: string, properties: Property[], editor: QueryEditor): QuerySpec {
  if (!editor.select.length) throw new Error('请至少勾选一个返回字段');
  if (new Set(editor.select).size !== editor.select.length || editor.select.some((id) => !properties.some((property) => property.id === id))) throw new Error('返回字段已失效，请重新选择');
  if (!/^\d+$/.test(editor.limit) || Number(editor.limit) < 1 || Number(editor.limit) > 100) throw new Error('每页条数应为 1 至 100 的整数');
  const where = compileFilter(editor.filter, properties);
  const sort = properties.find((property) => property.id === editor.sort);
  if (editor.sort && (!sort || sort.dataType === 'text' || sort.dataType === 'longtext')) throw new Error('当前字段不支持排序');
  return {
    from: { schemaId }, select: [...editor.select], ...(where ? { where } : {}),
    ...(sort ? { orderBy: [{ field: sort.id, direction: editor.direction }] } : {}),
    page: { limit: Number(editor.limit) },
  };
}

export function valueText(value: JsonValue | undefined): string {
  if (value === undefined) return '未返回';
  if (value === null) return '空值 NULL';
  if (value === '') return '空字符串';
  if (typeof value === 'boolean') return value ? '是' : '否';
  return typeof value === 'string' ? value : JSON.stringify(value);
}

export function draftText(value: JsonValue | undefined): string {
  return value === null || value === undefined ? '' : typeof value === 'string' ? value : String(value);
}

export function initialDrafts(properties: Property[], original?: Values): FieldDrafts {
  return Object.fromEntries(properties.map((property) => {
    const hasDefault = Object.hasOwn(property, 'default');
    const mode = original ? 'unchanged' : !property.readOnly && !hasDefault && (property.required || !property.nullable) ? 'value' : 'omit';
    return [property.id, { mode, text: original ? draftText(original[property.id]) : '' }];
  }));
}

export function buildWriteValues(properties: Property[], drafts: FieldDrafts, original?: Values): Values {
  const values: Values = {};
  for (const property of properties) {
    if (property.readOnly) continue;
    const draft = drafts[property.id];
    if (!draft || draft.mode === 'omit' || draft.mode === 'unchanged') {
      if (!original && !Object.hasOwn(property, 'default') && (property.required || !property.nullable)) throw new Error(`${property.name}：需要显式填写`);
      continue;
    }
    if (draft.mode === 'null' && !property.nullable) throw new Error(`${property.name}：不允许空值`);
    const value = draft.mode === 'null' ? null : parseFieldValue(property, draft.text);
    if (!original || !Object.hasOwn(original, property.id) || JSON.stringify(original[property.id]) !== JSON.stringify(value)) values[property.id] = value;
  }
  return values;
}

export function writeIntentSignature(values: Values): string {
  // Definition display edits can reorder properties without changing the write intent.
  return JSON.stringify(Object.keys(values).sort().map((id) => [id, values[id]]));
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : '请求失败，请稍后重试';
}
