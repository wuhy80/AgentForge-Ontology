import { useEffect, useRef, useState } from 'react';
import { ArrowDownWideNarrow, ArrowRight, Check, ChevronRight, Columns3, Database, Filter, Layers, LoaderCircle, Play, Plus, Rows3, Square } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, Organization, QueryResult, QuerySpec } from '../../lib/types';
import { InstanceDialog } from './InstanceDialog';
import { useSchemaWrites } from './useSchemaWrites';
import { RelatedInstances } from './RelatedInstances';
import { aggregateFields, compileQuery, errorMessage, newGroup, propertyValueText } from './query';
import type { QueryEditor } from './query';
import { GroupEditor } from './QueryFilterEditor';
import { VisualRelations } from './VisualRelations';
import { RowResults } from './RowResults';
import { AggregateEditor } from './AggregateEditor';
import { AggregateResults } from './AggregateResults';
import { compileRowQuery, rowQueryModel } from './ontologySql';
import type { VisualRowsDraft } from './visualRows';
import { loadRowContext } from './visualRows';
import type { FilterNode } from './query';
import './query.css';

const clients = new WeakMap<ApiClient, number>();
let nextClient = 0;
function clientKey(api: ApiClient) {
  if (!clients.has(api)) clients.set(api, ++nextClient);
  return clients.get(api);
}

export type VisualDraft = { schema: DefinitionRecord; editor: QueryEditor; association?: VisualRowsDraft };
type Props = { api: ApiClient; categoryId: string; schemas: DefinitionRecord[]; categories?: Organization[]; initialSchemaId?: string; selectionRequest?: number; onModel: () => void; initialEditor?: QueryEditor; initialRows?: VisualRowsDraft; authorizationFailure?: { sequence: number; message: string }; onDraftChange?: (draft: VisualDraft) => void };

