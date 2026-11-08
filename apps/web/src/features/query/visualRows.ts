import type { ApiClient } from '../../lib/api';
import type { Expression, Organization, QueryPathCandidate, QuerySpec, RowQuery } from '../../lib/types';
import type { FilterNode, QueryEditor } from './query';
import type { RowQueryContext } from './ontologySql';
import { rowQueryModel } from './ontologySql';

export type VisualRowsDraft = { rows: RowQuery; context: RowQueryContext; editor: QueryEditor };

export async function loadRowContext(api: ApiClient, rows: RowQuery, categories: Organization[], signal?: AbortSignal): Promise<RowQueryContext> {
  const schemaIds = [...new Set(rows.nodes.map((node) => node.schemaId))];
  const linkIds = [...new Set(rows.joins.map((join) => join.linkId))];
  const [models, definitions] = await Promise.all([
    Promise.all(schemaIds.map(async (schemaId) => {
      const response = await api.queryContext({ schemaId, limit: 1 }, signal);
      const model = response.models.find((item) => item.schemaId === schemaId);
      if (!model) throw new Error('关联模型不可查询，请检查授权后重试');
      return model;
    })),
    Promise.all(linkIds.map((id) => api.getCurrent(id, signal))),
  ]);
  const links = definitions.map(({ definition, status }) => {
    const category = categories.find((item) => item.id === definition.categoryId)?.name;
    if (status !== 'active' || definition.kind !== 'link' || !definition.link || !category) throw new Error('关联定义或分类已失效，请重新读取');
    return { id: definition.id, category, name: definition.name, sourceSchemaId: definition.link.sourceSchemaId, targetSchemaId: definition.link.targetSchemaId };
  });
  const context = { models, links };
  rowQueryModel(rows, context);
  return context;
}

export function appendVisualPath(current: { rows?: RowQuery; editor: QueryEditor }, candidate: QueryPathCandidate, context: RowQueryContext): VisualRowsDraft {
  const root = context.models[0];
  const rows: RowQuery = current.rows ? structuredClone(current.rows) : { nodes: [{ id: 'n0', schemaId: root.schemaId, alias: root.name }], joins: [] };
  if (!candidate.steps.length || rows.joins.length + candidate.steps.length > 4) throw new Error('关联路径超过 4 条 Link 的范围');
  const models = [...context.models], links = [...context.links];
  for (const step of candidate.steps) {
    const from = rows.nodes.find((node) => node.alias === step.fromAlias);
    if (!from || rows.nodes.some((node) => node.alias.normalize('NFC').toLowerCase() === step.toAlias.normalize('NFC').toLowerCase())) throw new Error('路径的起点或模型角色无效，请重新查找');
    const id = `n${rows.nodes.length}`;
    rows.nodes.push({ id, schemaId: step.target.schemaId, alias: step.toAlias });
    rows.joins.push({ from: from.id, to: id, linkId: step.linkId, direction: step.direction });
    if (!models.some((model) => model.schemaId === step.target.schemaId)) models.push(step.target);
    const link = { id: step.linkId, name: step.name, category: step.category, sourceSchemaId: step.direction === 'out' ? from.schemaId : step.target.schemaId, targetSchemaId: step.direction === 'out' ? step.target.schemaId : from.schemaId };
    const existing = links.find((item) => item.id === link.id);
    if (existing && (existing.name !== link.name || existing.category !== link.category || existing.sourceSchemaId !== link.sourceSchemaId || existing.targetSchemaId !== link.targetSchemaId)) throw new Error('关系定义已改变，请重新查找路径');
    if (!existing) links.push(link);
  }
  const reference = (id: string) => current.rows || !id ? id : `n0.${id}`;
  const visit = (node: FilterNode): FilterNode => node.kind === 'condition' ? { ...node, field: reference(node.field) } : { ...node, children: node.children.map(visit) };
  const editor: QueryEditor = { ...current.editor, select: current.editor.select.map(reference), sort: reference(current.editor.sort), filter: { ...current.editor.filter, children: current.editor.filter.children.map(visit) } };
  const result = { rows, editor, context: { models, links } };
  rowQueryModel(rows, result.context);
  return result;
}

function fieldBindings(query: QuerySpec) {
  const fields: string[] = [...query.select, ...(query.orderBy ?? []).map((order) => order.field)];
  function visit(expression?: Expression) {
    if (!expression) return;
    if (expression.field) fields.push(expression.field);
    expression.args?.forEach(visit);
  }
  visit(query.where);
  return fields;
}

// Name-based SQL is only an editing/validation format. Never replace an existing
// stable binding with a different definition that reused its business name.
export function assertVisualBindings(expected: QuerySpec, actual: QuerySpec) {
  const scope = (query: QuerySpec) => query.rows ? [query.rows.nodes.map((node) => [node.id, node.schemaId, node.alias]), query.rows.joins.map((join) => [join.from, join.to, join.linkId, join.direction])] : undefined;
  if (expected.from.schemaId !== actual.from.schemaId || JSON.stringify(scope(expected)) !== JSON.stringify(scope(actual))
    || JSON.stringify(fieldBindings(expected)) !== JSON.stringify(fieldBindings(actual))) throw new Error('模型、关系或字段绑定已改变，原查询已保留，请重新读取');
}
