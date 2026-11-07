import { forwardRef, lazy, Suspense, useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { ArrowRight, BookOpen, Check, ChevronDown, Code2, Database, FolderTree, LoaderCircle, Play, Search, Square, Undo2, Wand2 } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, JsonValue, OntologySQLAnalysis, OntologySQLCompletions, OntologySQLRequest, Organization, QueryContextModel, QueryPathCandidate, QueryResult, QuerySpec, SQLDiagnostic, SQLParameter } from '../../lib/types';
import { Dialog } from '../../components/Dialog';
import { RelatedInstances } from './RelatedInstances';
import { ValueInput } from './InstanceDialog';
import { errorMessage, valueText } from './query';
import { modelTemplate, parameterSignature, parameterValues, queryToEditor } from './ontologySql';
import { proposeFieldInsertion } from './sqlInsertion';
import type { ParameterDrafts } from './ontologySql';
import type { QueryEditor } from './query';
import type { SQLEditorHandle } from './OntologySQLEditor';

const SQLEditor = lazy(() => import('./OntologySQLEditor'));
export type SQLDraftMemory = { text: string; drafts: ParameterDrafts };
export type SQLWorkspaceHandle = { toVisual: () => Promise<{ model: QueryContextModel; editor: QueryEditor } | undefined> };
type Props = {
  api: ApiClient; categoryId: string; schemas: DefinitionRecord[]; categories?: Organization[];
  initialSchemaId?: string; selectionRequest?: number; onModel: () => void;
  initialDraft: SQLDraftMemory; onDraft: (draft: SQLDraftMemory) => void;
};
type Executed = { result: QueryResult; query: QuerySpec; page: number; schema?: DefinitionRecord };
type PathProposal = { sourceText: string; generation: number; target: QueryContextModel; candidates: QueryPathCandidate[]; truncated: boolean; message: string };

async function textHash(text: string): Promise<string | undefined> {
  if (!globalThis.crypto?.subtle) return undefined;
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, '0')).join('');
}
function matchesResponse(response: { requestId: string; textHash: string }, id: string, hash?: string) {
  return response.requestId === id && (!hash || response.textHash === hash);
}
const authFailure = (error: unknown) => error instanceof ApiError && [401, 403].includes(error.status);

