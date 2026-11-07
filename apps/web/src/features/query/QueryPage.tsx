import { useEffect, useRef, useState } from 'react';
import { ArrowDownWideNarrow, ArrowRight, Braces, Check, ChevronRight, Columns3, Database, Filter, Layers, LoaderCircle, Play, Plus, Rows3, Square, Trash2, X } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, Organization, Property, QueryResult, QuerySpec } from '../../lib/types';
import { InstanceDialog, ValueInput } from './InstanceDialog';
import { availableOperators, compileQuery, errorMessage, newCondition, newGroup, valueText } from './query';
import type { FilterCondition, FilterGroup, FilterNode, QueryEditor } from './query';
import './query.css';

const clients = new WeakMap<ApiClient, number>();
let nextClient = 0;
function clientKey(api: ApiClient) {
  if (!clients.has(api)) clients.set(api, ++nextClient);
  return clients.get(api);
}

type Props = { api: ApiClient; categoryId: string; schemas: DefinitionRecord[]; categories?: Organization[]; initialSchemaId?: string; selectionRequest?: number; onModel: () => void };

export function QueryPage({ api, categoryId, schemas, categories = [], initialSchemaId, selectionRequest, onModel }: Props) {
  const [selectedId, setSelectedId] = useState(initialSchemaId ?? '');
  const intentSession = useRef({ api, schemas: new Map<string, Map<string, string>>() });
  if (intentSession.current.api !== api) {
    intentSession.current = { api, schemas: new Map() };
  }
  const availableSchemas = categoryId ? schemas.filter((item) => item.definition.kind === 'schema' && item.definition.categoryId === categoryId) : [];
  useEffect(() => {
    setSelectedId('');
  }, [categoryId, initialSchemaId, selectionRequest]);
  const selected = availableSchemas.find((item) => item.definition.id === selectedId)
    ?? availableSchemas.find((item) => item.definition.id === initialSchemaId)
    ?? availableSchemas[0];
  const supported = selected?.definition.schema?.mode === 'schema_2_db' && selected.definition.schema.storageLayout === 'single';
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
      <span className="badge query-live-badge"><span />MySQL 数据服务</span>
    </div>
    {!availableSchemas.length ? <div className="card empty-state query-first-empty"><Database size={38} /><h2>{categoryId ? '当前分类暂无生效模型' : '请先选择当前分类'}</h2><p className="muted">{categoryId ? '在当前分类发布 Schema 后，可在这里选择字段、筛选数据和新建实例。' : '从顶部选择业务分类，再探索该分类中的数据。'}</p><button type="button" className="button primary" onClick={onModel}>前往模型管理<ArrowRight size={16} /></button></div>
      : <>
        <div className="card query-source"><div className="query-source-icon"><Layers size={22} /></div><div className="field query-schema-select"><label htmlFor="query-schema">查询模型</label>
          <select id="query-schema" value={selected?.definition.id ?? ''} onChange={(event) => setSelectedId(event.target.value)}>
            {availableSchemas.map((item) => {
              const category = categories.find((entry) => entry.id === item.definition.categoryId);
              return <option key={item.definition.id} value={item.definition.id}>{category ? `${category.name} / ` : ''}{item.definition.name}</option>;
            })}
          </select></div><div className="query-source-description"><span className="badge">当前生效</span><p className="muted">{selected?.definition.description || '单模型查询 · 字段与条件使用稳定标识绑定'}</p></div>
          <button type="button" className="button subtle" onClick={onModel}>查看模型<ChevronRight size={16} /></button>
        </div>
        {selected && supported ? <QueryWorkspace key={`${categoryId}:${clientKey(api)}:${selected.definition.id}:${selected.etag}`} api={api} schema={selected} createIntents={createIntents!} />
          : <div className="card empty-state"><Database size={30} /><h2>此模型暂不支持数据操作</h2><p className="muted">当前数据服务支持 schema_2_db 与 single 布局。请在模型管理中查看定义。</p><button type="button" className="button" onClick={onModel}>查看模型</button></div>}
      </>}
  </div>;
}

