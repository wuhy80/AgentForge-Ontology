import { describe, expect, it } from 'vitest';
import type { QueryContextModel } from '../../lib/types';
import { locateSingleSource, proposeFieldInsertion } from './sqlInsertion';

const model: QueryContextModel = { schemaId: 'notes', categoryId: 'knowledge', category: '个人知识库', name: '笔记', fields: [
  { id: 'title', name: '标题', dataType: 'text', filterable: true, sortable: false },
  { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, filterable: true, sortable: true },
  { id: 'external', name: '外部字段', dataType: 'string', filterable: false, sortable: false },
  { id: 'body', name: '正文', dataType: 'longtext', filterable: true, sortable: false },
] };
const title = model.fields[0], amount = model.fields[1], external = model.fields[2];
const base = 'SELECT "n"."标题"\nFROM "个人知识库"."笔记" AS "n"\nLIMIT 25;';
function edit(text: string, field = amount, cursor = text.length, to = cursor) {
  return proposeFieldInsertion(text, { from: cursor, to }, model, field, 'knowledge');
}
function at(text: string, marker: string, field = amount) {
  const cursor = text.indexOf(marker);
  return edit(text.replace(marker, ''), field, cursor);
}

describe('OntologySQL field insertion', () => {
  it('creates a single-field query from an empty draft, including an explicitly requested long-text field', () => {
    const result = edit('', model.fields[3]);
    expect(result.text).toBe('SELECT "笔记"."正文"\nFROM "个人知识库"."笔记" AS "笔记"\nLIMIT 25;');
    expect(result.text.slice(result.from, result.to)).toBe('"笔记"."正文"');
  });
  it('appends to SELECT when the cursor is in FROM or at the end, preserving all conditions and parameters', () => {
    const text = base.replace('LIMIT', 'WHERE "n"."金额" >= :最低金额\nLIMIT');
    const result = edit(text);
    expect(result.text).toBe(text.replace('SELECT "n"."标题"', 'SELECT "n"."标题", "n"."金额"'));
    expect(result.text.slice(result.from, result.to)).toBe('"n"."金额"');
    expect(edit(text, amount, text.indexOf('FROM') + 2).text).toBe(result.text);
  });
  it('uses the actual AS alias, and supports a fully qualified or a current-category source', () => {
    expect(edit(base).text).toContain('"n"."金额"');
    const unqualified = 'SELECT "标题" FROM "笔记" LIMIT 25;';
    expect(edit(unqualified).text).toContain('"笔记"."金额"');
    expect(locateSingleSource(base)).toEqual({ name: '笔记', category: '个人知识库', alias: 'n' });
  });
  it('fills empty SELECT and comma slots without duplicating commas', () => {
    expect(at('SELECT |\nFROM "笔记";', '|').text).toBe('SELECT "笔记"."金额" \nFROM "笔记";');
    const result = at('SELECT "标题", |\nFROM "笔记";', '|');
    expect(result.text).toMatch(/^SELECT "标题",\s*"笔记"\."金额"\s+FROM/u);
    expect(result.text).not.toContain(',,');
  });
  it('inserts a field before another projection when the caret is in a SELECT gap', () => {
    const result = at('SELECT "标题", |"正文" FROM "笔记";', '|');
    expect(result.text).toBe('SELECT "标题", "笔记"."金额", "正文" FROM "笔记";');
  });
  it('replaces a fully selected projection, but never overwrites part of a quoted token', () => {
    const from = base.indexOf('"n"."标题"');
    expect(edit(base, amount, from, from + '"n"."标题"'.length).text).toBe(base.replace('"n"."标题"', '"n"."金额"'));
    const partial = edit(base, amount, base.indexOf('标题') + 1);
    expect(partial.text).toContain('SELECT "n"."标题", "n"."金额"');
  });
  it('locates an already projected field without producing a duplicate, including unqualified projections', () => {
    const result = edit(base, title);
    expect(result.text).toBe(base);
    expect(result.text.slice(result.from, result.to)).toBe('"n"."标题"');
    expect(result.message).toContain('已在 SELECT');
    const unqualified = 'SELECT "金额" FROM "笔记";';
    expect(edit(unqualified).text).toBe(unqualified);
  });
  it('inserts into WHERE expression slots and preserves field references already used in SELECT', () => {
    const text = 'SELECT "金额" FROM "笔记" WHERE | >= :最低金额 LIMIT 25;';
    expect(at(text, '|').text).toBe(text.replace('|', '"笔记"."金额"'));
    const grouped = 'SELECT "标题" FROM "笔记" WHERE "标题" IS NOT NULL AND (|) LIMIT 25;';
    expect(at(grouped, '|').text).toBe(grouped.replace('|', '"笔记"."金额"'));
    const prefix = 'SELECT "标题" FROM "笔记" WHERE STARTS_WITH(|, :前缀);';
    expect(at(prefix, '|', title).text).toBe(prefix.replace('|', '"笔记"."标题"'));
  });
  it('replaces an entire WHERE left operand and rejects fields that cannot be filtered', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE "标题" = :值;';
    const from = text.lastIndexOf('"标题"');
    expect(edit(text, amount, from, from + '"标题"'.length).text).toBe(text.slice(0, from) + '"笔记"."金额"' + text.slice(from + '"标题"'.length));
    expect(() => at('SELECT "标题" FROM "笔记" WHERE |;', '|', external)).toThrow('不支持 WHERE');
  });
  it('never inserts a field as a comparison value or an IN member; it adds a projection instead', () => {
    for (const text of ['SELECT "标题" FROM "笔记" WHERE "标题" = |;', 'SELECT "标题" FROM "笔记" WHERE "标题" IN (|);', 'SELECT "标题" FROM "笔记" WHERE "标题" IS NOT |;', 'SELECT "标题" FROM "笔记" WHERE "标题" NOT |;']) {
      const result = at(text, '|');
      expect(result.text).toBe(text.replace('SELECT "标题"', 'SELECT "标题", "笔记"."金额"').replace('|', ''));
    }
  });
  it('recognizes nested prefix NOT independently of IS NOT NULL and NOT IN', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE NOT ((NOT |));';
    expect(at(text, '|').text).toBe(text.replace('|', '"笔记"."金额"'));
  });
  it('preserves unary NOT conditions when the caret is immediately before NOT', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE |NOT "标题" IS NULL;';
    expect(at(text, '|').text).toBe(text.replace('SELECT "标题"', 'SELECT "标题", "笔记"."金额"').replace('|', ''));
  });
  it('allows a missing left operand before NOT IN without mistaking it for unary NOT', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE |NOT IN (1, 2);';
    expect(at(text, '|').text).toBe(text.replace('|', '"笔记"."金额" '));
  });
  it('fills or replaces the one ORDER BY field and refuses an unsortable field', () => {
    const text = 'SELECT "标题" FROM "笔记" ORDER BY | LIMIT 25;';
    expect(at(text, '|').text).toBe(text.replace('|', '"笔记"."金额"'));
    expect(() => at(text, '|', title)).toThrow('不支持 ORDER BY');
    const existing = 'SELECT "标题" FROM "笔记" ORDER BY "标题" DESC;';
    const from = existing.lastIndexOf('"标题"');
    expect(edit(existing, amount, from, from + '"标题"'.length).text).toBe(existing.slice(0, from) + '"笔记"."金额"' + existing.slice(from + '"标题"'.length));
  });
  it('adds a projection when the caret is after an existing sort, without appending a second sort field', () => {
    const text = 'SELECT "标题" FROM "笔记" ORDER BY "金额" DESC|;';
    expect(at(text, '|').text).toBe(text.replace('SELECT "标题"', 'SELECT "标题", "笔记"."金额"').replace('|', ''));
  });
  it('preserves comments, literals, CRLF and UTF-16 offsets, including emoji before the selection', () => {
    const text = '-- FROM "假模型" 😀\r\nSELECT "n"."标题" -- 标题\r\nFROM "个人知识库"."笔记" AS "n"\r\nWHERE "n"."标题" = \'SELECT; JOIN FROM 😀\'\r\nLIMIT 25;';
    const result = edit(text);
    expect(result.text).toBe(text.replace('SELECT "n"."标题"', 'SELECT "n"."标题", "n"."金额"'));
    expect(result.text.slice(result.from, result.to)).toBe('"n"."金额"');
    expect(() => edit(text, amount, text.indexOf('😀') + 1)).toThrow('原稿已保留');
    expect(() => edit(text, amount, text.indexOf('JOIN') + 1)).toThrow('原稿已保留');
  });
  it.each([
    ['SELECT "标题" FROM "笔记" WHERE "标题" = \'文|本\';', '|'],
    ['SELECT "标题" FROM "笔记" /*字段|说明*/;', '|'],
    ['SELECT "标题" FROM "笔记" --字段|说明', '|'],
    ['SELECT "标题" FROM "笔记" WHERE "金额" = :最|低金额;', '|'],
    ['SELECT "标题" FROM "笔记" WHERE "金额" = 123|45;', '|'],
  ])('does not modify SQL when the caret is inside a literal or comment: %s', (text, marker) => {
    expect(() => at(text, marker)).toThrow('原稿已保留');
  });
  it.each([
    'SELECT "标题" FROM "笔记" WHERE --说明|',
    'SELECT "标题" FROM "笔记" WHERE --说明|\nLIMIT 25;',
    'SELECT "标题" FROM "笔记" WHERE --说明|\r\nLIMIT 25;',
  ])('rejects a caret at the end of a line comment, including EOF and CRLF: %s', (text) => {
    expect(() => at(text, '|')).toThrow('原稿已保留');
  });
  it.each(['\n', '\r\n'])('allows insertion after a line comment newline %j', (newline) => {
    const text = `SELECT "标题" FROM "笔记" WHERE --说明${newline}|LIMIT 25;`;
    expect(at(text, '|').text).toBe(text.replace('|', '"笔记"."金额" '));
  });
  it('does not overwrite a selection that includes a literal or comment', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE "标题" = \'原值\';';
    const from = text.indexOf("'原值'");
    expect(() => edit(text, amount, from, from + "'原值'".length)).toThrow('原稿已保留');
  });
  it('escapes identifier quotes and treats punctuation and keywords inside quoted names as ordinary names', () => {
    const odd = { ...model, name: '笔"记', fields: [{ ...amount, name: ',;JOIN"😀' }] };
    const text = 'SELECT FROM "个人知识库"."笔""记" AS "n""😀";';
    const result = proposeFieldInsertion(text, { from: text.length, to: text.length }, odd, odd.fields[0], 'knowledge');
    expect(result.text).toContain('SELECT "n""😀".",;JOIN""😀"');
    expect(result.text.slice(result.from, result.to)).toBe('"n""😀".",;JOIN""😀"');
    const punctuation = { ...model, fields: [{ ...amount, name: ',' }] };
    const comma = 'SELECT "," FROM "笔记";';
    expect(proposeFieldInsertion(comma, { from: comma.length, to: comma.length }, punctuation, punctuation.fields[0], 'knowledge').text).toBe(comma);
  });
  it('does not treat a semicolon string literal as a statement delimiter', () => {
    const text = 'SELECT "标题" FROM "笔记" WHERE "标题" = \';\';';
    expect(edit(text).text).toContain('SELECT "标题", "笔记"."金额"');
  });
  it('refuses a field from another model/category and a stale or unavailable field', () => {
    expect(() => edit(base.replace('"笔记" AS', '"项目" AS'))).toThrow('FROM 不一致');
    expect(() => edit(base.replace('"个人知识库"', '"其他分类"'))).toThrow('FROM 不一致');
    expect(() => proposeFieldInsertion('SELECT "标题" FROM "笔记";', { from: 0, to: 0 }, model, title, 'other-category')).toThrow('FROM 不一致');
    expect(() => edit(base, { ...amount, id: 'missing' })).toThrow('不可用');
  });
  it.each([
    ['SELECT "标题" FROM "笔记" JOIN LINK "笔记"."引用" AS "n";', '尚未开放'],
    ['SELECT "标题" FROM "笔记"; SELECT "标题" FROM "笔记";', '单模型'],
    ['SELECT "标题" FROM "笔记"; DELETE FROM "笔记";', '一条查询'],
    ['SELECT "标题" FROM "笔记" UNION SELECT "标题" FROM "笔记";', '单模型'],
    ['SELECT * FROM "笔记";', '显式字段'],
    ['SELECT "标题" AS "名称" FROM "笔记";', '显式字段'],
    ['SELECT "标题" FROM "笔记" WHERE "标题" = \'未完成', '补全引号'],
    ['SELECT "标题" FROM "笔记" /*未完成', '补全块注释'],
    ['SELECT "标题" FROM "笔记" AS', '补全 AS'],
    ['SELECT "标题"', '补全 FROM'],
  ])('refuses unsupported or unsafe editing without dropping SQL: %s', (text, message) => {
    expect(() => edit(text)).toThrow(message);
  });
});
