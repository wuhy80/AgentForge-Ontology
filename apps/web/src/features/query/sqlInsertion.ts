import type { QueryContextField, QueryContextModel } from '../../lib/types';
import { modelTemplate, quoteIdentifier } from './ontologySql';

export type SQLSelection = { from: number; to: number };
export type SQLInsertion = SQLSelection & { text: string; message?: string };
type Token = SQLSelection & { kind: 'word' | 'identifier' | 'literal' | 'symbol' | 'comment'; value: string };
type FieldReference = SQLSelection & { name: string; qualifier?: string };
type Source = {
  name: string; category?: string; alias: string;
  tokens: Token[]; projection: FieldReference[]; projectionTokens: Token[];
  select: Token; from: Token; where?: Token; order?: Token; by?: Token; limit?: Token;
};

// This scanner only finds safe text-editing boundaries. The server remains the
// sole authority for binding, types, permissions, and executable query semantics.
function scan(text: string): Token[] {
  const tokens: Token[] = [];
  for (let i = 0; i < text.length;) {
    if (/\s/u.test(text[i])) { i++; continue; }
    const from = i;
    if (text.startsWith('--', i)) {
      while (i < text.length && !/[\r\n]/.test(text[i])) i++;
      tokens.push({ from, to: i, kind: 'comment', value: text.slice(from, i) }); continue;
    }
    if (text.startsWith('/*', i)) {
      const end = text.indexOf('*/', i + 2);
      if (end === -1) throw new Error('请先补全块注释，再插入字段');
      i = end + 2; tokens.push({ from, to: i, kind: 'comment', value: text.slice(from, i) }); continue;
    }
    const quote = text[i];
    if (quote === '"' || quote === "'") {
      let value = '', closed = false; i++;
      while (i < text.length) {
        if (text[i] === quote) {
          if (text[i + 1] === quote) { value += quote; i += 2; continue; }
          i++; closed = true; break;
        }
        value += text[i++];
      }
      if (!closed) throw new Error('请先补全引号，再插入字段');
      tokens.push({ from, to: i, kind: quote === '"' ? 'identifier' : 'literal', value }); continue;
    }
    if (/[A-Za-z_]/.test(text[i])) {
      while (i < text.length && /[A-Za-z_0-9]/.test(text[i])) i++;
      tokens.push({ from, to: i, kind: 'word', value: text.slice(from, i).toUpperCase() }); continue;
    }
    if (text[i] === ':') {
      i++;
      while (i < text.length && /[\p{L}\p{N}_]/u.test(text[i])) i++;
      tokens.push({ from, to: i, kind: 'literal', value: text.slice(from, i) }); continue;
    }
    if (/[0-9]/.test(text[i])) {
      while (i < text.length && /[0-9.]/.test(text[i])) i++;
      tokens.push({ from, to: i, kind: 'literal', value: text.slice(from, i) }); continue;
    }
    i += text.codePointAt(i)! > 0xffff ? 2 : 1;
    tokens.push({ from, to: i, kind: 'symbol', value: text.slice(from, i) });
  }
  return tokens;
}

