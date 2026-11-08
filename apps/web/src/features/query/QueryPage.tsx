import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Braces, Code2, LoaderCircle } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, Organization, SavedQueryInput } from '../../lib/types';
import { compileQuery, errorMessage } from './query';
import { compileRowQuery, queryToSQLDraft } from './ontologySql';
import { assertVisualBindings, loadRowContext } from './visualRows';
import { VisualQueryPage } from './VisualQueryPage';
import type { VisualDraft } from './VisualQueryPage';
import { OntologySQLWorkspace } from './OntologySQLWorkspace';
import type { SQLDraftMemory, SQLWorkspaceHandle, VisualConversion } from './OntologySQLWorkspace';
import { SavedQueries } from './SavedQueries';
import { visualSavedTemplate } from './saved-query-template';
import './query.css';

type Props = { api: ApiClient; categoryId: string; schemas: DefinitionRecord[]; categories?: Organization[]; initialSchemaId?: string; selectionRequest?: number; onModel: () => void };
const clients = new WeakMap<ApiClient, number>();
let sequence = 0;
function clientKey(api: ApiClient) { if (!clients.has(api)) clients.set(api, ++sequence); return clients.get(api); }

export function QueryPage(props: Props) {
  const session = useRef<{ api: ApiClient; drafts: Map<string, SQLDraftMemory> }>({ api: props.api, drafts: new Map() });
  if (session.current.api !== props.api) session.current = { api: props.api, drafts: new Map() };
  const remember = useCallback((categoryId: string, draft: SQLDraftMemory) => {
    const drafts = session.current.drafts;
    drafts.delete(categoryId); drafts.set(categoryId, draft);
    if (drafts.size > 8) drafts.delete(drafts.keys().next().value!);
  }, []);
  return <QueryModes key={`${clientKey(props.api)}:${props.categoryId}`} {...props} initialDraft={session.current.drafts.get(props.categoryId)} remember={remember} />;
}