export function VisualQueryPage({ api, categoryId, schemas, categories = [], initialSchemaId, selectionRequest, onModel, initialEditor, initialRows, authorizationFailure, onDraftChange }: Props) {
  const [selectedId, setSelectedId] = useState(initialSchemaId ?? '');
  const [pendingSchema, setPendingSchema] = useState<string>();
  const currentDraft = useRef<VisualDraft | undefined>(undefined);
  const rememberDraft = (draft: VisualDraft) => { currentDraft.current = draft; onDraftChange?.(draft); };
  const intentSession = useRef({ api, schemas: new Map<string, Map<string, string>>() });
  if (intentSession.current.api !== api) {
    intentSession.current = { api, schemas: new Map() };
  }
  const availableSchemas = categoryId ? schemas.filter((item) => item.definition.kind === 'schema' && item.definition.categoryId === categoryId) : [];
  useEffect(() => {
    setSelectedId('');
    setPendingSchema(undefined);
  }, [categoryId, initialSchemaId, selectionRequest]);
  const selected = availableSchemas.find((item) => item.definition.id === selectedId)
    ?? availableSchemas.find((item) => item.definition.id === initialSchemaId)
    ?? availableSchemas[0];
  const supported = ['schema_2_db', 'db_2_schema'].includes(selected?.definition.schema?.mode ?? '') && selected?.definition.schema?.storageLayout === 'single';
  let createIntents: Map<string, string> | undefined;
  if (selected) {
    createIntents = intentSession.current.schemas.get(selected.definition.id);
    if (!createIntents) {
      createIntents = new Map();
      intentSession.current.schemas.set(selected.definition.id, createIntents);
    }
  }
  return <div className="query-page">
    <div className="query-page-heading"><div><div className="query-eyebrow">DATA EXPLORER</div><h1>探索业务数据</h1><p className="muted">从生效模型出发，构建查询并管理真实业务实例。</p></div>
      <span className="badge query-live-badge"><span />{selected?.definition.schema?.storageBinding ? '映射数据源' : 'MySQL 数据服务'}</span>
    </div>
    {!availableSchemas.length ? <div className="card empty-state query-first-empty"><Database size={38} /><h2>{categoryId ? '当前分类暂无生效模型' : '请先选择当前分类'}</h2><p className="muted">{categoryId ? '在当前分类发布 Schema 后，可在这里选择字段、筛选数据和新建实例。' : '从顶部选择业务分类，再探索该分类中的数据。'}</p><button type="button" className="button primary" onClick={onModel}>前往模型管理<ArrowRight size={16} /></button></div>
      : <>
        <div className="card query-source"><div className="query-source-icon"><Layers size={22} /></div><div className="field query-schema-select"><label htmlFor="query-schema">查询模型</label>
          <select id="query-schema" value={selected?.definition.id ?? ''} onChange={(event) => {
            if (currentDraft.current?.association && event.target.value !== selected?.definition.id) setPendingSchema(event.target.value);
            else setSelectedId(event.target.value);
          }}>
            {availableSchemas.map((item) => {
              const category = categories.find((entry) => entry.id === item.definition.categoryId);
              return <option key={item.definition.id} value={item.definition.id}>{category ? `${category.name} / ` : ''}{item.definition.name}</option>;
            })}
          </select></div><div className="query-source-description"><span className="badge">当前生效</span><p className="muted">{selected?.definition.description || '单模型查询 · 字段与条件使用稳定标识绑定'}</p></div>
          <button type="button" className="button subtle" onClick={onModel}>查看模型<ChevronRight size={16} /></button>
        </div>
        {pendingSchema && <div className="notice" role="status"><p>更换查询起点将清空当前关联路径、返回字段、条件和结果。</p><button type="button" className="button" onClick={() => { setSelectedId(pendingSchema); setPendingSchema(undefined); }}>确认更换起点</button><button type="button" className="button subtle" onClick={() => setPendingSchema(undefined)}>保留当前查询</button></div>}
        {selected && supported ? <QueryWorkspace key={`${categoryId}:${clientKey(api)}:${selected.definition.id}:${selected.etag}`} api={api} schema={selected} categories={categories} createIntents={createIntents!} initialEditor={selected.definition.id === initialSchemaId ? initialEditor : undefined} initialRows={selected.definition.id === initialSchemaId ? initialRows : undefined} authorizationFailure={authorizationFailure} onDraftChange={rememberDraft} />
          : <div className="card empty-state"><Database size={30} /><h2>此模型暂不支持数据操作</h2><p className="muted">当前数据服务支持 schema_2_db 与 single 布局。请在模型管理中查看定义。</p><button type="button" className="button" onClick={onModel}>查看模型</button></div>}
      </>}
  </div>;
}

