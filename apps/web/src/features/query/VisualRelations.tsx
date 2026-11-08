import { useEffect, useRef, useState } from 'react';
import { Link2, LoaderCircle, Plus, Search, X } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, JsonValue, Organization, QueryContextModel, QueryPathCandidate, RowQuery, SQLDiagnostic } from '../../lib/types';
import { compileQuery, errorMessage } from './query';
import type { QueryEditor } from './query';
import { compileRowQuery, queryToSQLDraft } from './ontologySql';
import type { RowQueryContext } from './ontologySql';
import { appendVisualPath, assertVisualBindings, loadRowContext } from './visualRows';
import type { VisualRowsDraft } from './visualRows';

type Props = {
  api: ApiClient; schema: DefinitionRecord; categories: Organization[]; editor: QueryEditor;
  rows?: RowQuery; context?: RowQueryContext;
  onApply: (draft: VisualRowsDraft) => void;
  onRename: (id: string, alias: string) => void;
  onRemove: () => void;
  onForbidden: (message: string) => void;
};
type Proposal = { candidates: QueryPathCandidate[]; context: RowQueryContext; parameters: Record<string, JsonValue>; truncated: boolean; message: string };
const denied = (cause: unknown) => cause instanceof ApiError && [401, 403].includes(cause.status);
async function checkResponse(request: { text: string; requestId: string }, response: { requestId: string; textHash: string }) {
  if (response.requestId !== request.requestId) throw new Error('查询检查响应已失效，请重试');
  if (crypto.subtle) {
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(request.text));
    const hash = Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, '0')).join('');
    if (response.textHash !== hash) throw new Error('查询检查响应已失效，请重试');
  }
}

