import { useEffect, useRef, useState, type ReactNode } from 'react';
import { ArrowRight, FolderOpen, LoaderCircle, Pencil, Play, RefreshCw, Save, Trash2 } from 'lucide-react';
import { Dialog } from '../../components/Dialog';
import { ApiClient, ApiError } from '../../lib/api';
import type { Property, QueryResult, SavedQuery, SavedQueryInput, SavedQuerySummary, Values } from '../../lib/types';
import { AggregateResults } from './AggregateResults';
import { ValueInput } from './InstanceDialog';
import { errorMessage, propertyValueText } from './query';
import { savedParameterValue } from './saved-query-template';

const denied = (cause: unknown) => cause instanceof ApiError && ([401, 403, 404].includes(cause.status) || ['saved_query_binding_changed', 'definition_changed', 'unsupported_authorization', 'source_unavailable', 'field_unavailable'].includes(cause.code));
const summary = ({ id, name, etag, createdAt, updatedAt }: SavedQuerySummary): SavedQuerySummary => ({ id, name, etag, createdAt, updatedAt });
type Capture = (signal: AbortSignal) => Promise<Omit<SavedQueryInput, 'name'>>;

export function SavedQueries({ api, capture, onDenied, children }: { api: ApiClient; capture: Capture; onDenied: (message: string) => void; children: ReactNode }) {
  const [available, setAvailable] = useState(false);
  const [items, setItems] = useState<SavedQuerySummary[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [selected, setSelected] = useState<SavedQuery>();
  const [open, setOpen] = useState(false);
  const [dialog, setDialog] = useState<{ kind: 'save' | 'rename' | 'delete'; item?: SavedQuerySummary; record?: SavedQuery }>();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const controller = useRef<AbortController | undefined>(undefined);
  const listController = useRef<AbortController | undefined>(undefined);
  const sequence = useRef(0), listSequence = useRef(0);
  const latestCapture = useRef(capture); latestCapture.current = capture;
  const deniedCallback = useRef(onDenied); deniedCallback.current = onDenied;

  useEffect(() => {
    const capability = new AbortController();
    if (typeof api.dataCapabilities !== 'function') return;
    api.dataCapabilities(capability.signal).then((value) => { if (!capability.signal.aborted) setAvailable(!!value.savedQueries); }).catch(() => { /* Missing capability keeps the feature closed. */ });
    return () => { capability.abort(); sequence.current++; listSequence.current++; controller.current?.abort(); listController.current?.abort(); };
  }, [api]);

  function authorizationChanged(message: string) {
    sequence.current++; controller.current?.abort(); setSelected(undefined); setDialog(undefined); setBusy(false); setError(message);
    deniedCallback.current(message);
  }
  async function list(more = false) {
    listController.current?.abort(); const request = new AbortController(); listController.current = request;
    const current = ++listSequence.current; setLoading(true); setError('');
    try {
      const page = await api.listSavedQueries(more ? cursor : undefined, request.signal);
      if (request.signal.aborted || current !== listSequence.current) return;
      const summaries = page.data.map(summary);
      setItems((previous) => more ? [...previous, ...summaries.filter((item) => !previous.some((entry) => entry.id === item.id))] : summaries);
      setCursor(page.hasMore ? page.nextCursor : undefined);
    } catch (cause) { if (!request.signal.aborted && current === listSequence.current) { if (denied(cause)) authorizationChanged(errorMessage(cause)); else setError(errorMessage(cause)); } }
    finally { if (!request.signal.aborted && current === listSequence.current) setLoading(false); }
  }
  async function read(item: SavedQuerySummary, rename = false) {
    controller.current?.abort(); const request = new AbortController(); controller.current = request; const current = ++sequence.current;
    setBusy(true); setError(''); setNotice(''); setSelected(undefined);
    try {
      const record = await api.getSavedQuery(item.id, request.signal);
      if (request.signal.aborted || current !== sequence.current) return;
      if (rename) { setDialog({ kind: 'rename', item, record }); setName(record.name); }
      else { setSelected(record); setNotice('已加载查询模板，请填写参数后点击运行。'); }
    } catch (cause) { if (!request.signal.aborted && current === sequence.current) { if (denied(cause)) authorizationChanged(errorMessage(cause)); else setError(errorMessage(cause)); } }
    finally { if (!request.signal.aborted && current === sequence.current) setBusy(false); }
  }
  async function submit() {
    if (!dialog || busy || dialog.kind !== 'delete' && !name.trim()) return;
    controller.current?.abort(); listController.current?.abort(); listSequence.current++; setLoading(false);
    const request = new AbortController(); controller.current = request; const current = ++sequence.current;
    const action = dialog; let creating = false; setBusy(true); setError(''); setNotice('');
    try {
      if (action.kind === 'delete') {
        await api.deleteSavedQuery(action.item!.id, action.item!.etag, request.signal);
        if (request.signal.aborted || current !== sequence.current) return;
        setItems((previous) => previous.filter((item) => item.id !== action.item!.id));
        if (selected?.id === action.item!.id) setSelected(undefined);
        setNotice('查询已删除。');
      } else {
        const input = action.kind === 'rename' ? { name: name.trim(), source: action.record!.source, query: action.record!.query, parameters: action.record!.parameters } : { ...await latestCapture.current(request.signal), name: name.trim() };
        if (request.signal.aborted || current !== sequence.current) return;
        creating = action.kind === 'save';
        const saved = action.kind === 'rename' ? await api.updateSavedQuery(action.record!.id, input, action.record!.etag, request.signal) : await api.createSavedQuery(input, request.signal);
        if (request.signal.aborted || current !== sequence.current) return;
        setItems((previous) => [summary(saved), ...previous.filter((item) => item.id !== saved.id)]); setSelected(saved); setUncertain(false);
        setNotice(action.kind === 'rename' ? '查询名称已更新。' : '查询模板已保存，运行参数需要重新填写。');
      }
      setDialog(undefined);
    } catch (cause) {
      if (!request.signal.aborted && current === sequence.current) {
        if (denied(cause)) { authorizationChanged(errorMessage(cause)); if (creating) { setUncertain(true); setNotice('保存可能已经提交，请先刷新目录核对结果。'); } }
        else if (cause instanceof ApiError && cause.status === 412) { setError('查询已被其他页面修改，请关闭此窗口并刷新目录后重新读取。'); }
        else if (action.kind === 'save' && cause instanceof ApiError && (cause.status === 0 || cause.status >= 500)) {
          setUncertain(true); setDialog(undefined); setError('保存结果尚未确认，请先刷新「我的查询」核对，确认未保存后再创建。');
        } else setError(errorMessage(cause));
      }
    } finally { if (!request.signal.aborted && current === sequence.current) setBusy(false); }
  }

  if (!available) return <>{children}</>;
  return <>
    <section className="card query-saved" aria-label="我的查询">
      <div className="query-section-heading"><button type="button" className="button subtle" aria-expanded={open} onClick={() => { setOpen(!open); if (!open) void list(); }}><FolderOpen size={16} />我的查询<span className="badge">仅当前用户</span></button><div className="query-actions"><button type="button" className="button" disabled={busy || !!selected || uncertain} onClick={() => { setName(''); setError(''); setDialog({ kind: 'save' }); }}><Save size={15} />保存当前查询</button>{selected && <button type="button" className="button" onClick={() => { sequence.current++; controller.current?.abort(); setSelected(undefined); setNotice(''); }}>返回编写<ArrowRight size={14} /></button>}</div></div>
      {open && <div className="query-saved-list"><div className="query-saved-list-heading"><p className="muted">保存查询结构与参数声明；加载后由当前用户权限重新校验。</p><button type="button" className="button subtle" disabled={loading || busy} onClick={() => void list()}><RefreshCw size={14} />刷新我的查询</button></div>{loading && !items.length && <p className="muted">正在读取查询目录…</p>}{!loading && !items.length && <p className="muted">还没有保存的查询。</p>}{items.map((item) => <div key={item.id} className="query-saved-item"><button type="button" className="button subtle query-saved-name" disabled={busy} onClick={() => void read(item)}><FolderOpen size={14} />{item.name}</button><div className="query-actions"><button type="button" className="button subtle" aria-label={`重命名查询 ${item.name}`} disabled={busy} onClick={() => void read(item, true)}><Pencil size={14} />重命名</button><button type="button" className="button subtle" aria-label={`删除查询 ${item.name}`} disabled={busy} onClick={() => { setError(''); setDialog({ kind: 'delete', item }); }}><Trash2 size={14} />删除</button></div></div>)}{cursor && <button type="button" className="button" disabled={loading || busy} onClick={() => void list(true)}>加载更多查询</button>}</div>}
      {uncertain && <div className="notice" role="status">请核对目录是否已保存此查询。<button type="button" className="button subtle" onClick={() => { setOpen(true); void list(); }}>刷新并核对</button><button type="button" className="button subtle" onClick={() => { setUncertain(false); setError(''); }}>已核对，允许重新创建</button></div>}
    </section>
    {error && <div className="notice notice-error" role="alert">{error}</div>}{notice && <div className="notice" role="status">{notice}</div>}
    {selected && <SavedQueryRunner key={`${selected.id}:${selected.etag}`} api={api} record={selected} onDenied={authorizationChanged} />}
    <div hidden={!!selected} inert={busy || !!selected}>{children}</div>
    {dialog && <Dialog title={dialog.kind === 'save' ? '保存查询' : dialog.kind === 'rename' ? '重命名查询' : '删除查询'} onClose={() => { if (!busy) setDialog(undefined); }}><p className="muted">{dialog.kind === 'save' ? '保存当前查询结构；条件输入将作为参数，每次运行重新填写。' : dialog.kind === 'rename' ? '修改名称不会改变查询对象和字段绑定。' : `删除“${dialog.item?.name}”后将无法恢复。`}</p>{dialog.kind !== 'delete' && <label>查询名称<input className="field" aria-label="查询名称" autoFocus maxLength={128} value={name} onChange={(event) => setName(event.target.value)} disabled={busy} /></label>}{error && <div className="notice notice-error" role="alert">{error}</div>}<div className="dialog-actions"><button type="button" className="button" disabled={busy} onClick={() => setDialog(undefined)}>取消</button><button type="button" className="button primary" disabled={busy || dialog.kind !== 'delete' && !name.trim()} onClick={() => void submit()}>{busy ? '正在保存…' : dialog.kind === 'delete' ? '确认删除' : '确认保存'}</button></div></Dialog>}
  </>;
}

function SavedQueryRunner({ api, record, onDenied }: { api: ApiClient; record: SavedQuery; onDenied: (message: string) => void }) {
  const [fields, setFields] = useState<Property[]>([]);
  const [drafts, setDrafts] = useState<Record<string, { mode: 'missing' | 'value' | 'null'; text: string }>>({});
  const [result, setResult] = useState<QueryResult>();
  const [executed, setExecuted] = useState<{ values: Values; page: number }>();
  const [stale, setStale] = useState(false), [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef<AbortController | undefined>(undefined), sequence = useRef(0);
  const deniedCallback = useRef(onDenied); deniedCallback.current = onDenied;
  useEffect(() => {
    const request = new AbortController();
    api.queryContext({ schemaId: record.query.from.schemaId, limit: 1 }, request.signal).then((context) => { if (!request.signal.aborted) {
      const model = context.models.find((item) => item.schemaId === record.query.from.schemaId);
      if (!model) deniedCallback.current('查询模型或字段已不可用，请重新检查授权。'); else setFields(model.fields);
    } }).catch((cause: unknown) => { if (!request.signal.aborted) { if (denied(cause)) deniedCallback.current(errorMessage(cause)); else setError(errorMessage(cause)); } });
    return () => { request.abort(); sequence.current++; controller.current?.abort(); };
  }, [api, record]);
  function change(name: string, patch: Partial<{ mode: 'missing' | 'value' | 'null'; text: string }>) {
    sequence.current++; controller.current?.abort(); setBusy(false); setStale(!!result); setError('');
    setDrafts((previous) => ({ ...previous, [name]: { ...(previous[name] ?? { mode: 'missing', text: '' }), ...patch } }));
  }
  async function run(next = false) {
    try {
      const values: Values = {};
      if (next) Object.assign(values, executed?.values);
      else for (const parameter of record.parameters) {
        const draft = drafts[parameter.name];
        if (!draft || draft.mode === 'missing') throw new Error(`请填写参数 ${parameter.name}`);
        if (draft.mode === 'null') { if (!parameter.nullable) throw new Error(`${parameter.name} 不允许 NULL`); values[parameter.name] = null; }
        else values[parameter.name] = savedParameterValue({ ...parameter, id: parameter.name }, draft.text);
      }
      controller.current?.abort(); const request = new AbortController(); controller.current = request; const current = ++sequence.current;
      setBusy(true); setError(''); if (result) setStale(true);
      try {
        const response = await api.executeSavedQuery(record.id, values, next ? result?.nextCursor : undefined, request.signal);
        if (request.signal.aborted || current !== sequence.current) return;
        if (!!record.query.aggregate !== !!response.aggregateMode || response.rowMode) throw new Error('查询结果形态与模板不一致');
        setResult(response); setExecuted({ values, page: next ? (executed?.page ?? 1) + 1 : 1 }); setStale(false);
      } catch (cause) { if (!request.signal.aborted && current === sequence.current) { if (denied(cause)) { setDrafts({}); setResult(undefined); setExecuted(undefined); deniedCallback.current(errorMessage(cause)); } else setError(errorMessage(cause)); } }
      finally { if (!request.signal.aborted && current === sequence.current) setBusy(false); }
    } catch (cause) { setError(errorMessage(cause)); }
  }
  const count = result?.aggregateMode ? result.aggregateRows?.length ?? 0 : result?.data.length ?? 0;
  return <section className="card query-saved-runner" aria-label="已保存查询"><div className="query-section-heading"><div><FolderOpen size={17} /><h2>{record.name}</h2></div><span className="muted">按已保存的对象与字段运行</span></div><div className="query-saved-body"><p className="muted">{record.query.aggregate ? '分组汇总' : '明细查询'} · 每页 {record.query.page.limit} 条</p>{record.source.text && <details><summary>查看编写来源</summary><pre className="query-saved-source">{record.source.text}</pre></details>}{record.parameters.map((parameter) => {
    const draft = drafts[parameter.name] ?? { mode: 'missing' as const, text: '' };
    const source = fields.find((field) => field.enumId && field.enumId === parameter.enumId);
    return <div className="ontology-parameter" key={parameter.name}><label htmlFor={`saved-${parameter.name}`}>{parameter.name}<small className="muted">{parameter.dataType}{parameter.array ? ' 集合' : ''}</small></label><select aria-label={`${parameter.name}参数方式`} value={draft.mode} onChange={(event) => change(parameter.name, { mode: event.target.value as typeof draft.mode })}><option value="missing">待填写</option><option value="value">填写值</option>{parameter.nullable && !parameter.array && <option value="null">显式 NULL</option>}</select><div className="ontology-parameter-value">{parameter.array ? <textarea aria-label={`${parameter.name}参数值，每行一个`} id={`saved-${parameter.name}`} disabled={draft.mode !== 'value'} value={draft.text} onChange={(event) => change(parameter.name, { text: event.target.value })} placeholder="每行一个稳定业务值，最多 100 个" /> : <ValueInput property={{ ...parameter, id: parameter.name, ...(source ? { enumValues: source.enumValues } : {}) }} allowDeprecated id={`saved-${parameter.name}`} disabled={draft.mode !== 'value'} value={draft.text} onChange={(text) => change(parameter.name, { text })} />}</div></div>;
  })}{!record.parameters.length && <p className="muted">此查询没有运行参数。</p>}</div><div className="query-run-bar"><span className="muted">每次运行按当前权限校验</span><div className="query-actions">{busy && <button type="button" className="button" onClick={() => { sequence.current++; controller.current?.abort(); setBusy(false); }}>取消查询</button>}<button type="button" className="button primary" disabled={busy} onClick={() => void run()}>{busy ? <LoaderCircle size={15} className="query-spin" /> : <Play size={15} />}运行保存查询</button></div></div>{error && <div className="notice notice-error" role="alert">{error}</div>}{stale && result && <div className="query-stale" role="status">参数已改变或查询待确认，请重新运行。续页已停用。</div>}{result && <section className="query-saved-results" aria-label="保存查询结果"><p className="muted">本页 {count} 条</p>{!count ? <p className="muted">没有匹配的数据。</p> : result.aggregateMode ? <AggregateResults result={result} /> : <div className="table-wrap"><table className="query-data-table"><thead><tr>{result.fields.map((field) => <th key={field.id}>{field.name}</th>)}</tr></thead><tbody>{result.data.map((row) => <tr key={row.id}>{result.fields.map((field) => <td key={field.id}>{propertyValueText(field, row.values[field.id])}</td>)}</tr>)}</tbody></table></div>}<div className="query-result-footer"><span className="muted">第 {executed?.page} 页</span><button type="button" className="button" disabled={busy || stale || !result.hasMore || !result.nextCursor} onClick={() => void run(true)}>保存查询下一页<ArrowRight size={14} /></button></div></section>}</section>;
}
