import { describe, expect, it } from 'vitest';
import type { QueryContextModel, QuerySpec, SQLParameter } from '../../lib/types';
import { compileQuery } from './query';
import { modelTemplate, parameterSignature, parameterValues, queryToEditor, queryToSQL, queryToSQLDraft } from './ontologySql';

const model: QueryContextModel = { schemaId: 'notes', categoryId: 'knowledge', category: '个人知识库', name: '笔记', fields: [
  { id: 'title', name: '标"题', dataType: 'text', nullable: true, filterable: true, sortable: false },
  { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: true, filterable: true, sortable: true },
  { id: 'body', name: '正文', dataType: 'longtext', filterable: true, sortable: false },
  { id: 'external', name: '外部字段', dataType: 'string', filterable: false, sortable: false },
] };
const base: QuerySpec = { from: { schemaId: model.schemaId }, select: ['title', 'amount'], page: { limit: 25 } };
const parameter: SQLParameter = { name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: false, provided: false, ranges: [] };

describe('OntologySQL editor conversions and parameters', () => {
  it('templates escape business names, omit long text and generate syntactically safe parameter names', () => {
    const template = modelTemplate(model, 'parameter');
    expect(template).toContain('"笔记"."标""题"');
    expect(template).not.toContain('"正文"');
    expect(template).not.toContain('ORDER BY');
    expect(template).toContain(':参数标题前缀');
    expect(template).toContain('FROM "个人知识库"."笔记"');
  });
  it('keeps decimal precision and produces independent typed parameter values for visual queries', () => {
    const query: QuerySpec = { ...base, where: { op: 'gte', field: 'amount', value: '9007199254740993.000001' } };
    const converted = queryToSQLDraft(query, model);
    expect(converted.text).toContain('"金额" >= :条件1');
    expect(converted.text).not.toContain('9007199254740993');
    expect(parameterValues([{ ...parameter, name: '条件1' }], converted.drafts)).toEqual({ 条件1: '9007199254740993.000001' });
    const editor = queryToEditor(query, model);
    expect(compileQuery(model.schemaId, model.fields, editor).where?.args?.[0].value).toBe('9007199254740993.000001');
  });
  it('keeps nested NOT, OR, typed sets, explicit projection and sort without dropping predicates', () => {
    const query: QuerySpec = { ...base, orderBy: [{ field: 'amount', direction: 'desc' }], where: { op: 'not', args: [{ op: 'or', args: [
      { op: 'in', field: 'amount', value: ['0.000001', '9007199254740993.000001'] },
      { op: 'isNull', field: 'title' },
    ] }] } };
    const editor = queryToEditor(query, model);
    const roundtrip = compileQuery(model.schemaId, model.fields, editor);
    expect(roundtrip).toEqual(query);
    expect(queryToSQL(roundtrip, model)).toContain('(NOT ("笔记"."金额" IN (0.000001, 9007199254740993.000001) OR "笔记"."标""题" IS NULL))');
  });
  it('rejects entire nonrepresentable queries rather than converting a subset', () => {
    expect(() => queryToEditor({ ...base, via: { linkId: 'link', direction: 'out', instanceId: 'one' } }, model)).toThrow('完整');
    expect(() => queryToEditor({ ...base, where: { op: 'in', field: 'title', value: ['第一行\n第二行'] } }, model)).toThrow('换行');
    expect(() => queryToEditor({ ...base, where: { op: 'eq', field: 'external', value: 'hidden' } }, model)).toThrow('无法表达');
    expect(() => queryToSQL({ ...base, orderBy: [{ field: 'title', direction: 'asc' }] }, model)).toThrow('排序');
    expect(() => queryToEditor({ ...base, where: { op: 'unknown', field: 'amount', value: '1' } }, model)).toThrow('无法表达');
  });
  it('distinguishes missing, empty string and NULL and discards values after an incompatible type change', () => {
    const p: SQLParameter = { ...parameter, name: '前缀', dataType: 'string', precision: undefined, scale: undefined, nullable: true };
    const signature = parameterSignature(p);
    expect(parameterValues([p], { 前缀: { mode: 'missing', text: '', signature } })).toEqual({});
    expect(parameterValues([p], { 前缀: { mode: 'value', text: '', signature } })).toEqual({ 前缀: '' });
    expect(parameterValues([p], { 前缀: { mode: 'null', text: '', signature } })).toEqual({ 前缀: null });
    expect(parameterValues([{ ...p, dataType: 'int64' }], { 前缀: { mode: 'value', text: '123', signature } })).toEqual({});
    expect(() => parameterValues([parameter], { 金额: { mode: 'null', text: '', signature: parameterSignature(parameter) } })).toThrow('NULL');
  });
  it('preserves legal prototype-like parameter names as own JSON keys', () => {
    const p = { ...parameter, name: '__proto__' };
    const result = parameterValues([p], { ['__proto__']: { mode: 'value', text: '123.000001', signature: parameterSignature(p) } });
    expect(Object.hasOwn(result, '__proto__')).toBe(true);
    expect(JSON.stringify(result)).toBe('{"__proto__":"123.000001"}');
  });
});