export function VisualRelations(props: Props) {
  const { api, schema, categories, editor, rows, context, onApply, onRename, onRemove } = props;
  const [open, setOpen] = useState(false);
  const [category, setCategory] = useState(schema.definition.categoryId);
  const [search, setSearch] = useState('');
  const [models, setModels] = useState<QueryContextModel[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [target, setTarget] = useState('');
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [proposal, setProposal] = useState<Proposal>();
  const [revision, setRevision] = useState(0);
  const controller = useRef<AbortController | undefined>(undefined);
  const directoryController = useRef<AbortController | undefined>(undefined);
  const generation = useRef(0);
  const latest = useRef(props); latest.current = props;
  const signature = JSON.stringify([editor, rows, context]);
  function clearForDenial(message: string) {
    controller.current?.abort(); directoryController.current?.abort();
    generation.current++; setModels([]); setCursor(undefined); setTarget(''); setProposal(undefined); setLoading(false); setBusy(false);
    latest.current.onForbidden(message);
  }
  function checkAvailability(diagnostics: SQLDiagnostic[]) {
    const unavailable = diagnostics.find((item) => ['source_unavailable', 'field_unavailable', 'link_unavailable'].includes(item.code));
    if (unavailable) { clearForDenial(unavailable.message); throw new Error(unavailable.message); }
  }
  useEffect(() => {
    generation.current++; controller.current?.abort(); setBusy(false); setProposal(undefined); setError('');
    return () => { generation.current++; controller.current?.abort(); };
  }, [signature, target, category, search, open]);

  useEffect(() => {
    if (!open) return;
    directoryController.current?.abort();
    const pending = new AbortController(); directoryController.current = pending;
    setModels([]); setTarget(''); setCursor(undefined); setLoading(true); setError('');
    const timer = setTimeout(() => {
      api.queryContext({ categoryId: category, search: search.trim(), limit: 30 }, pending.signal).then((result) => {
        if (!pending.signal.aborted) { setModels(result.models); setCursor(result.nextCursor); }
      }).catch((cause: unknown) => {
        if (!pending.signal.aborted) { setError(errorMessage(cause)); if (denied(cause)) clearForDenial(errorMessage(cause)); }
      }).finally(() => { if (!pending.signal.aborted) setLoading(false); });
    }, 250);
    return () => { clearTimeout(timer); pending.abort(); directoryController.current?.abort(); };
  }, [api, open, category, search, revision]);

  async function moreModels() {
    if (!cursor || loading) return;
    directoryController.current?.abort();
    const pending = new AbortController(); directoryController.current = pending;
    setLoading(true); setError('');
    try {
      const result = await api.queryContext({ categoryId: category, search: search.trim(), limit: 30, cursor }, pending.signal);
      if (!pending.signal.aborted) {
        setModels((current) => [...current, ...result.models.filter((model) => !current.some((item) => item.schemaId === model.schemaId))]);
        if (result.nextCursor === cursor) throw new Error('模型目录分页未推进，请重新读取');
        setCursor(result.nextCursor);
      }
    } catch (cause) {
      if (!pending.signal.aborted) { setError(errorMessage(cause)); if (denied(cause)) clearForDenial(errorMessage(cause)); }
    } finally { if (!pending.signal.aborted) setLoading(false); }
  }

  async function findPaths() {
    controller.current?.abort(); const pending = new AbortController(); controller.current = pending;
    const current = ++generation.current;
    setBusy(true); setError(''); setProposal(undefined);
    try {
      let refreshed: RowQueryContext;
      if (rows) refreshed = await loadRowContext(api, rows, categories, pending.signal);
      else {
        const result = await api.queryContext({ schemaId: schema.definition.id, limit: 1 }, pending.signal);
        const model = result.models.find((item) => item.schemaId === schema.definition.id);
        if (!model) throw new Error('当前模型不可查询');
        refreshed = { models: [model], links: [] };
      }
      const model = refreshed.models[0];
      const query = rows ? compileRowQuery(rows, refreshed, editor) : compileQuery(model.schemaId, model.fields, editor);
      const draft = queryToSQLDraft(query, model, refreshed);
      const request = { text: draft.text, parameters: draft.parameters, defaultCategoryId: schema.definition.categoryId, requestId: crypto.randomUUID() };
      const analysis = await api.analyzeOntologySQL(request, pending.signal);
      await checkResponse(request, analysis);
      if (pending.signal.aborted || current !== generation.current) return;
      checkAvailability(analysis.diagnostics);
      if (!analysis.ready || !analysis.query) throw new Error(analysis.diagnostics[0]?.message ?? '请完善当前查询后再查找关联');
      assertVisualBindings(query, analysis.query);
      const pathRequest = { ...request, requestId: crypto.randomUUID(), targetSchemaId: target };
      const result = await api.planOntologySQLPaths(pathRequest, pending.signal);
      await checkResponse(pathRequest, result);
      if (pending.signal.aborted || current !== generation.current) return;
      checkAvailability(result.diagnostics);
      setProposal({ candidates: result.candidates, context: refreshed, parameters: draft.parameters, truncated: result.truncated, message: result.diagnostics.map((item) => item.message).join('；') });
    } catch (cause) {
      if (!pending.signal.aborted && current === generation.current) { setError(errorMessage(cause)); if (denied(cause)) clearForDenial(errorMessage(cause)); }
    } finally { if (!pending.signal.aborted && current === generation.current) setBusy(false); }
  }

  async function apply(candidate: QueryPathCandidate) {
    if (!proposal) return;
    controller.current?.abort(); const pending = new AbortController(); controller.current = pending;
    const current = ++generation.current;
    setBusy(true); setError('');
    try {
      const added = appendVisualPath({ rows, editor }, candidate, proposal.context);
      const expected = compileRowQuery(added.rows, added.context, added.editor);
      const request = { text: candidate.text, parameters: proposal.parameters, defaultCategoryId: schema.definition.categoryId, requestId: crypto.randomUUID() };
      const analysis = await api.analyzeOntologySQL(request, pending.signal);
      await checkResponse(request, analysis);
      if (pending.signal.aborted || current !== generation.current) return;
      checkAvailability(analysis.diagnostics);
      if (!analysis.ready || !analysis.query) throw new Error(analysis.diagnostics[0]?.message ?? '此关联当前不可用，请重新查找');
      assertVisualBindings(expected, analysis.query);
      // Refresh names and field permissions for every node before displaying it.
      const refreshed = await loadRowContext(api, added.rows, categories, pending.signal);
      compileRowQuery(added.rows, refreshed, added.editor);
      if (pending.signal.aborted || current !== generation.current) return;
      onApply({ ...added, context: refreshed }); setOpen(false); setProposal(undefined);
    } catch (cause) {
      if (!pending.signal.aborted && current === generation.current) { setError(errorMessage(cause)); if (denied(cause)) clearForDenial(errorMessage(cause)); }
    } finally { if (!pending.signal.aborted && current === generation.current) setBusy(false); }
  }

  return <section className="visual-relations" aria-label="关联查询配置">
    <div className="query-section-heading"><div><Link2 size={18} /><h3>{rows ? `关联明细 · ${rows.joins.length} 条 Link` : '关联查询'}</h3></div>
      <button type="button" className="button" disabled={(rows?.joins.length ?? 0) >= 4} onClick={() => setOpen((value) => !value)}><Plus size={14} />添加关联</button></div>
    <p className="muted">{rows ? '每行是一组匹配对象；同一对象可能出现多行。字段和条件按模型角色区分，结果只读。' : '先选择返回字段，再沿现有 Link 关联其他模型；添加后按匹配关系逐行展示。'}</p>
    {rows && <><ol className="visual-role-list">{rows.nodes.map((node, index) => {
      const join = rows.joins[index - 1], link = context?.links.find((item) => item.id === join?.linkId);
      const from = rows.nodes.find((item) => item.id === join?.from);
      const model = context?.models.find((item) => item.schemaId === node.schemaId);
      return <li key={node.id}><span>{index ? `${from?.alias} → ${link?.name}（${join.direction === 'out' ? '正向' : '反向'}）→ ` : '起点：'}{model?.category} / {model?.name}</span>
        <label>模型角色<input aria-label={`模型角色 ${index + 1}`} value={node.alias} maxLength={128} onChange={(event) => onRename(node.id, event.target.value)} /></label></li>;
    })}</ol><button type="button" className="button subtle" onClick={onRemove}>移除最后一个关联</button></>}
    {open && <div className="visual-path-builder">
      <div className="query-section-heading"><h4>选择关联目标</h4><button type="button" aria-label="收起关联选择" className="button subtle" onClick={() => setOpen(false)}><X size={15} /></button></div>
      <div className="form-grid"><label className="field">关联模型分类<select aria-label="关联模型分类" value={category} onChange={(event) => setCategory(event.target.value)}>{categories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
        <label className="field">搜索关联模型<input aria-label="搜索关联模型" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="输入模型名称" /></label></div>
      <label className="field">关联目标模型<select aria-label="关联目标模型" value={target} onChange={(event) => setTarget(event.target.value)}><option value="">请选择模型</option>{models.map((model) => <option key={model.schemaId} value={model.schemaId}>{model.category} / {model.name}</option>)}</select></label>
      <div className="query-actions">{cursor && <button type="button" className="button subtle" disabled={loading} onClick={() => void moreModels()}>更多模型</button>}
        <button type="button" className="button subtle" disabled={loading} onClick={() => setRevision((value) => value + 1)}>重新读取模型</button>
        <button type="button" className="button" disabled={!target || busy || loading} onClick={() => void findPaths()}>{busy ? <LoaderCircle size={15} className="query-spin" /> : <Search size={15} />}查找关联路径</button></div>
      {loading && <p className="muted" role="status">正在读取可查询模型…</p>}
      {proposal && <div className="visual-path-candidates" role="region" aria-label="可视化关联路径选择">
        {proposal.message && <p role="status">{proposal.message}</p>}
        {proposal.truncated && <p role="status">规划未完成，请缩小范围或选择中间模型后重试。</p>}
        {!proposal.candidates.length && !proposal.message && <p>当前可用关系无法连接目标模型。</p>}
        {!proposal.truncated && proposal.candidates.map((candidate, index) => <article key={candidate.id}><strong>路径 {index + 1} · 从 {candidate.fromAlias} 接入</strong><ol>{candidate.steps.map((step, stepIndex) => <li key={stepIndex}>{step.fromAlias} → {step.category} / {step.name}（{step.direction === 'out' ? '正向' : '反向'}）→ {step.target.name} · 角色 {step.toAlias}</li>)}</ol>
          <button type="button" className="button" disabled={busy} onClick={() => void apply(candidate)}>采用关联路径 {index + 1}</button></article>)}
      </div>}
    </div>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}
  </section>;
}