type Executed = { result: QueryResult; spec: QuerySpec; page: number };
function QueryWorkspace({ api, schema, categories, createIntents, initialEditor, initialRows, authorizationFailure, onDraftChange }: { api: ApiClient; schema: DefinitionRecord; categories: Organization[]; createIntents: Map<string, string>; initialEditor?: QueryEditor; initialRows?: VisualRowsDraft; authorizationFailure?: { sequence: number; message: string }; onDraftChange?: (draft: VisualDraft) => void }) {
  const [association, setAssociation] = useState(initialRows);
  const [blocked, setBlocked] = useState(false);
  const [removePending, setRemovePending] = useState(false);
  const [aggregateAvailable, setAggregateAvailable] = useState(false);
  const [aggregateCapabilityError, setAggregateCapabilityError] = useState('');
  const properties = association ? rowQueryModel(association.rows, association.context, true).fields : schema.definition.schema!.properties;
  const binding = schema.definition.schema!.storageBinding;
  const readOnly = !!binding || !!association;
  const writes = useSchemaWrites(api, schema.definition.id, !!binding);
  const filterProperties = binding && !association ? properties.filter((property) => {
    const mapping = property.mapping;
    return mapping?.dataSourceId === binding.dataSourceId && (!mapping.table || mapping.table === binding.table) && (!mapping.namespace || mapping.namespace === binding.namespace);
  }) : properties;
  const [editor, setEditor] = useState<QueryEditor>(() => initialRows?.editor ?? initialEditor ?? ({ filter: newGroup(), select: [], sort: '', direction: 'asc', limit: '25' }));
  const draftCallback = useRef(onDraftChange);
  draftCallback.current = onDraftChange;
  useEffect(() => { draftCallback.current?.({ schema, editor, association: association ? { ...association, editor } : undefined }); }, [schema, editor, association]);
  const [executed, setExecuted] = useState<Executed>();
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [dialog, setDialog] = useState<{ id?: string }>();
  const [relatedId, setRelatedId] = useState<string>();
  const request = useRef<{ controller: AbortController; sequence: number } | undefined>(undefined);
  const sequence = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    if (typeof api.dataCapabilities !== 'function') return;
    api.dataCapabilities(controller.signal).then((value) => { if (!controller.signal.aborted) setAggregateAvailable(!!value.aggregates); }).catch((cause: unknown) => { if (!controller.signal.aborted) setAggregateCapabilityError(errorMessage(cause)); });
    return () => controller.abort();
  }, [api]);

  useEffect(() => () => {
    sequence.current += 1;
    request.current?.controller.abort();
  }, []);
  useEffect(() => { if (authorizationFailure) forbidden(authorizationFailure.message); }, [authorizationFailure]);

  function cancel(message = '本次查询已取消，可以修改条件后重新运行。') {
    sequence.current += 1;
    request.current?.controller.abort();
    request.current = undefined;
    setBusy(false);
    setNotice(message);
  }

  function change(patch: Partial<QueryEditor>) {
    if (request.current) cancel('查询配置已修改，请重新运行。');
    setEditor((previous) => ({ ...previous, ...patch }));
    if (executed) setStale(true);
    setError('');
    setRemovePending(false);
  }

  function applyAssociation(value: VisualRowsDraft) {
    change(value.editor); setAssociation(value); setDialog(undefined); setRelatedId(undefined);
    setNotice('已添加关联，原字段与条件保留。请按模型角色选择关联字段，再运行查询。');
  }

  function renameRole(id: string, alias: string) {
    if (!association) return;
    // Keep incomplete role input editable; validation happens before execution.
    change({}); setAssociation({ ...association, rows: { ...association.rows, nodes: association.rows.nodes.map((node) => node.id === id ? { ...node, alias } : node) } });
  }

  function removeAssociation(confirmed = false) {
    if (!association) return;
    const last = association.rows.nodes.at(-1)!;
    const used = (node: FilterNode): boolean => node.kind === 'condition' ? node.field.startsWith(`${last.id}.`) : node.children.some(used);
    if (editor.select.some((id) => id.startsWith(`${last.id}.`)) || editor.sort.startsWith(`${last.id}.`) || used(editor.filter)) {
      setError(`请先移除“${last.alias}”角色的返回字段、筛选条件及排序，再移除关联。`); return;
    }
    if (!confirmed) { setRemovePending(true); return; }
    if (association.rows.joins.length > 1) {
      change({}); setAssociation({ ...association, rows: { nodes: association.rows.nodes.slice(0, -1), joins: association.rows.joins.slice(0, -1) } });
    } else {
      const reference = (id: string) => id.startsWith('n0.') ? id.slice(3) : id;
      const visit = (node: FilterNode): FilterNode => node.kind === 'condition' ? { ...node, field: reference(node.field) } : { ...node, children: node.children.map(visit) };
      change({ select: editor.select.map(reference), sort: reference(editor.sort), filter: { ...editor.filter, children: editor.filter.children.map(visit) } });
      setAssociation(undefined);
    }
    setRemovePending(false); setNotice('已移除关联，请重新运行查询。');
  }

  async function execute(spec: QuerySpec, page: number) {
    request.current?.controller.abort();
    const controller = new AbortController();
    const current = ++sequence.current;
    request.current = { controller, sequence: current };
    setBusy(true);
    setError('');
    setNotice('');
    if (executed && page === 1) setStale(true);
    try {
      const result = await api.query(spec, controller.signal);
      if (controller.signal.aborted || sequence.current !== current) return;
      if (!!spec.rows !== !!result.rowMode) throw new Error('查询结果形态与请求不一致，请检查服务后重试');
      if (!!spec.aggregate !== !!result.aggregateMode) throw new Error('汇总结果形态与请求不一致，请检查服务后重试');
      // Preserve exactly the submitted query; subsequent pages only replace its cursor.
      const base: QuerySpec = { ...spec, page: { limit: spec.page.limit } };
      setExecuted({ result, spec: base, page });
      setStale(false);
    } catch (reason) {
      if (!controller.signal.aborted && sequence.current === current) {
        setError(errorMessage(reason));
        if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) {
          setExecuted(undefined);
          setStale(false);
          if (association) setBlocked(true);
        }
        if (reason instanceof ApiError && reason.code === 'invalid_cursor') {
          setStale(true);
          setNotice('分页游标已失效，请重新运行查询。');
        }
      }
    } finally {
      if (sequence.current === current) {
        request.current = undefined;
        setBusy(false);
      }
    }
  }

  function run() {
    if (editor.aggregate && (!aggregateAvailable || binding || association)) { setError('当前服务或模型尚不支持分组汇总，请使用默认平台 MySQL 单模型'); return; }
    try { void execute(association ? compileRowQuery(association.rows, association.context, editor) : compileQuery(schema.definition.id, properties, editor), 1); }
    catch (reason) { setError(errorMessage(reason)); }
  }

  function nextPage() {
    if (!executed || stale || busy || !executed.result.hasMore || !executed.result.nextCursor) return;
    void execute({ ...executed.spec, page: { ...executed.spec.page, cursor: executed.result.nextCursor } }, executed.page + 1);
  }

  function changed(message: string) {
    if (request.current) cancel();
    if (executed) setStale(true);
    setNotice(`${message}。请重新运行查询获取最新结果。`);
  }

  function forbidden(message: string) {
    if (request.current) cancel('');
    setExecuted(undefined);
    setStale(false);
    setDialog(undefined);
    setRelatedId(undefined);
    setEditor({ filter: newGroup(), select: [], sort: '', direction: 'asc', limit: '25' });
    setError(message);
    setNotice('操作被授权拒绝，已清除先前读取的结果。');
    if (association) setBlocked(true);
  }

  async function reloadAuthorization() {
    if (!association) return;
    request.current?.controller.abort();
    const controller = new AbortController(), current = ++sequence.current;
    request.current = { controller, sequence: current };
    try {
      const context = await loadRowContext(api, association.rows, categories, controller.signal);
      if (controller.signal.aborted || current !== sequence.current) return;
      setAssociation({ ...association, context }); setBlocked(false); setError('');
    } catch (cause) { if (!controller.signal.aborted && current === sequence.current) setError(errorMessage(cause)); }
    finally { if (current === sequence.current) request.current = undefined; }
  }

  const selectedCount = editor.aggregate ? editor.aggregate.measures.length : editor.select.length;
  const sortProperties = editor.aggregate ? aggregateFields(editor.aggregate, properties) : filterProperties;
  const result = executed?.result;
  const rowCount = result?.aggregateMode ? result.aggregateRows?.length ?? 0 : result?.rowMode ? result.rows?.length ?? 0 : result?.data.length ?? 0;
  if (blocked) return <div className="notice notice-error" role="alert">{error || '查询权限已改变，已清除结果及关联字段候选。'}<button className="button" type="button" onClick={() => void reloadAuthorization()}>重新读取查询权限</button></div>;
  return <>
    <div className="card query-builder">
      <div className="query-section-heading"><div><Filter size={18} /><h2>构建查询</h2></div><span className="muted">更改配置后，点击运行应用</span></div>
      <div className="query-builder-content">
        <label className="query-result-mode">结果类型<select aria-label="查询结果类型" value={editor.aggregate ? 'aggregate' : 'detail'} onChange={(event) => { change({ aggregate: event.target.value === 'aggregate' ? { groupBy: [], measures: [{ id: 'm0', op: 'count', name: '记录数量' }] } : undefined, sort: '' }); setDialog(undefined); setRelatedId(undefined); }}><option value="detail">明细记录</option><option value="aggregate" disabled={!aggregateAvailable || !!binding || !!association}>分组汇总</option></select></label>
        {(!aggregateAvailable || binding || association) && <p className="muted">分组汇总需要服务端开放能力，并使用默认平台 MySQL 单模型。</p>}
        {aggregateCapabilityError && <p className="notice notice-error">汇总能力读取失败：{aggregateCapabilityError}</p>}
        {!editor.aggregate && <VisualRelations api={api} schema={schema} categories={categories} editor={editor} rows={association?.rows} context={association?.context} onApply={applyAssociation} onRename={renameRole} onRemove={() => removeAssociation()} onForbidden={forbidden} />}
        {removePending && <div className="notice" role="status"><p>{association?.rows.joins.length === 1 ? '移除后返回单模型对象列表，同一对象只出现一次。现有字段和条件将保留。' : '移除最后一个关联后，仅返回剩余路径的匹配行。现有字段和条件将保留。'}</p><button type="button" className="button" onClick={() => removeAssociation(true)}>确认移除关联</button><button type="button" className="button subtle" onClick={() => setRemovePending(false)}>保留关联</button></div>}
        <section className="query-filter-section"><div className="query-step-title"><span>01</span><h3>筛选条件</h3></div>
          {filterProperties.length > 0 ? <GroupEditor group={editor.filter} properties={filterProperties} onChange={(filter) => change({ filter })} /> : <p className="muted">主表尚未映射可筛选属性。</p>}
          {readOnly && <p className="muted">{association ? '筛选作用于全部匹配关系行；字段名称包含模型角色。' : '筛选和排序使用主表属性；其他来源的属性可选择返回。'}</p>}
        </section>
        <div className="query-settings-grid">{editor.aggregate ? <AggregateEditor aggregate={editor.aggregate} properties={properties} onChange={(aggregate) => change({ aggregate, ...(editor.sort && !aggregateFields(aggregate, properties).some((field) => field.id === editor.sort) ? { sort: '' } : {}) })} /> : <section><div className="query-step-title"><span>02</span><h3>返回字段</h3><small className="muted">已选 {selectedCount} 项</small></div>
          <div className="query-column-actions"><button type="button" className="button subtle" onClick={() => change({ select: properties.map((property) => property.id) })}>全选</button><button type="button" className="button subtle" onClick={() => change({ select: [] })}>清空</button></div>
          <div className="query-columns">{properties.map((property) => <label className={`query-column ${editor.select.includes(property.id) ? 'query-column-selected' : ''}`} key={property.id}>
            <input type="checkbox" aria-label={property.name} checked={editor.select.includes(property.id)} onChange={(event) => change({ select: event.target.checked ? [...editor.select, property.id] : editor.select.filter((id) => id !== property.id) })} />
            <span>{property.name}</span><small className="muted">{property.dataType}</small>
          </label>)}</div>
        </section>}<section><div className="query-step-title"><span>03</span><h3>排序与分页</h3></div>
          <div className="query-sort-settings"><div className="field"><label htmlFor="query-sort">排序字段</label><select id="query-sort" value={editor.sort} onChange={(event) => change({ sort: event.target.value })}>
            <option value="">{editor.aggregate ? '分组属性（默认）' : association ? '全部模型的实例 ID（默认）' : '实例 ID（默认）'}</option>{sortProperties.filter((property) => property.dataType !== 'text' && property.dataType !== 'longtext').map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}
          </select></div><div className="form-grid"><div className="field"><label htmlFor="query-direction">排序方向</label><select id="query-direction" value={editor.direction} disabled={!editor.sort} onChange={(event) => change({ direction: event.target.value as 'asc' | 'desc' })}><option value="asc">升序</option><option value="desc">降序</option></select></div>
            <div className="field"><label htmlFor="query-limit">每页条数</label><input id="query-limit" type="number" min="1" max="100" step="1" value={editor.limit} onChange={(event) => change({ limit: event.target.value })} /></div></div>
            <p className="muted query-sort-hint"><ArrowDownWideNarrow size={14} />结果由服务端排序和分页，每页最多 100 条。</p>
          </div>
        </section></div>
      </div>
      <div className="query-run-bar"><span className="muted"><Columns3 size={15} />{selectedCount ? `${selectedCount} 个返回字段` : '请勾选需要返回的字段'}</span>
        <div className="query-actions">{busy && <button className="button" type="button" onClick={() => cancel()}><Square size={14} />取消查询</button>}
          <button className="button primary" type="button" onClick={run} disabled={busy || !selectedCount}>{busy ? <LoaderCircle className="query-spin" size={16} /> : <Play size={16} />}{busy ? '正在查询…' : '运行查询'}</button></div>
      </div>
    </div>
    {error && <div className="notice notice-error query-status" role="alert">{error}<span>请检查输入或权限后重试。</span></div>}
    {notice && <div className="notice query-status" role="status">{notice}</div>}
    <section className="card query-results" aria-label="查询结果" aria-busy={busy}>
      <div className="query-section-heading"><div><Rows3 size={18} /><h2>查询结果</h2>{result && <span className="badge">本页 {rowCount} 条</span>}</div>
        {editor.aggregate || result?.aggregateMode || association || !writes.allowed('create') ? <span className="muted">{editor.aggregate || result?.aggregateMode ? '分组汇总' : association ? '关联明细 · 只读结果' : '写入能力由当前权限与来源状态决定'}</span> : <button className="button" type="button" onClick={() => setDialog({})}><Plus size={16} />新建实例</button>}
      </div>
      {stale && result && <div className="query-stale" role="status"><span className="query-stale-dot" />结果已过期：配置或数据已改变，请重新运行查询。<span>续页已停用</span></div>}
      {!result ? <div className="empty-state query-result-empty"><div className="query-empty-icon"><Database size={27} /></div><h3>{busy ? '正在读取数据' : '准备好后，运行第一次查询'}</h3><p className="muted">{busy ? '你可以取消本次请求并继续调整条件。' : '勾选返回字段，按需添加条件。这里将展示服务端返回的真实结果。'}</p></div>
        : !rowCount ? <div className="empty-state query-result-empty"><Check size={30} /><h3>没有匹配的数据</h3><p className="muted">{association ? '可以调整关联路径或筛选条件后重新运行。' : '可以调整筛选条件后重新运行，或新建一个实例。'}</p></div>
          : result.aggregateMode ? <AggregateResults result={result} /> : result.rowMode ? <><p className="ontology-row-grain muted">每行对应一组匹配对象，同一对象可能出现多次。结果只读。</p><RowResults result={result} query={executed!.spec} /></>
          : <div className="table-wrap query-result-table"><table><thead><tr><th className="query-row-number">#</th><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}<small>{field.dataType}</small></th>)}<th className="query-row-action">操作</th></tr></thead>
            <tbody>{result.data.map((instance, index) => <tr key={instance.id}><td className="query-row-number">{index + 1}</td><td><button type="button" className="query-id-link" title={instance.id} onClick={() => setDialog({ id: instance.id })}>{instance.id}</button></td>
              {result.fields.map((field) => { const value = instance.values[field.id]; return <td key={field.id}><span className={value === undefined || value === null || value === '' ? 'query-cell-special' : 'query-cell-value'}>{propertyValueText(field, value)}</span></td>; })}
              <td className="query-row-action"><div className="query-instance-actions"><button type="button" className="button subtle" onClick={() => setDialog({ id: instance.id })}>查看<ChevronRight size={14} /></button><button type="button" className="button subtle" onClick={() => setRelatedId(instance.id)}>关联数据</button></div></td></tr>)}</tbody>
          </table></div>}
      {executed && <div className="query-result-footer"><span className="muted">第 {executed.page} 页 · {executed.result.hasMore ? '后面还有数据' : '已到结果末尾'}</span>
        <button className="button" type="button" disabled={busy || stale || !executed.result.hasMore || !executed.result.nextCursor} onClick={nextPage}>下一页<ArrowRight size={15} /></button></div>}
    </section>
    {dialog && <InstanceDialog key={dialog.id ?? 'new'} api={api} schema={schema} instanceId={dialog.id} createIntents={createIntents} onClose={() => setDialog(undefined)} onChanged={changed} onForbidden={forbidden} />}
    {relatedId && <RelatedInstances key={relatedId} api={api} schema={schema} instanceId={relatedId} onClose={() => setRelatedId(undefined)} onForbidden={forbidden} />}
  </>;
}
