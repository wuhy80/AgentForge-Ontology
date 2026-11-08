import type { Expression, JsonValue, Property, SavedQueryTemplate } from '../../lib/types';
import { compileQuery, parseFieldValue, type FilterNode, type QueryEditor } from './query';

// Visual comparison inputs become named parameters. Their runtime values never
// enter the persisted request, including values typed before opening Save.
export function visualSavedTemplate(schemaId: string, properties: Property[], editor: QueryEditor): SavedQueryTemplate {
  const safeText = (property?: Property) => property?.dataType === 'boolean' ? 'true' : ['int64', 'decimal'].includes(property?.dataType ?? '') ? '0' : property?.dataType === 'uuid' ? '00000000-0000-0000-0000-000000000000' : property?.dataType === 'timestamp' ? '2026-01-01T00:00:00Z' : '';
  const visit = (node: FilterNode): FilterNode => node.kind === 'group' ? { ...node, children: node.children.map(visit) } : { ...node, raw: safeText(properties.find((field) => field.id === node.field)) };
  const query = compileQuery(schemaId, properties, { ...editor, filter: { ...editor.filter, children: editor.filter.children.map(visit) } });
  const parameters: SavedQueryTemplate['parameters'] = [];
  function parameterize(node: Expression): Expression {
    if (node.args) return { ...node, args: node.args.map(parameterize) };
    if (!Object.hasOwn(node, 'value')) return node;
    const field = properties.find((property) => property.id === node.field)!;
    const name = `条件${parameters.length + 1}`;
    parameters.push({ name, dataType: field.dataType, nullable: ['in', 'notIn'].includes(node.op) && !!field.nullable, ...(field.dataType === 'decimal' ? { precision: field.precision, scale: field.scale } : {}), ...(field.enumId ? { enumId: field.enumId } : {}), ...(['in', 'notIn'].includes(node.op) ? { array: true } : {}) });
    const { value: _value, ...predicate } = node;
    return { ...predicate, parameter: name };
  }
  if (query.where) query.where = parameterize(query.where);
  if (parameters.length > 64) throw new Error('保存查询最多支持 64 个参数，请减少条件');
  return { query, parameters };
}

export function savedParameterValue(property: Property & { array?: boolean }, text: string): JsonValue {
  if (!property.array) return parseFieldValue(property, text);
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  if (lines.length > 100) throw new Error(`${property.name}：集合最多包含 100 个值`);
  return lines.map((line) => parseFieldValue(property, line));
}