export const OntologySQLWorkspace = forwardRef<SQLWorkspaceHandle, Props>(function OntologySQLWorkspace({ api, categoryId, schemas, categories = [], initialSchemaId, selectionRequest, onModel, initialDraft, onDraft }, ref) {
  const [text, setText] = useState(initialDraft.text);
  const [drafts, setDrafts] = useState<ParameterDrafts>(initialDraft.drafts);
  const [analysis, setAnalysis] = useState<OntologySQLAnalysis>();
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState('');
  const [models, setModels] = useState<QueryContextModel[]>([]);
  const [scopeModels, setScopeModels] = useState<QueryContextModel[]>([]);
  const [selectedId, setSelectedId] = useState(initialSchemaId ?? '');
  const [nextContextCursor, setNextContextCursor] = useState<string>();
  const [search, setSearch] = useState('');
  const [contextBusy, setContextBusy] = useState(false);
  const [contextError, setContextError] = useState('');
  const [contextRevision, setContextRevision] = useState(0);
  const [directory, setDirectory] = useState(() => !window.matchMedia?.('(max-width: 760px)').matches);
  const [guide, setGuide] = useState(() => !window.matchMedia?.('(max-width: 760px)').matches);
  const [fieldsPanel, setFieldsPanel] = useState(() => !window.matchMedia?.('(max-width: 760px)').matches);
  const [composing, setComposing] = useState(false);
  const [executed, setExecuted] = useState<Executed>();
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [relatedId, setRelatedId] = useState<string>();
  const [detailId, setDetailId] = useState<string>();
  const [pendingTemplate, setPendingTemplate] = useState<string>();
  const [pathProposal, setPathProposal] = useState<PathProposal>();
  const [pathBusy, setPathBusy] = useState(false);
  const [fieldBusy, setFieldBusy] = useState(false);
  const [fieldAlias, setFieldAlias] = useState('');
  const [undo, setUndo] = useState<SQLDraftMemory[]>([]);
  const editor = useRef<SQLEditorHandle>(null);
  const alive = useRef(true);
  const generation = useRef(0);
  const checkController = useRef<AbortController | undefined>(undefined);
  const runController = useRef<AbortController | undefined>(undefined);
  const formatController = useRef<AbortController | undefined>(undefined);
  const pathController = useRef<AbortController | undefined>(undefined);
  const fieldController = useRef<AbortController | undefined>(undefined);
  const completeControllers = useRef(new Set<AbortController>());
  const requestCounter = useRef(0);
  const latest = useRef({ text, drafts, analysis, composing }); latest.current = { text, drafts, analysis, composing };
  const draftCallback = useRef(onDraft); draftCallback.current = onDraft;
  const contextGeneration = useRef(0);
  const contextController = useRef<AbortController | undefined>(undefined);
  const navigating = useRef<string>('');
  useEffect(() => { draftCallback.current({ text, drafts }); }, [text, drafts]);
  useEffect(() => { alive.current = true; return () => {
    alive.current = false; generation.current++;
    checkController.current?.abort(); runController.current?.abort(); formatController.current?.abort(); contextController.current?.abort(); pathController.current?.abort(); fieldController.current?.abort();
    for (const controller of completeControllers.current) controller.abort();
  }; }, []);

  const clearAuthorized = useCallback((message: string) => {
    generation.current++; contextGeneration.current++;
    checkController.current?.abort(); runController.current?.abort(); formatController.current?.abort(); contextController.current?.abort(); pathController.current?.abort(); fieldController.current?.abort();
    for (const controller of completeControllers.current) controller.abort();
    setModels([]); setScopeModels([]); setAnalysis(undefined); setExecuted(undefined); setRelatedId(undefined); setDetailId(undefined);
    setPathProposal(undefined); setPendingTemplate(undefined); setPathBusy(false); setFieldBusy(false); setFieldAlias('');
    setStale(false); setBusy(false); setChecking(false); setContextBusy(false); setError(message); setNotice('授权已改变，请重新读取可查询目录。');
  }, []);

  useEffect(() => {
    contextController.current?.abort();
    if (!categoryId) { setModels([]); setContextBusy(false); return; }
    const controller = new AbortController(); contextController.current = controller;
    const current = ++contextGeneration.current;
    setContextBusy(true); setContextError('');
    const timer = setTimeout(() => {
      api.queryContext({ categoryId, search, limit: 30 }, controller.signal).then((context) => {
        if (controller.signal.aborted || current !== contextGeneration.current) return;
        setModels(context.models); setNextContextCursor(context.nextCursor);
        setSelectedId((previous) => context.models.some((model) => model.schemaId === previous) ? previous : context.models[0]?.schemaId ?? '');
      }).catch((cause: unknown) => {
        if (!controller.signal.aborted && current === contextGeneration.current) {
          if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setContextError(errorMessage(cause));
        }
      }).finally(() => { if (!controller.signal.aborted && current === contextGeneration.current) setContextBusy(false); });
    }, search ? 250 : 0);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [api, categoryId, search, clearAuthorized, contextRevision]);

  const selected = models.find((model) => model.schemaId === selectedId) ?? scopeModels.find((model) => model.schemaId === selectedId);
  useEffect(() => {
    const key = `${initialSchemaId ?? ''}:${selectionRequest ?? ''}`;
    if (!initialSchemaId || key === navigating.current || !models.length) return;
    const model = models.find((item) => item.schemaId === initialSchemaId);
    if (!model) return;
    navigating.current = key; setSelectedId(model.schemaId);
    // A model-page navigation is explicit; retain the previous draft as an undo snapshot.
    const next = modelTemplate(model);
    if (latest.current.text && latest.current.text !== next) setUndo((previous) => [...previous.slice(-4), { text: latest.current.text, drafts: latest.current.drafts }]);
    generation.current++; setText(next); setDrafts({}); setAnalysis(undefined); setStale(!!executed);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialSchemaId, selectionRequest, models]);

  function invalidate() {
    generation.current++;
    checkController.current?.abort(); runController.current?.abort(); formatController.current?.abort(); pathController.current?.abort(); fieldController.current?.abort();
    for (const controller of completeControllers.current) controller.abort();
    setBusy(false); setChecking(false); setStale(!!executed); setError(''); setNotice(''); setRelatedId(undefined); setDetailId(undefined);
    setPathProposal(undefined); setPathBusy(false); setFieldBusy(false);
  }
  function changeText(value: string) { invalidate(); setText(value); setAnalysis(undefined); setCheckError(''); setPendingTemplate(undefined); }
  function changeDraft(name: string, patch: Partial<ParameterDrafts[string]>, parameter: SQLParameter) {
    invalidate();
    setDrafts((previous) => ({ ...previous, [name]: { ...(previous[name] ?? { mode: 'missing', text: '' }), ...patch, signature: parameterSignature(parameter) } }));
  }
  function requestValues(allowInvalid: boolean): Record<string, JsonValue> {
    const parameters = latest.current.analysis?.parameters ?? [];
    if (!allowInvalid) return parameterValues(parameters, latest.current.drafts);
    // Keep discovering all parameters while an individual input is incomplete.
    const values: Record<string, JsonValue> = Object.create(null);
    for (const parameter of parameters) {
      try { Object.assign(values, parameterValues([parameter], latest.current.drafts)); } catch { /* Show the local typed error below. */ }
    }
    return values;
  }
  function makeRequest(values: Record<string, JsonValue>, sourceText = latest.current.text): OntologySQLRequest {
    return { text: sourceText, parameters: values, defaultCategoryId: categoryId, requestId: `sql-${++requestCounter.current}` };
  }
  async function analyzeNow(signal: AbortSignal): Promise<OntologySQLAnalysis | undefined> {
    const current = generation.current;
    const request = makeRequest(requestValues(true));
    const hash = await textHash(request.text);
    if (signal.aborted || generation.current !== current || latest.current.composing) return;
    const result = await api.analyzeOntologySQL(request, signal);
    if (signal.aborted || !alive.current || generation.current !== current || latest.current.composing || !matchesResponse(result, request.requestId!, hash)) return;
    if (result.diagnostics.some((diagnostic) => ['source_unavailable', 'field_unavailable', 'link_unavailable'].includes(diagnostic.code))) {
      setExecuted(undefined); setRelatedId(undefined); setDetailId(undefined); setStale(false);
      for (const controller of completeControllers.current) controller.abort();
      setModels([]); setScopeModels([]); setNotice('查询对象或属性不可用，先前结果与候选已清除。');
      pathController.current?.abort(); fieldController.current?.abort(); setPathProposal(undefined); setPendingTemplate(undefined); setPathBusy(false); setFieldBusy(false);
    }
    setDrafts((previous) => {
      const allowed = new Map(result.parameters.map((parameter) => [parameter.name, parameterSignature(parameter)]));
      const retained = Object.fromEntries(Object.entries(previous).filter(([name, draft]) => allowed.get(name) === draft.signature));
      return Object.keys(retained).length === Object.keys(previous).length ? previous : retained;
    });
    setAnalysis(result); setCheckError('');
    return result;
  }
  const parameterMetadata = JSON.stringify(analysis?.parameters.map((parameter) => [parameter.name, parameterSignature(parameter)]));
  useEffect(() => {
    checkController.current?.abort();
    if (composing || !text.trim() || !categoryId) { setChecking(false); return; }
    const controller = new AbortController(); checkController.current = controller;
    const current = generation.current;
    setChecking(true);
    const timer = setTimeout(() => {
      analyzeNow(controller.signal).catch((cause: unknown) => {
        if (!controller.signal.aborted && current === generation.current) {
          if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setCheckError(errorMessage(cause));
        }
      }).finally(() => { if (!controller.signal.aborted && current === generation.current) setChecking(false); });
    }, 500);
    return () => { clearTimeout(timer); controller.abort(); };
    // Values and text form the complete editable input. Analysis itself never schedules another scan.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, categoryId, text, drafts, composing, clearAuthorized, parameterMetadata]);

  useImperativeHandle(ref, () => ({ async toVisual() {
    if (latest.current.composing) throw new Error('请先完成中文输入');
    if (!latest.current.text.trim()) return undefined;
    checkController.current?.abort();
    const controller = new AbortController(); checkController.current = controller; const current = generation.current;
    try {
      const result = await analyzeNow(controller.signal);
      if (!result?.ready || !result.query) throw new Error(result?.diagnostics[0]?.message ?? '请先补全语句和全部参数');
      if (result.query.rows) throw new Error('多模型关联行查询不能转换为单模型可视化查询，请继续使用 OntologySQL');
      // Explicitly validate every typed input; never silently drop an invalid or missing value.
      parameterValues(result.parameters, latest.current.drafts);
      if (result.parameters.some((parameter) => !parameter.provided)) throw new Error('请先填写全部参数');
      const context = await api.queryContext({ schemaId: result.query.from.schemaId, limit: 1 }, controller.signal);
      if (controller.signal.aborted || !alive.current || current !== generation.current || latest.current.composing) throw new Error('查询已改变，请重新转换');
      const model = context.models.find((item) => item.schemaId === result.query!.from.schemaId);
      if (!model) throw new Error('当前模型不可查询');
      return { model, editor: queryToEditor(result.query, model) };
    } catch (cause) { if (authFailure(cause) && alive.current) clearAuthorized(errorMessage(cause)); throw cause; }
  } }));

  async function complete(sourceText: string, cursorOffset: number, signal: AbortSignal): Promise<OntologySQLCompletions | undefined> {
    if (latest.current.composing) return;
    const controller = new AbortController(); completeControllers.current.add(controller);
    signal.addEventListener('abort', () => controller.abort(), { once: true });
    const current = generation.current;
    const request = { ...makeRequest(requestValues(true), sourceText), cursorOffset };
    try {
      const hash = await textHash(sourceText);
      if (controller.signal.aborted || generation.current !== current) return;
      const result = await api.completeOntologySQL(request, controller.signal);
      if (controller.signal.aborted || !alive.current || generation.current !== current || latest.current.text !== sourceText || latest.current.composing || !matchesResponse(result, request.requestId!, hash)) return;
      return result;
    } catch (cause) {
      if (!controller.signal.aborted && alive.current && generation.current === current) {
        if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setNotice(`补全暂不可用：${errorMessage(cause)}`);
      }
    } finally { completeControllers.current.delete(controller); }
  }
  async function format() {
    if (composing) return;
    formatController.current?.abort(); const controller = new AbortController(); formatController.current = controller;
    const current = generation.current, request = makeRequest(requestValues(true));
    try {
      const hash = await textHash(request.text);
      const result = await api.formatOntologySQL(request, controller.signal);
      if (controller.signal.aborted || !alive.current || generation.current !== current || latest.current.composing || !matchesResponse(result, request.requestId!, hash)) return;
      if (result.diagnostics.some((item) => item.severity === 'error')) { setAnalysis((previous) => ({ ...previous, requestId: result.requestId, textHash: result.textHash, fields: previous?.fields ?? [], parameters: previous?.parameters ?? [], ready: false, diagnostics: result.diagnostics })); setNotice('当前语句无法格式化，原稿已保留。'); return; }
      setUndo((previous) => [...previous.slice(-4), { text, drafts }]); changeText(result.text);
    } catch (cause) { if (!controller.signal.aborted && generation.current === current) { if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setError(errorMessage(cause)); } }
  }
  async function run(next = false) {
    if (latest.current.composing || busy) return;
    if (next && (!executed || stale || !executed.result.hasMore || !executed.result.nextCursor)) return;
    runController.current?.abort(); const controller = new AbortController(); runController.current = controller;
    const current = generation.current;
    setBusy(true); setError(''); setNotice(''); if (executed && !next) setStale(true);
    try {
      let result: QueryResult, query: QuerySpec;
      if (next && executed) {
        query = { ...executed.query, page: { limit: executed.query.page.limit, cursor: executed.result.nextCursor } };
        result = await api.query(query, controller.signal);
      } else {
        const values = requestValues(false);
        const request = makeRequest(values), hash = await textHash(request.text);
        if (controller.signal.aborted || current !== generation.current) return;
        const response = await api.queryOntologySQL(request, controller.signal);
        if (!matchesResponse(response, request.requestId!, hash)) return;
        result = response; query = response.query;
      }
      if (controller.signal.aborted || !alive.current || current !== generation.current) return;
      const schema = schemas.find((item) => item.definition.id === query.from.schemaId);
      setExecuted({ result, query: { ...query, page: { limit: query.page.limit } }, page: next ? (executed?.page ?? 1) + 1 : 1, schema }); setStale(false);
    } catch (cause) {
      if (!controller.signal.aborted && current === generation.current) {
        if (authFailure(cause)) clearAuthorized(errorMessage(cause));
        else { setError(errorMessage(cause)); if (cause instanceof ApiError && cause.code === 'invalid_cursor') { setStale(true); setNotice('游标已失效，请重新运行查询。'); } }
      }
    } finally { if (!controller.signal.aborted && current === generation.current) setBusy(false); }
  }
  function cancel() { runController.current?.abort(); setBusy(false); setNotice('本次查询已取消，可以继续编辑。'); }
  function insertTemplate(kind: 'read' | 'filter' | 'parameter') {
    if (!selected) return;
    try {
      const value = modelTemplate(selected, kind);
      if (text.trim() && text !== value) setPendingTemplate(value); else applyTemplate(value);
    } catch (cause) { setNotice(errorMessage(cause)); }
  }
  async function handleModelInsert(model: QueryContextModel) {
    if (composing) return;
    setSelectedId(model.schemaId); setFieldsPanel(true); setFieldAlias('');
    try {
      const value = modelTemplate(model);
      if (!text.trim()) { applyTemplate(value); return; }
      if (text === value) { editor.current?.focus(); setNotice('此模型的查询已在编辑器中，可以双击右侧字段添加返回列。'); return; }
      pathController.current?.abort(); fieldController.current?.abort(); setFieldBusy(false);
      const controller = new AbortController(); pathController.current = controller;
      const current = generation.current, request = { ...makeRequest(requestValues(true)), targetSchemaId: model.schemaId };
      setPendingTemplate(undefined); setPathProposal(undefined); setPathBusy(true); setNotice('');
      try {
        if (typeof api.planOntologySQLPaths !== 'function') throw new Error('当前服务尚未开放关联路径规划');
        const hash = await textHash(request.text);
        if (controller.signal.aborted || generation.current !== current || latest.current.composing) return;
        const response = await api.planOntologySQLPaths(request, controller.signal);
        if (controller.signal.aborted || !alive.current || generation.current !== current || latest.current.composing || latest.current.text !== request.text || !matchesResponse(response, request.requestId!, hash)) return;
        if (response.diagnostics.some((item) => ['source_unavailable', 'field_unavailable', 'link_unavailable'].includes(item.code))) {
          clearAuthorized(response.diagnostics[0]?.message ?? '查询对象或关系不可用'); return;
        }
        setPathProposal({ sourceText: request.text, generation: current, target: model, candidates: response.candidates, truncated: response.truncated, message: response.diagnostics.map((item) => item.message).join('；') });
      } catch (cause) {
        if (!controller.signal.aborted && alive.current && current === generation.current && !latest.current.composing) {
          if (authFailure(cause)) clearAuthorized(errorMessage(cause));
          else setPathProposal({ sourceText: request.text, generation: current, target: model, candidates: [], truncated: false, message: cause instanceof ApiError && [404, 405, 501].includes(cause.status) ? '当前服务尚未开放关联路径规划，请更新后端后重试。' : errorMessage(cause) });
        }
      } finally { if (!controller.signal.aborted && current === generation.current) setPathBusy(false); }
    } catch (cause) { setNotice(errorMessage(cause)); setPathBusy(false); }
  }
  function applyPath(candidate: QueryPathCandidate) {
    if (!pathProposal || composing || pathProposal.generation !== generation.current || pathProposal.sourceText !== latest.current.text) return;
    const added = candidate.steps.map((step) => step.target);
    setUndo((previous) => [...previous.slice(-4), { text, drafts }]);
    invalidate(); setText(candidate.text); setAnalysis(undefined); setPendingTemplate(undefined);
    setScopeModels((previous) => [...previous.filter((model) => !added.some((item) => item.schemaId === model.schemaId)), ...added.filter((model, index) => added.findIndex((item) => item.schemaId === model.schemaId) === index)]);
    editor.current?.focus(); setNotice('已加入关联路径，原筛选和参数保留；检查后点击运行。');
  }
  async function handleFieldInsert(field: QueryContextModel['fields'][number]) {
    if (composing || !selected) return;
    const selection = editor.current?.getSelection();
    if (!selection) { setNotice('编辑器正在加载，请稍后重试。'); return; }
    try {
      if (analysis?.scope || analysis?.query?.rows || /\bJOIN\s+LINK\b/i.test(text)) {
        const nodes = (analysis?.scope ?? analysis?.query?.rows)?.nodes.filter((node) => node.schemaId === selected.schemaId) ?? [];
        const chosenAlias = nodes.some((node) => node.alias === fieldAlias) ? fieldAlias : nodes.length === 1 ? nodes[0].alias : !nodes.length && fieldAlias ? fieldAlias : undefined;
        if (nodes.length > 1 && !chosenAlias) { setNotice('此模型在查询中有多个角色，请先选择字段所属的查询别名。'); return; }
        fieldController.current?.abort(); pathController.current?.abort(); setPathBusy(false); setPathProposal(undefined);
        const controller = new AbortController(); fieldController.current = controller;
        const current = generation.current, request = { ...makeRequest(requestValues(true)), schemaId: selected.schemaId, fieldId: field.id, alias: chosenAlias, selectionFrom: selection.from, selectionTo: selection.to };
        setFieldBusy(true); setNotice('');
        try {
          const hash = await textHash(request.text);
          if (controller.signal.aborted || current !== generation.current || latest.current.composing) return;
          const edit = await api.insertOntologySQLField(request, controller.signal);
          if (controller.signal.aborted || !alive.current || current !== generation.current || latest.current.composing || latest.current.text !== request.text || !matchesResponse(edit, request.requestId!, hash)) return;
          if (edit.diagnostics.length) {
            if (edit.diagnostics.some((item) => ['source_unavailable', 'field_unavailable', 'link_unavailable'].includes(item.code))) clearAuthorized(edit.diagnostics[0].message);
            else setNotice(edit.diagnostics.map((item) => item.message).join('；'));
            return;
          }
          if (edit.from < 0 || edit.to < edit.from || edit.to > edit.text.length) { setNotice('字段编辑响应的位置无效，原稿已保留。'); return; }
          if (edit.text !== text) {
            setUndo((previous) => [...previous.slice(-4), { text, drafts }]);
            editor.current?.applyEdit({ text: edit.text, from: edit.to, to: edit.to });
          } else editor.current?.locate({ from: edit.to, to: edit.to, line: 1, column: 1 });
          setNotice(edit.text === text ? '此字段已在查询中，已定位到现有字段。' : '已插入所选角色的字段，检查后点击运行。');
        } catch (cause) {
          if (!controller.signal.aborted && current === generation.current) { if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setNotice(errorMessage(cause)); }
        } finally { if (!controller.signal.aborted && current === generation.current) setFieldBusy(false); }
        return;
      }
      const edit = proposeFieldInsertion(text, selection, selected, field, categoryId);
      if (edit.text === text) editor.current?.locate({ from: edit.to, to: edit.to, line: 1, column: 1 });
      else {
        setUndo((previous) => [...previous.slice(-4), { text, drafts }]);
        editor.current?.applyEdit({ ...edit, from: edit.to });
      }
      setNotice(edit.message ?? '已插入字段；检查后点击运行。');
    } catch (cause) { setNotice(errorMessage(cause)); }
  }
  function applyTemplate(value: string) {
    setUndo((previous) => [...previous.slice(-4), { text, drafts }]); invalidate(); setText(value); setDrafts({}); setAnalysis(undefined); setPendingTemplate(undefined); editor.current?.focus();
  }
  async function loadMore() {
    if (!nextContextCursor || contextBusy) return;
    const controller = new AbortController(); contextController.current = controller; const current = contextGeneration.current;
    setContextBusy(true);
    try {
      const context = await api.queryContext({ categoryId, search, cursor: nextContextCursor, limit: 30 }, controller.signal);
      if (!controller.signal.aborted && current === contextGeneration.current) {
        setModels((previous) => [...previous, ...context.models.filter((model) => !previous.some((entry) => entry.schemaId === model.schemaId))]); setNextContextCursor(context.nextCursor);
      }
    } catch (cause) { if (!controller.signal.aborted) { if (authFailure(cause)) clearAuthorized(errorMessage(cause)); else setContextError(errorMessage(cause)); } }
    finally { if (!controller.signal.aborted && current === contextGeneration.current) setContextBusy(false); }
  }
  const parameters = analysis?.parameters ?? [];
  let parameterError = '';
  try { parameterValues(parameters, drafts); } catch (cause) { parameterError = errorMessage(cause); }
  const diagnostics: SQLDiagnostic[] = analysis?.diagnostics ?? [];
  const result = executed?.result;
  const rowCount = result?.rowMode ? result.rows?.length ?? 0 : result?.data.length ?? 0;
  const queryScope = analysis?.scope ?? analysis?.query?.rows;
  const selectedRoles = queryScope?.nodes.filter((node) => node.schemaId === selectedId) ?? [];
  const selectedRole = selectedRoles.find((node) => node.alias === fieldAlias) ?? (selectedRoles.length === 1 ? selectedRoles[0] : undefined);
  // Use bound IDs so equal names on different models or self-Link roles remain independent.
  const selectedFieldIds = new Set(queryScope
    ? selectedRole ? (analysis?.fields ?? []).filter((field) => field.id.startsWith(`${selectedRole.id}.`)).map((field) => field.id.slice(selectedRole.id.length + 1)) : []
    : analysis?.query?.from.schemaId === selectedId ? analysis.query.select : []);
  const category = categories.find((item) => item.id === categoryId);
  const bound = models.find((item) => item.schemaId === analysis?.query?.from.schemaId);
  return <div className="query-page ontology-workspace">
    <div className="query-page-heading"><div><div className="query-eyebrow">ONTOLOGY QUERY</div><h1>数据查询</h1><p className="muted">用模型和属性的业务名称编写查询，结果直接来自已映射的数据源。</p></div><span className="badge">{category?.name ?? '当前分类'} · 只读 SELECT</span></div>
    {!categoryId ? <div className="card empty-state"><Database size={32} /><h2>请先选择当前分类</h2><p className="muted">选择顶部分类后，开始编写查询。</p></div> : <>
      <div className="ontology-toolbar"><button type="button" className="button subtle" aria-expanded={guide} onClick={() => setGuide(!guide)}><BookOpen size={16} />查询指导<ChevronDown size={13} /></button><button type="button" className="button subtle" aria-expanded={directory} onClick={() => setDirectory(!directory)}><FolderTree size={16} />模型目录<ChevronDown size={13} /></button><button type="button" className="button subtle" aria-expanded={fieldsPanel} onClick={() => setFieldsPanel(!fieldsPanel)}><Code2 size={16} />模型字段<ChevronDown size={13} /></button><span className="muted">Ctrl / ⌘ + Enter 运行整条查询</span></div>
      {guide && <section className="card ontology-guide" aria-label="查询指导"><div className="ontology-panel-heading"><BookOpen size={16} /><strong>查询指导</strong></div><ol><li>双击左侧模型，生成查询。</li><li>继续双击模型，预览并选择关联路径。</li><li>双击右侧字段，按角色插入返回列。</li><li>填写筛选与参数，运行后查看结果。</li></ol><div className="ontology-guide-templates"><button type="button" className="button subtle" disabled={!selected || composing} onClick={() => insertTemplate('read')}>读取常用字段<ArrowRight size={13} /></button><button type="button" className="button subtle" disabled={!selected || composing} onClick={() => insertTemplate('filter')}>加一个筛选<ArrowRight size={13} /></button><button type="button" className="button subtle" disabled={!selected || composing} onClick={() => insertTemplate('parameter')}>使用参数<ArrowRight size={13} /></button></div><details><summary>语法速查</summary><p><code>"模型"."属性"</code><br />业务名称用双引号；双引号内转义为两个双引号。</p><p><code>'字符串'</code> · <code>TRUE / FALSE</code><br />字符串用单引号，数值不加引号。</p><p><code>:参数名</code><br />在参数面板填写，空字符串与待填写不同。</p><p><code>IS NULL</code> / <code>IS NOT NULL</code><br />请勿使用 <code>= NULL</code>。</p><p><code>STARTS_WITH("属性", :前缀)</code><br />按字面前缀匹配；% 与 _ 都是普通字符。</p><p><code>LIMIT 25</code><br />每页 1–100 条，使用结果下方“下一页”续页。</p></details><p className="muted ontology-support-note">支持单模型查询及同数据源、同数据库的 INNER Link 关联，最多 4 个 Link；使用 JOIN LINK 按已定义的关系连接模型。可添加条件和一个排序字段，暂不支持聚合与星号投影。</p></section>}
      <div className={`ontology-layout ${directory ? '' : 'without-directory'} ${fieldsPanel ? '' : 'without-fields'}`}>
        {directory && <aside className="card ontology-directory" aria-label="模型目录"><div className="ontology-panel-heading"><FolderTree size={16} /><strong>模型目录</strong><button className="button subtle" type="button" disabled={contextBusy} onClick={() => setContextRevision((value) => value + 1)}>刷新</button></div><label className="ontology-search"><Search size={15} /><input aria-label="搜索模型或字段" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索模型或字段" /></label>
          <p className="muted ontology-directory-note">单击查看右侧字段，双击生成查询；继续双击其他模型可选择关联路径。也可按 Shift+Enter。</p>
          {contextError && <div className="notice notice-error" role="alert">{contextError}</div>}
          {contextBusy && !models.length && <p className="muted">正在读取查询目录…</p>}
          {!contextBusy && !models.length && !contextError && <div className="ontology-directory-empty"><p className="muted">当前分类没有可查询的模型</p><button type="button" className="button subtle" onClick={onModel}>前往模型管理<ArrowRight size={14} /></button></div>}
          <div className="ontology-model-list">{models.map((model) => <button type="button" className={model.schemaId === selectedId ? 'active' : ''} key={model.schemaId} title="单击查看字段，双击或 Shift+Enter 插入查询" onClick={() => { setSelectedId(model.schemaId); setFieldsPanel(true); }} onDoubleClick={() => void handleModelInsert(model)} onKeyDown={(event) => { if (event.key === 'Enter' && event.shiftKey) { event.preventDefault(); void handleModelInsert(model); } }}><Database size={14} /><span>{model.name}</span><small>{model.fields.length} 属性</small></button>)}</div>
          {nextContextCursor && <button className="button subtle" type="button" disabled={contextBusy} onClick={() => void loadMore()}>加载更多模型</button>}

        </aside>}
        <section className="card ontology-center" aria-label="OntologySQL 查询"><div className="ontology-editor-heading"><div><Code2 size={17} /><strong>OntologySQL</strong>{bound && <span className="badge">{bound.name}</span>}</div><span className="muted">一条语句 · 最多 100 条 / 页</span></div>
          <Suspense fallback={<div className="ontology-editor-loading">正在加载编辑器…</div>}><SQLEditor ref={editor} text={text} diagnostics={diagnostics} onChange={changeText} onRun={() => void run()} onComposition={(value) => {
            setComposing(value); if (value) { checkController.current?.abort(); formatController.current?.abort(); pathController.current?.abort(); fieldController.current?.abort(); setPathBusy(false); setFieldBusy(false); setPathProposal(undefined); for (const controller of completeControllers.current) controller.abort(); }
          }} onComplete={complete} /></Suspense>
          {!text.trim() && <div className="ontology-first-query"><strong>从模型开始第一次查询</strong><p className="muted">{selected ? `已选择“${selected.name}”，点击模板生成 SQL，再按需修改。` : '先在左侧选择一个可查询模型。'}</p><div><button className="button" type="button" disabled={!selected || composing} onClick={() => insertTemplate('read')}>读取常用字段</button><button className="button" type="button" disabled={!selected || composing} onClick={() => insertTemplate('filter')}>加一个筛选</button><button className="button" type="button" disabled={!selected || composing} onClick={() => insertTemplate('parameter')}>使用参数</button></div></div>}
          <div className="ontology-editor-actions"><button className="button subtle" type="button" disabled={composing || !text.trim()} onClick={() => void format()}><Wand2 size={14} />格式化</button><button className="button subtle" type="button" disabled={composing} onClick={() => editor.current?.complete()}>补全<span className="ontology-key">Ctrl+Space</span></button>{undo.length > 0 && <button className="button subtle" type="button" disabled={composing} onClick={() => { const previous = undo[undo.length - 1]; invalidate(); setText(previous.text); setDrafts(previous.drafts); setAnalysis(undefined); setPendingTemplate(undefined); setUndo(undo.slice(0, -1)); }}><Undo2 size={14} />撤销生成或插入</button>}<span className="muted">{composing ? '中文输入中…' : checking ? '正在检查…' : analysis?.ready && !parameterError ? '检查通过' : text.trim() ? '检查与参数见下方' : '尚未编写'}</span></div>
          {pendingTemplate && <div className="notice ontology-replace-prompt" role="status"><p>这会替换当前 SQL 与参数；替换后可以撤销。</p><button className="button primary" type="button" disabled={composing} onClick={() => applyTemplate(pendingTemplate)}>替换 SQL</button><button className="button" type="button" onClick={() => { setPendingTemplate(undefined); setNotice(''); }}>保留原稿</button></div>}
          {pathBusy && <div className="notice ontology-path-progress" role="status"><LoaderCircle size={15} className="query-spin" />正在寻找可见模型之间的关联路径…<button className="button subtle" type="button" onClick={() => { pathController.current?.abort(); setPathBusy(false); }}>取消规划</button></div>}
          {fieldBusy && <div className="notice ontology-path-progress" role="status"><LoaderCircle size={15} className="query-spin" />正在按查询别名检查字段…</div>}
          {pathProposal && <section className="ontology-path-proposal" aria-label="关联路径选择"><div className="ontology-panel-heading"><FolderTree size={16} /><strong>连接到 {pathProposal.target.name}</strong></div>
            <p className="muted">先预览并选择关联路径；采纳后添加 JOIN，保留已有返回列、筛选及参数。新模型的返回字段在右侧按需双击添加。</p>
            {pathProposal.message && <div className="notice" role="status">{pathProposal.message}</div>}
            {pathProposal.truncated && <div className="notice" role="status">已达到路径规划预算，候选可能不完整；请缩小范围或先加入中间模型。</div>}
            {!pathProposal.candidates.length && !pathProposal.message && <p className="muted">当前可用关系中没有找到连接路径；可以保留原稿，或改为此模型的单模型查询。</p>}
            <div className="ontology-path-candidates">{pathProposal.candidates.map((candidate, index) => <article key={candidate.id}>
              <strong>路径 {index + 1} · 从 {candidate.fromAlias} 接入</strong>
              <ol>{candidate.steps.map((step, stepIndex) => <li key={`${step.linkId}:${stepIndex}`}><span>{step.fromAlias}</span><ArrowRight size={12} /><span>{step.name}（{step.direction === 'out' ? '正向' : '反向'}）</span><ArrowRight size={12} /><span>{step.toAlias}</span><small className="muted">{step.category} / {step.target.name}</small></li>)}</ol>
              <details><summary>查看生成的 SQL</summary><pre>{candidate.text}</pre></details>
              <button className="button primary" type="button" disabled={composing || fieldBusy} onClick={() => applyPath(candidate)}>使用路径 {index + 1}</button>
            </article>)}</div>
            <div className="query-actions"><button className="button" type="button" disabled={composing} onClick={() => { const value = modelTemplate(pathProposal.target); setPathProposal(undefined); setPendingTemplate(value); }}>改为单模型查询</button><button className="button subtle" type="button" onClick={() => setPathProposal(undefined)}>保留原稿</button></div>
          </section>}
          {queryScope && <div className="ontology-query-scopes" role="group" aria-label="查询中的模型角色"><span className="muted">关联角色</span>{queryScope.nodes.map((node) => <button type="button" key={node.id} className="button subtle" onClick={() => { setSelectedId(node.schemaId); setFieldsPanel(true); setFieldAlias(node.alias); }}>{node.alias}</button>)}</div>}
          {parameters.length > 0 && <section className="ontology-parameters" aria-label="查询参数"><div className="ontology-panel-heading"><strong>查询参数</strong><span className="muted">值直接绑定，不需要添加引号</span></div>{parameters.map((parameter) => {
            const signature = parameterSignature(parameter), stored = drafts[parameter.name];
            const draft = stored?.signature === signature ? stored : { mode: 'missing' as const, text: '', signature };
            return <div className="ontology-parameter" key={parameter.name}><label htmlFor={`parameter-${parameter.name}`}><code>:{parameter.name}</code><small className="muted">{parameter.dataType}{parameter.dataType === 'decimal' ? `(${parameter.precision},${parameter.scale})` : ''}</small></label><select aria-label={`${parameter.name}赋值方式`} value={draft.mode} onChange={(event) => changeDraft(parameter.name, { mode: event.target.value as typeof draft.mode }, parameter)}><option value="missing">待填写</option><option value="value">填写值（可为空字符串）</option>{parameter.nullable && <option value="null">显式 NULL</option>}</select><div className="ontology-parameter-value"><ValueInput property={{ ...parameter, id: parameter.name }} id={`parameter-${parameter.name}`} value={draft.text} disabled={draft.mode !== 'value' || composing} onChange={(value) => changeDraft(parameter.name, { text: value, mode: 'value' }, parameter)} /></div></div>;
          })}</section>}
          {(checkError || parameterError) && <div className="notice notice-error ontology-diagnostics" role="alert">{parameterError || checkError}</div>}
          {diagnostics.length > 0 && <div className="ontology-diagnostics" aria-label="查询诊断">{diagnostics.map((diagnostic, index) => <button type="button" key={`${diagnostic.code}:${index}`} className={diagnostic.severity === 'error' ? 'error' : ''} onClick={() => editor.current?.locate(diagnostic.range)}><span>第 {diagnostic.range.line} 行 {diagnostic.range.column} 列</span>{diagnostic.message}</button>)}</div>}
          <div className="query-run-bar"><span className="muted">{analysis?.ready && !parameterError ? <><Check size={15} />可运行 · LIMIT 控制每页条数</> : 'SELECT 返回字段，FROM 选择模型，WHERE 添加筛选'}</span><div className="query-actions">{busy && <button className="button" type="button" onClick={cancel}><Square size={14} />取消查询</button>}<button className="button primary" type="button" disabled={busy || composing || !text.trim() || !!parameterError} onClick={() => void run()}>{busy ? <LoaderCircle size={16} className="query-spin" /> : <Play size={16} />}{busy ? '正在查询…' : '运行查询'}</button></div></div>
        </section>
        {fieldsPanel && <aside className="card ontology-field-panel" aria-label="模型字段"><div className="ontology-panel-heading"><Code2 size={16} /><strong>模型字段</strong>{selected && <span className="badge">{selected.fields.length}</span>}</div>
          {selected ? <>
            <div className="ontology-fields-heading"><strong>{selected.name}</strong><button className="button subtle" type="button" disabled={composing || pathBusy} onClick={() => void handleModelInsert(selected)}>生成 / 关联查询</button></div>
            <p className="muted ontology-directory-note">双击字段或按 Enter 插入。已选字段会标记，重复插入只定位已有返回列；不同模型或角色的同名字段分别保留。</p>
            {selectedRoles.length > 1 && <label className="ontology-role-select">字段所属角色<select aria-label="字段所属查询别名" value={fieldAlias} onChange={(event) => setFieldAlias(event.target.value)}><option value="">请选择此模型的角色</option>{selectedRoles.map((node) => <option key={node.id} value={node.alias}>{node.alias}</option>)}</select></label>}
            <div className="ontology-fields">{selected.fields.map((field) => {
              const included = selectedFieldIds.has(field.id);
              return <button key={field.id} type="button" className={included ? 'is-projected' : undefined} aria-label={`插入字段 ${field.name}`} title={`${field.name} · ${field.dataType}${field.filterable ? ' · 可筛选' : ''}${field.sortable ? ' · 可排序' : ''} · ${included ? '已在 SELECT 中，重复插入会定位已有列' : '双击或 Enter 插入'}`} onDoubleClick={() => void handleFieldInsert(field)} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); void handleFieldInsert(field); } }} disabled={composing || fieldBusy}>
                <span>{field.name}</span>{included && <small className="ontology-field-selected"><Check size={11} aria-hidden="true" />已选</small>}<small>{field.dataType}</small>
              </button>;
            })}</div>
          </> : <div className="ontology-directory-empty"><p className="muted">先在左侧选择一个可查询模型，即可查看字段。</p></div>}
        </aside>}

      </div>
    </>}
    {error && <div className="notice notice-error" role="alert">{error}</div>}{notice && <div className="notice" role="status">{notice}</div>}
    <section className="card query-results" aria-label="查询结果" aria-busy={busy}><div className="query-section-heading"><div><Database size={17} /><h2>查询结果</h2>{result && <span className="badge">本页 {rowCount} 条</span>}</div><span className="muted">{result?.rowMode ? `${executed?.query.rows?.nodes.length ?? 0} 个模型 · 关联行` : executed?.schema?.definition.name ?? '运行后展示真实数据'}</span></div>
      {stale && result && <div className="query-stale" role="status">结果对应上次查询：文本、参数或配置已改变，请重新运行。续页已停用。</div>}
      {result?.rowMode && <p className="ontology-row-grain muted">每行对应一组匹配的对象与关系路径。同一对象关联多个结果时保留多行，结果只读。</p>}
      {!result ? <div className="empty-state query-result-empty"><Database size={27} /><h3>{busy ? '正在读取数据' : '运行查询后，在这里查看结果'}</h3><p className="muted">检查和补全只读取模型信息；点击运行才会查询业务数据。</p></div> : !rowCount ? <div className="empty-state query-result-empty"><Check size={27} /><h3>查询完成，没有匹配的数据</h3><p className="muted">可以修改条件或参数后重新运行。</p></div> : result.rowMode ? <RowResults result={result} query={executed!.query} /> : <div className="table-wrap"><table className="query-data-table"><thead><tr><th>实例 ID</th>{result.fields.map((field) => <th key={field.id}>{field.name}<small className="muted">{field.dataType}</small></th>)}<th>操作</th></tr></thead><tbody>{result.data.map((instance) => <tr key={instance.id}><td><code>{instance.id}</code></td>{result.fields.map((field) => <td key={field.id} title={valueText(instance.values[field.id])}><span className={field.dataType === 'longtext' ? 'query-longtext-preview' : ''}>{valueText(instance.values[field.id])}</span></td>)}<td className="query-row-action"><div className="query-instance-actions"><button type="button" className="button subtle" disabled={stale} onClick={() => setDetailId(instance.id)}>查看</button><button type="button" className="button subtle" disabled={stale || !executed?.schema} onClick={() => setRelatedId(instance.id)}>关联数据</button></div></td></tr>)}</tbody></table></div>}
      {executed && <div className="query-result-footer"><span className="muted">第 {executed.page} 页 · {result?.hasMore ? '后面还有数据' : '已到结果末尾'}</span><button className="button" type="button" disabled={busy || stale || !result?.hasMore || !result.nextCursor} onClick={() => void run(true)}>下一页<ArrowRight size={15} /></button></div>}
    </section>
    {detailId && executed && !executed.result.rowMode && <SQLInstanceDetails key={detailId} api={api} id={detailId} schemaId={executed.query.from.schemaId!} fields={executed.result.fields} onClose={() => setDetailId(undefined)} onForbidden={clearAuthorized} />}
    {relatedId && executed?.schema && !executed.result.rowMode && <RelatedInstances key={relatedId} api={api} schema={executed.schema} instanceId={relatedId} onClose={() => setRelatedId(undefined)} onForbidden={clearAuthorized} />}
  </div>;
});