function QueryModes({ initialDraft, remember, ...props }: Props & { initialDraft?: SQLDraftMemory; remember: (categoryId: string, draft: SQLDraftMemory) => void }) {
  const [mode, setMode] = useState<'sql' | 'visual'>('sql');
  const [draft, setDraft] = useState<SQLDraftMemory>(initialDraft ?? { text: '', drafts: {} });
  const [visual, setVisual] = useState<VisualConversion>();
  const visualDraft = useRef<VisualDraft | undefined>(undefined);
  const sql = useRef<SQLWorkspaceHandle>(null);
  const [switching, setSwitching] = useState(false);
  const [message, setMessage] = useState('');
  const [authorizationFailure, setAuthorizationFailure] = useState<{ sequence: number; message: string }>();
  const active = useRef(true);
  const modeRequest = useRef(0);
  const modeController = useRef<AbortController | undefined>(undefined);
  useEffect(() => { active.current = true; return () => { active.current = false; modeRequest.current++; modeController.current?.abort(); }; }, []);
  const onDraft = useCallback((value: SQLDraftMemory) => { setDraft(value); remember(props.categoryId, value); }, [props.categoryId, remember]);
  const onVisualDraft = useCallback((value: VisualDraft) => { visualDraft.current = value; }, []);
  function savedAuthorizationFailed(message: string) {
    setAuthorizationFailure({ sequence: ++modeRequest.current, message });
    onDraft({ text: draft.text, drafts: {} });
  }
  async function capture(signal: AbortSignal): Promise<Omit<SavedQueryInput, 'name'>> {
    if (mode === 'sql') {
      const text = sql.current?.sourceText();
      if (!text?.trim()) throw new Error('请先编写查询');
      const template = await props.api.compileSavedQuery(text, props.categoryId, signal);
      return { ...template, source: { kind: 'ontology-sql', text, defaultCategoryId: props.categoryId } };
    }
    const current = visualDraft.current;
    if (!current) throw new Error('请先配置可视化查询');
    if (current.association) throw new Error('保存查询首批支持单模型，暂不支持 Link 关联');
    const context = await props.api.queryContext({ schemaId: current.schema.definition.id, limit: 1 }, signal);
    const model = context.models.find((item) => item.schemaId === current.schema.definition.id);
    if (!model) throw new Error('模型当前不可查询，请检查授权');
    if (visualDraft.current !== current) throw new Error('查询配置已改变，请重新保存');
    return { ...visualSavedTemplate(model.schemaId, model.fields, current.editor), source: { kind: 'visual', defaultCategoryId: props.categoryId } };
  }

  async function switchMode(next: 'sql' | 'visual') {
    if (next === mode || switching) return;
    const request = ++modeRequest.current;
    modeController.current?.abort(); const controller = new AbortController(); modeController.current = controller;
    setSwitching(true); setMessage('');
    try {
      if (next === 'visual') {
        const converted = await sql.current?.toVisual();
        if (!active.current || request !== modeRequest.current) return;
        if (converted && (converted.model.categoryId !== props.categoryId || !props.schemas.some((schema) => schema.definition.id === converted.model.schemaId))) throw new Error('可视化查询只支持顶部当前分类中的已加载模型；请保留此跨分类 SQL，或切换到对应分类后再转换');
        setVisual(converted); visualDraft.current = undefined;
        setMessage(converted ? '已完整转换为可视化查询；参数实参已填入条件。切换后请重新运行。' : '请勾选返回字段，按需添加条件后运行。');
      } else {
        const current = visualDraft.current;
        if (current && (current.association || current.editor.select.length || current.editor.filter.children.length || current.editor.sort)) {
          const rowContext = current.association ? await loadRowContext(props.api, current.association.rows, props.categories ?? [], controller.signal) : undefined;
          const context = rowContext ?? await props.api.queryContext({ schemaId: current.schema.definition.id, limit: 1 }, controller.signal);
          if (!active.current || request !== modeRequest.current) return;
          if (visualDraft.current !== current) throw new Error('查询配置已改变，请重新转换');
          const model = context.models.find((item) => item.schemaId === current.schema.definition.id);
          if (!model) throw new Error('当前模型不可查询，请检查授权后重试');
          const query = current.association ? compileRowQuery(current.association.rows, rowContext!, current.editor) : compileQuery(model.schemaId, model.fields, current.editor);
          const converted = queryToSQLDraft(query, model, rowContext);
          if (query.rows) {
            const requestId = crypto.randomUUID();
            const analysis = await props.api.analyzeOntologySQL({ text: converted.text, parameters: converted.parameters, defaultCategoryId: props.categoryId, requestId }, controller.signal);
            if (!active.current || request !== modeRequest.current) return;
            if (visualDraft.current !== current) throw new Error('查询配置已改变，请重新转换');
            const unavailable = analysis.diagnostics.find((item) => ['source_unavailable', 'field_unavailable', 'link_unavailable'].includes(item.code));
            if (unavailable) setAuthorizationFailure({ sequence: request, message: unavailable.message });
            if (analysis.requestId !== requestId || !analysis.ready || !analysis.query) throw new Error(analysis.diagnostics[0]?.message ?? '关联查询当前不可转换');
            assertVisualBindings(query, analysis.query);
          }
          onDraft({ text: converted.text, drafts: converted.drafts });
          setMessage('已完整生成 OntologySQL，条件值放入独立参数面板。请检查后重新运行。');
        }
      }
      setMode(next);
    } catch (error) { if (active.current && request === modeRequest.current) {
      setMessage(`无法切换：${errorMessage(error)}。当前查询已保留。`);
      if (error instanceof ApiError && [401, 403].includes(error.status)) setAuthorizationFailure({ sequence: request, message: errorMessage(error) });
    } }
    finally { if (active.current && request === modeRequest.current) setSwitching(false); }
  }

  // A mode owns its requests and results. Unmounting the mode invalidates every old cursor.
  const visualSchemas = useMemo(() => visual ? props.schemas.map((schema) => schema.definition.id === visual.model.schemaId
    ? { ...schema, definition: { ...schema.definition, schema: { ...schema.definition.schema!, properties: schema.definition.schema!.properties.filter((field) => visual.model.fields.some((entry) => entry.id === field.id)) } } }
    : schema) : props.schemas, [visual, props.schemas]);
  return <div className="ontology-query-page"><SavedQueries api={props.api} capture={capture} onDenied={savedAuthorizationFailed}>
    <div className="ontology-mode-bar" role="tablist" aria-label="查询编写方式">
      <button type="button" role="tab" aria-selected={mode === 'sql'} className={mode === 'sql' ? 'active' : ''} disabled={switching} onClick={() => void switchMode('sql')}><Code2 size={16} />OntologySQL</button>
      <button type="button" role="tab" aria-selected={mode === 'visual'} className={mode === 'visual' ? 'active' : ''} disabled={switching} onClick={() => void switchMode('visual')}><Braces size={16} />可视化查询</button>
      {switching && <span className="muted"><LoaderCircle size={15} className="query-spin" />正在检查完整转换…</span>}
      <span className="muted ontology-mode-hint">{mode === 'sql' ? '使用业务名称编写；也可切换可视化，无需写 SQL' : '选择字段、条件与 Link 关系，直接查询数据'}</span>
    </div>
    {message && <div className={`notice ${message.startsWith('无法') ? 'notice-error' : ''}`} role={message.startsWith('无法') ? 'alert' : 'status'}>{message}</div>}
    <div inert={switching}>{mode === 'sql' ? <OntologySQLWorkspace ref={sql} {...props} initialDraft={draft} onDraft={onDraft} authorizationFailure={authorizationFailure} />
      : <VisualQueryPage {...props} schemas={visualSchemas} initialSchemaId={visual?.model.schemaId ?? props.initialSchemaId} initialEditor={visual?.editor} initialRows={visual?.association} authorizationFailure={authorizationFailure} onDraftChange={onVisualDraft} />}</div>
  </SavedQueries></div>;
}