function ConditionEditor({ condition, properties, onChange, onRemove }: {
  condition: FilterCondition; properties: Property[]; onChange: (value: FilterCondition) => void; onRemove: () => void;
}) {
  const property = properties.find((item) => item.id === condition.field);
  const noValue = condition.op === 'isNull' || condition.op === 'isNotNull';
  const isSet = condition.op === 'in' || condition.op === 'notIn';
  return <div className={`query-condition ${isSet ? 'query-condition-set' : ''}`}>
    <select aria-label="筛选字段" value={condition.field} onChange={(event) => {
      const next = properties.find((item) => item.id === event.target.value);
      const validOp = availableOperators(next).some((item) => item.value === condition.op) ? condition.op : 'eq';
      onChange({ ...condition, field: event.target.value, op: validOp, raw: next?.dataType === 'boolean' ? 'true' : '' });
    }}>{properties.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}</select>
    <select aria-label="筛选运算符" value={condition.op} onChange={(event) => onChange({ ...condition, op: event.target.value })}>
      {availableOperators(property).map((item) => <option value={item.value} key={item.value}>{item.label}</option>)}
    </select>
    <div className="query-condition-input">{noValue ? <span className="muted query-no-value">无需填写比较值</span>
      : isSet ? <><textarea aria-label="集合值，每行一个" rows={3} value={condition.raw} onChange={(event) => onChange({ ...condition, raw: event.target.value })} placeholder="每行一个值，最多 100 个" />
        <small className="muted">每行一个值；字符串保留空格，空行表示空字符串。</small></>
        : property && <><label className="query-sr-only" htmlFor={`condition-${condition.id}`}>比较值</label><ValueInput property={property} id={`condition-${condition.id}`} value={condition.raw} onChange={(raw) => onChange({ ...condition, raw })} /></>}
    </div>
    <button type="button" className="button subtle query-icon-button" aria-label="移除此条件" title="移除此条件" onClick={onRemove}><X size={16} /></button>
  </div>;
}

function GroupEditor({ group, properties, onChange, onRemove, depth = 0 }: {
  group: FilterGroup; properties: Property[]; onChange: (value: FilterGroup) => void; onRemove?: () => void; depth?: number;
}) {
  function replace(value: FilterNode) { onChange({ ...group, children: group.children.map((child) => child.id === value.id ? value : child) }); }
  function remove(id: string) { onChange({ ...group, children: group.children.filter((child) => child.id !== id) }); }
  return <div className={`query-filter-group ${depth ? 'query-filter-group-nested' : ''}`}>
    <div className="query-group-heading"><span className="muted">满足</span><select aria-label={depth ? '子条件组逻辑' : '条件组逻辑'} value={group.join} onChange={(event) => onChange({ ...group, join: event.target.value as 'and' | 'or' })}>
      <option value="and">全部条件 AND</option><option value="or">任意条件 OR</option>
    </select><label className="query-check"><input type="checkbox" checked={group.negated} onChange={(event) => onChange({ ...group, negated: event.target.checked })} />取反 NOT</label>
      {onRemove && <button type="button" className="button subtle query-icon-button" aria-label="移除此条件组" onClick={onRemove}><Trash2 size={15} /></button>}
    </div>
    {!group.children.length && <div className="query-no-filters muted">{depth ? '请为此分组添加条件。' : '尚未添加筛选，将查询当前有权读取的数据。'}</div>}
    <div className="query-filter-children">{group.children.map((child) => child.kind === 'condition'
      ? <ConditionEditor key={child.id} condition={child} properties={properties} onChange={replace} onRemove={() => remove(child.id)} />
      : <GroupEditor key={child.id} group={child} properties={properties} onChange={replace} onRemove={() => remove(child.id)} depth={depth + 1} />)}</div>
    <div className="query-group-actions"><button className="button subtle" type="button" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newCondition(properties)] })}><Plus size={14} />添加条件</button>
      {depth < 3 && <button className="button subtle" type="button" disabled={group.children.length >= 30} onClick={() => onChange({ ...group, children: [...group.children, newGroup()] })}><Braces size={14} />添加分组</button>}</div>
  </div>;
}

