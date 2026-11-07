import { describe, expect, it } from 'vitest';
import type { Property } from '../../lib/types';
import { availableOperators, buildWriteValues, compileQuery, initialDrafts, newCondition, newGroup, parseFieldValue, valueText, writeIntentSignature } from './query';

const amount: Property = { id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: true };
const integer: Property = { id: 'integer', name: '数量', dataType: 'int64', nullable: true };
const timestamp: Property = { id: 'time', name: '发生时间', dataType: 'timestamp', nullable: true };
const title: Property = { id: 'title', name: '名称', dataType: 'string', required: true };

describe('visual query compilation', () => {
  it('keeps adjacent large decimals and int64 values as strings, including IN predicates', () => {
    const group = newGroup();
    group.children = [
      { ...newCondition([amount]), op: 'in', raw: '9007199254740992.000001\n9007199254740993.000001' },
      { ...newCondition([integer]), raw: '9223372036854775807' },
    ];
    const spec = compileQuery('schema', [amount, integer], { filter: group, select: ['amount'], sort: '', direction: 'asc', limit: '25' });
    expect(spec.where?.args?.[0].value).toEqual(['9007199254740992.000001', '9007199254740993.000001']);
    expect(spec.where?.args?.[1].value).toBe('9223372036854775807');
    expect(spec.page).toEqual({ limit: 25 });
    expect(spec.from).toEqual({ schemaId: 'schema' });
    expect(() => parseFieldValue(integer, '9223372036854775808')).toThrow('超出 int64');
    expect(() => parseFieldValue(integer, '-0')).toThrow();
    expect(() => parseFieldValue(amount, '1e8')).toThrow();
    expect(() => parseFieldValue(amount, '1.1234567')).toThrow('精度');
  });

  it('compiles nested OR, NOT and explicit NULL without injecting a value', () => {
    const root = newGroup(), child = newGroup();
    child.join = 'or'; child.negated = true;
    child.children = [
      { ...newCondition([amount]), op: 'isNull', raw: 'ignored' },
      { ...newCondition([amount]), op: 'gt', raw: '12.3' },
    ];
    root.children = [child];
    const spec = compileQuery('schema', [amount], { filter: root, select: ['amount'], sort: 'amount', direction: 'desc', limit: '100' });
    expect(spec.where).toEqual({ op: 'and', args: [{ op: 'not', args: [{ op: 'or', args: [{ op: 'isNull', field: 'amount' }, { op: 'gt', field: 'amount', value: '12.3' }] }] }] });
    expect(spec.orderBy).toEqual([{ field: 'amount', direction: 'desc' }]);
    root.children = [newGroup()];
    expect(() => compileQuery('schema', [amount], { filter: root, select: ['amount'], sort: '', direction: 'asc', limit: '10' })).toThrow('不能为空');
  });

  it('requires explicit projection and rejects invalid limits, fields and text sorts', () => {
    const editor = { filter: newGroup(), select: [] as string[], sort: '', direction: 'asc' as const, limit: '25' };
    expect(() => compileQuery('s', [amount], editor)).toThrow('至少勾选');
    expect(() => compileQuery('s', [amount], { ...editor, select: ['amount'], limit: '1.5' })).toThrow('整数');
    expect(() => compileQuery('s', [amount], { ...editor, select: ['amount'], limit: '101' })).toThrow('整数');
    expect(() => compileQuery('s', [amount], { ...editor, select: ['removed'] })).toThrow('失效');
    expect(() => compileQuery('s', [{ ...title, dataType: 'text' }], { ...editor, select: ['title'], sort: 'title' })).toThrow('不支持排序');
  });
});

describe('instance form serialization', () => {
  it('preserves long bodies and validates decoded UTF-8 byte limits independently from text', () => {
    const property: Property = { id: 'body', name: '正文', dataType: 'longtext', nullable: true };
    const body = '知识库正文\n'.repeat(10000);
    expect(parseFieldValue(property, body)).toBe(body);
    expect(() => parseFieldValue({ ...property, dataType: 'text' }, body)).toThrow('65535');
    const limit = 4 * 1024 * 1024;
    const atLimit = '知'.repeat(Math.floor(limit / 3)) + 'a';
    expect(parseFieldValue(property, atLimit)).toBe(atLimit);
    expect(() => parseFieldValue(property, `${atLimit}a`)).toThrow('4 MiB');
    const original = { body };
    const drafts = initialDrafts([property], original);
    expect(drafts.body.text).toBe(body);
    drafts.body = { mode: 'value', text: `${body}结尾` };
    expect(buildWriteValues([property], drafts, original)).toEqual({ body: `${body}结尾` });
    expect(availableOperators(property).map((operator) => operator.value)).toContain('startsWith');
    const editor = { filter: newGroup(), select: ['body'], sort: 'body', direction: 'asc' as const, limit: '1' };
    expect(() => compileQuery('s', [property], editor)).toThrow('不支持排序');
  });

  it('identifies the same write intent independently of property display order', () => {
    expect(writeIntentSignature({ amount: '9007199254740993.000001', title: '' })).toBe(writeIntentSignature({ title: '', amount: '9007199254740993.000001' }));
    expect(writeIntentSignature({ title: null })).not.toBe(writeIntentSignature({ title: '' }));
    expect(writeIntentSignature({})).not.toBe(writeIntentSignature({ title: null }));
  });

  it('preserves RFC 3339 microseconds and timezone verbatim while rejecting invalid dates', () => {
    const input = '2026-11-06T14:30:00.123456+08:00';
    expect(parseFieldValue(timestamp, input)).toBe(input);
    expect(() => parseFieldValue(timestamp, '2026-02-30T14:30:00Z')).toThrow('时间无效');
    expect(() => parseFieldValue(timestamp, '2026-11-06T14:30:00.1234567Z')).toThrow('微秒');
    expect(() => parseFieldValue(timestamp, '2026-11-06T14:30')).toThrow('时区');
  });

  it('submits only changed fields and distinguishes omitted, NULL and empty string', () => {
    const properties = [title, amount, integer, timestamp];
    const original = { title: '示例', amount: '9007199254740993.000001', integer: null, time: '2026-11-06T14:30:00.123456+08:00' };
    const drafts = initialDrafts(properties, original);
    expect(buildWriteValues(properties, drafts, original)).toEqual({});
    drafts.title = { mode: 'value', text: '' };
    drafts.amount = { mode: 'null', text: '' };
    drafts.time = { mode: 'value', text: original.time };
    expect(buildWriteValues(properties, drafts, original)).toEqual({ title: '', amount: null });
    expect(valueText(undefined)).toBe('未返回');
    expect(valueText(null)).toBe('空值 NULL');
    expect(valueText('')).toBe('空字符串');
    expect(valueText('9007199254740993.000001')).toBe('9007199254740993.000001');
  });

  it('omits defaults and read-only fields on create, and blocks missing required fields', () => {
    const readOnly: Property = { ...title, id: 'readOnly', readOnly: true, default: 'generated' };
    const withDefault: Property = { ...integer, default: '42' };
    const properties = [title, withDefault, readOnly];
    const drafts = initialDrafts(properties);
    drafts.title = { mode: 'value', text: '测试' };
    drafts.readOnly = { mode: 'value', text: 'should not submit' };
    expect(buildWriteValues(properties, drafts)).toEqual({ title: '测试' });
    drafts.title = { mode: 'omit', text: '' };
    expect(() => buildWriteValues(properties, drafts)).toThrow('需要显式填写');
  });
});