const keyword = (token: Token | undefined, value: string) => token?.kind === 'word' && token.value === value;
const symbol = (token: Token | undefined, value: string) => token?.kind === 'symbol' && token.value === value;
function fieldReference(tokens: Token[]): FieldReference | undefined {
  if (tokens.length === 1 && tokens[0].kind === 'identifier') return { ...tokens[0], name: tokens[0].value };
  if (tokens.length === 3 && tokens[0].kind === 'identifier' && symbol(tokens[1], '.') && tokens[2].kind === 'identifier') {
    return { from: tokens[0].from, to: tokens[2].to, qualifier: tokens[0].value, name: tokens[2].value };
  }
}
function source(text: string): Source {
  const tokens = scan(text).filter((token) => token.kind !== 'comment');
  if (!keyword(tokens[0], 'SELECT')) throw new Error('请先生成一条单模型 SELECT 查询，再插入字段');
  if (tokens.some((token, index) => keyword(token, 'SELECT') && index !== 0 || token.kind === 'word' && ['JOIN', 'UNION', 'INTERSECT', 'EXCEPT', 'WITH'].includes(token.value))) {
    throw new Error('当前仅支持为单模型查询插入字段；多模型关联查询尚未开放，原稿已保留');
  }
  const semicolon = tokens.findIndex((token) => symbol(token, ';'));
  if (semicolon !== -1 && semicolon !== tokens.length - 1) throw new Error('请保留一条查询语句，再插入字段');
  const fromIndex = tokens.findIndex((token) => keyword(token, 'FROM'));
  if (fromIndex === -1 || tokens.filter((token) => keyword(token, 'FROM')).length !== 1) throw new Error('请先补全 FROM 中的查询模型');
  let index = fromIndex + 1;
  if (tokens[index]?.kind !== 'identifier') throw new Error('请先用双引号补全 FROM 中的查询模型');
  let name = tokens[index++].value, category: string | undefined;
  if (symbol(tokens[index], '.')) {
    index++;
    if (tokens[index]?.kind !== 'identifier') throw new Error('请先补全 FROM 中的分类和模型');
    category = name; name = tokens[index++].value;
  }
  let alias = name;
  if (keyword(tokens[index], 'AS')) {
    index++;
    if (tokens[index]?.kind !== 'identifier') throw new Error('请先补全 AS 后的模型别名');
    alias = tokens[index++].value;
  }
  const next = tokens[index];
  if (next && !symbol(next, ';') && !['WHERE', 'ORDER', 'LIMIT'].some((value) => keyword(next, value))) {
    throw new Error('FROM 后仅支持一个模型及 AS 别名；请先修正查询，原稿已保留');
  }
  const clauses = tokens.slice(index);
  const where = clauses.find((token) => keyword(token, 'WHERE'));
  const orderIndex = tokens.findIndex((token) => keyword(token, 'ORDER'));
  const order = orderIndex === -1 ? undefined : tokens[orderIndex];
  const by = order ? tokens[orderIndex + 1] : undefined;
  if (order && !keyword(by, 'BY')) throw new Error('请先补全 ORDER BY，再插入字段');
  const limit = clauses.find((token) => keyword(token, 'LIMIT'));
  for (const clause of ['WHERE', 'ORDER', 'LIMIT']) {
    if (clauses.filter((token) => keyword(token, clause)).length > 1) throw new Error('查询包含重复子句，请先修正后再插入字段');
  }
  if (where && order && where.from > order.from || where && limit && where.from > limit.from || order && limit && order.from > limit.from) {
    throw new Error('请按 WHERE、ORDER BY、LIMIT 的顺序编写查询');
  }
  const projectionTokens = tokens.slice(1, fromIndex);
  const projection: FieldReference[] = [];
  let item: Token[] = [];
  for (const token of projectionTokens) {
    if (symbol(token, ',')) {
      const reference = fieldReference(item);
      if (!reference) throw new Error('SELECT 仅支持显式字段列表，请先修正返回字段');
      projection.push(reference); item = [];
    } else item.push(token);
  }
  if (item.length) {
    const reference = fieldReference(item);
    if (!reference) throw new Error('SELECT 仅支持显式字段列表，请先修正返回字段');
    projection.push(reference);
  }
  return { name, category, alias, tokens, projection, projectionTokens, select: tokens[0], from: tokens[fromIndex], where, order, by, limit };
}

export function locateSingleSource(text: string): { name: string; category?: string; alias: string } {
  const { name, category, alias } = source(text);
  return { name, category, alias };
}