type Executed = { result: QueryResult; spec: QuerySpec; page: number };
function QueryWorkspace({ api, schema, createIntents }: { api: ApiClient; schema: DefinitionRecord; createIntents: Map<string, string> }) {
  const properties = schema.definition.schema!.properties;
  const [editor, setEditor] = useState<QueryEditor>(() => ({ filter: newGroup(), select: [], sort: '', direction: 'asc', limit: '25' }));
  const [executed, setExecuted] = useState<Executed>();
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [dialog, setDialog] = useState<{ id?: string }>();
  const request = useRef<{ controller: AbortController; sequence: number } | undefined>(undefined);
  const sequence = useRef(0);

  useEffect(() => () => {
    sequence.current += 1;
    request.current?.controller.abort();
  }, []);

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
    try { void execute(compileQuery(schema.definition.id, properties, editor), 1); }
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
    setError(message);
    setNotice('操作被授权拒绝，已清除先前读取的结果。');
  }

  const selectedCount = editor.select.length;
  const result = executed?.result;
  return <>
    <div className="card query-builder">
      <div className="query-section-heading"><div><Filter size={18} /><h2>构建查询</h2></div><span className="muted">更改配置后，点击运行应用</span></div>
      <div className="query-builder-content">
        <section className="query-filter-section"><div className="query-step-title"><span>01</span><h3>筛选条件</h3></div>
          <GroupEditor group={editor.filter} properties={properties} onChange={(filter) => change({ filter })} />
        </section>
        <div className="query-settings-grid"><section><div className="query-step-title"><span>02</span><h3>返回字段</h3><small className="muted">已选 {selectedCount} 项</small></div>
          <div className="query-column-actions"><button type="button" className="button subtle" onClick={() => change({ select: properties.map((property) => property.id) })}>全选</button><button type="button" className="button subtle" onClick={() => change({ select: [] })}>清空</button></div>
          <div className="query-columns">{properties.map((property) => <label className={`query-column ${editor.select.includes(property.id) ? 'query-column-selected' : ''}`} key={property.id}>
            <input type="checkbox" aria-label={property.name} checked={editor.select.includes(property.id)} onChange={(event) => change({ select: event.target.checked ? [...editor.select, property.id] : editor.select.filter((id) => id !== property.id) })} />
            <span>{property.name}</span><small className="muted">{property.dataType}</small>
          </label>)}</div>
        </section><section><div className="query-step-title"><span>03</span><h3>排序与分页</h3></div>
          <div className="query-sort-settings"><div className="field"><label htmlFor="query-sort">排序字段</label><select id="query-sort" value={editor.sort} onChange={(event) => change({ sort: event.target.value })}>
            <option value="">实例 ID（默认）</option>{properties.filter((property) => property.dataType !== 'text' && property.dataType !== 'longtext').map((property) => <option key={property.id} value={property.id}>{property.name}</option>)}
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
      <div className="query-section-heading"><div><Rows3 size={18} /><h2>查询结果</h2>{result && <span className="badge">本页 {result.data.length} 条</span>}</div>
        <button className="button" type="button" onClick={() => setDialog({})}><Plus size={16} />新建实例</button>
      </div>
      {stale && result && <div className="query-stale" role="status"><span className="query-stale-dot" />结果已过期：配置或数据已改变，请重新运行查询。<span>续页已停用</span></div>}
      {!result ? <div className="empty-state query-result-empty"><div className="query-empty-icon"><Database size={27} /></div><h3>{busy ? '正在读取数据' : '准备好后，运行第一次查询'}</h3><p className="muted">{busy ? '你可以取消本次请求并继续调整条件。' : '勾选返回字段，按需添加条件。这里将展示服务端返回的真实结果。'}</p></div>
        : !result.data.length ? <div className="empty-state query-result-empty"><Check size={30} /><h3>没有匹配的数据</h3><p className="muted">可以调整筛选条件后重新运行，或新建一个实例。</p></div>
          : <div className="table-wrap query-result-table"><table><thead><tr><th className="query-row-number">#</th><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}<small>{field.dataType}</small></th>)}<th className="query-row-action">操作</th></tr></thead>
            <tbody>{result.data.map((instance, index) => <tr key={instance.id}><td className="query-row-number">{index + 1}</td><td><button type="button" className="query-id-link" title={instance.id} onClick={() => setDialog({ id: instance.id })}>{instance.id}</button></td>
              {result.fields.map((field) => { const value = instance.values[field.id]; return <td key={field.id}><span className={value === undefined || value === null || value === '' ? 'query-cell-special' : 'query-cell-value'}>{valueText(value)}</span></td>; })}
              <td className="query-row-action"><button type="button" className="button subtle" onClick={() => setDialog({ id: instance.id })}>查看<ChevronRight size={14} /></button></td></tr>)}</tbody>
          </table></div>}
      {executed && <div className="query-result-footer"><span className="muted">第 {executed.page} 页 · {executed.result.hasMore ? '后面还有数据' : '已到结果末尾'}</span>
        <button className="button" type="button" disabled={busy || stale || !executed.result.hasMore || !executed.result.nextCursor} onClick={nextPage}>下一页<ArrowRight size={15} /></button></div>}
    </section>
    {dialog && <InstanceDialog key={dialog.id ?? 'new'} api={api} schema={schema} instanceId={dialog.id} createIntents={createIntents} onClose={() => setDialog(undefined)} onChanged={changed} onForbidden={forbidden} />}
  </>;
}