function RowResults({ result, query }: { result: QueryResult; query: QuerySpec }) {
  return <div className="table-wrap"><table className="query-data-table" aria-label="关联行查询结果"><thead><tr>{result.fields.map((field) => <th key={field.id}>{field.name}<small className="muted">{field.dataType}</small></th>)}<th>行来源</th></tr></thead><tbody>{result.rows?.map((row) => <tr key={row.id}>{result.fields.map((field) => <td key={field.id} title={valueText(row.values[field.id])}><span className={field.dataType === 'longtext' ? 'query-longtext-preview' : ''}>{valueText(row.values[field.id])}</span></td>)}<td><details className="ontology-row-origin"><summary>查看来源</summary><dl>{row.entities.map((entity) => <div key={entity.nodeId}><dt>{query.rows?.nodes.find((node) => node.id === entity.nodeId)?.alias ?? entity.nodeId}</dt><dd><code>{entity.instanceId}</code></dd></div>)}</dl></details></td></tr>)}</tbody></table></div>;
}

function SQLInstanceDetails({ api, id, schemaId, fields, onClose, onForbidden }: { api: ApiClient; id: string; schemaId: string; fields: QueryResult['fields']; onClose: () => void; onForbidden: (message: string) => void }) {
  const [values, setValues] = useState<Record<string, JsonValue>>();
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    api.getInstance(schemaId, id, controller.signal).then((instance) => { if (!controller.signal.aborted) setValues(instance.values); }).catch((cause: unknown) => {
      if (!controller.signal.aborted) { if (authFailure(cause)) onForbidden(errorMessage(cause)); else setError(errorMessage(cause)); }
    });
    return () => controller.abort();
  }, [api, schemaId, id, onForbidden]);
  return <Dialog title="实例详情" description="只读查看本次查询返回的属性；实例维护请使用可视化查询入口。" wide onClose={onClose}><div className="ontology-instance-details"><p className="muted">实例 ID：<code>{id}</code></p>{error ? <div className="notice notice-error" role="alert">{error}</div> : !values ? <p className="muted">正在读取实例…</p> : <dl>{fields.map((field) => <div key={field.id}><dt>{field.name}<small className="muted">{field.dataType}</small></dt><dd>{valueText(values[field.id])}</dd></div>)}</dl>}</div></Dialog>;
}