function replace(text: string, selection: SQLSelection, value: string, message?: string): SQLInsertion {
  return { text: text.slice(0, selection.from) + value + text.slice(selection.to), from: selection.from, to: selection.from + value.length, message };
}
function insertReference(text: string, selection: SQLSelection, reference: string): SQLInsertion {
  const prefix = selection.from > 0 && /[\p{L}\p{N}_"']/u.test(text[selection.from - 1]) ? ' ' : '';
  const suffix = selection.to < text.length && /[\p{L}\p{N}_"']/u.test(text[selection.to]) ? ' ' : '';
  return { ...replace(text, selection, prefix + reference + suffix), from: selection.from + prefix.length, to: selection.from + prefix.length + reference.length };
}
function selectedReference(text: string, tokens: Token[], selection: SQLSelection): FieldReference | undefined {
  if (selection.from === selection.to) return;
  const included = tokens.filter((token) => token.to > selection.from && token.from < selection.to);
  const reference = fieldReference(included);
  if (!reference || reference.from < selection.from || reference.to > selection.to) return;
  if (text.slice(selection.from, reference.from).trim() || text.slice(reference.to, selection.to).trim()) return;
  const previous = tokens.filter((token) => token.to <= selection.from).at(-1);
  const next = tokens.find((token) => token.from >= selection.to);
  if (symbol(previous, '.') || symbol(next, '.')) return;
  return reference;
}
function fieldSlot(tokens: Token[], selection: SQLSelection, where: Token): boolean {
  const before = tokens.filter((token) => token.to <= selection.from);
  for (let index = before.length - 1; index >= 0; index--) {
    const previous = before[index];
    if (previous === where || ['AND', 'OR'].some((value) => keyword(previous, value))) return true;
    if (symbol(previous, '(') && keyword(before[index - 1], 'STARTS_WITH')) return true;
    if (!symbol(previous, '(') && !keyword(previous, 'NOT')) return false;
  }
  return false;
}

/** Propose a reversible edit; this function never executes or semantically binds SQL. */
export function proposeFieldInsertion(text: string, selection: SQLSelection, model: QueryContextModel, field: QueryContextField, defaultCategoryId: string): SQLInsertion {
  const current = model.fields.find((item) => item.id === field.id && item.name === field.name);
  if (!current) throw new Error('当前字段不可用，请刷新模型目录');
  if (!text.trim()) {
    const next = modelTemplate({ ...model, fields: [current] });
    const reference = `${quoteIdentifier(model.name)}.${quoteIdentifier(current.name)}`;
    const from = next.indexOf(reference);
    return { text: next, from, to: from + reference.length, message: '已生成此字段的查询，检查后点击运行。' };
  }
  const scope = source(text);
  if (scope.name !== model.name || scope.category !== undefined && scope.category !== model.category || scope.category === undefined && model.categoryId !== defaultCategoryId) {
    throw new Error('此字段所属模型与 FROM 不一致；请先为该模型生成查询，原稿已保留');
  }
  const range = { from: Math.max(0, Math.min(text.length, selection.from)), to: Math.max(0, Math.min(text.length, selection.to)) };
  if (range.to < range.from) [range.from, range.to] = [range.to, range.from];
  const reference = `${quoteIdentifier(scope.alias)}.${quoteIdentifier(current.name)}`;
  const allTokens = scan(text);
  const unsafe = allTokens.some((token) => (token.kind === 'comment' || token.kind === 'literal') && (range.from === range.to
    ? token.from < range.from && (range.from < token.to || token.kind === 'comment' && token.value.startsWith('--') && range.from === token.to)
    : token.from < range.to && token.to > range.from));
  if (unsafe) throw new Error('光标或选区位于字符串、参数、数值或注释中；请先移到字段位置，原稿已保留');
  const fullSelection = selectedReference(text, scope.tokens, range);
  const statementEnd = scope.tokens.find((token) => symbol(token, ';'))?.from ?? text.length;
  const whereEnd = scope.order?.from ?? scope.limit?.from ?? statementEnd;
  const inWhere = !!scope.where && range.from >= scope.where.to && range.to <= whereEnd;
  const inOrder = !!scope.by && range.from >= scope.by.to && range.to <= (scope.limit?.from ?? statementEnd);
  const overlappingToken = scope.tokens.some((token) => token.from < range.from && token.to > range.from);
  const nextToken = scope.tokens.find((token) => token.from >= range.to);
  const nextIndex = nextToken ? scope.tokens.indexOf(nextToken) : -1;
  const nextAllowsField = !nextToken || nextToken.kind === 'symbol' && /^[=<>!),;]$/.test(nextToken.value)
    || ['IS', 'IN', 'ORDER', 'LIMIT'].some((value) => keyword(nextToken, value))
    || keyword(nextToken, 'NOT') && keyword(scope.tokens[nextIndex + 1], 'IN');
  if (inWhere && (fullSelection || range.from === range.to && !overlappingToken && nextAllowsField) && fieldSlot(scope.tokens, range, scope.where!)) {
    if (!current.filterable) throw new Error('此字段当前只能作为返回字段，不支持 WHERE 筛选');
    return insertReference(text, range, reference);
  }
  if (inOrder) {
    const orderTokens = scope.tokens.filter((token) => token.from >= scope.by!.to && token.to <= (scope.limit?.from ?? statementEnd));
    const before = scope.tokens.filter((token) => token.to <= range.from).at(-1);
    if (fullSelection && before === scope.by || !orderTokens.length && range.from === range.to && !overlappingToken) {
      if (!current.sortable) throw new Error('此字段不支持 ORDER BY 排序，请选择可排序字段');
      return insertReference(text, range, reference);
    }
  }
  const duplicate = scope.projection.find((item) => item.name === current.name && (!item.qualifier || item.qualifier === scope.alias));
  if (duplicate) return { text, from: duplicate.from, to: duplicate.to, message: '此字段已在 SELECT 中，已定位到现有字段。' };
  const inProjection = range.from >= scope.select.to && range.to <= scope.from.from;
  if (fullSelection && inProjection && scope.projection.some((item) => item.from === fullSelection.from && item.to === fullSelection.to)) return replace(text, range, reference);
  const position = inProjection && range.from === range.to && !overlappingToken ? range.from : undefined;
  if (!scope.projection.length) return insertReference(text, { from: scope.select.to, to: scope.select.to }, reference);
  const last = scope.projection.at(-1)!;
  const trailingComma = symbol(scope.projectionTokens.at(-1), ',') ? scope.projectionTokens.at(-1) : undefined;
  if (trailingComma && (position === undefined || position >= last.to)) {
    const result = replace(text, { from: trailingComma.to, to: trailingComma.to }, ' ' + reference);
    return { ...result, from: result.from + 1, message: '已加入 SELECT 返回字段。' };
  }
  if (position !== undefined) {
    const next = scope.projection.find((item) => item.from >= position);
    if (next) {
      const result = replace(text, { from: next.from, to: next.from }, reference + ', ');
      return { ...result, to: result.from + reference.length };
    }
  }
  const result = replace(text, { from: last.to, to: last.to }, ', ' + reference);
  return { ...result, from: result.from + 2, message: '已加入 SELECT 返回字段。' };
}
