import { useCallback, useEffect, useRef, useState } from 'react';
import { Braces, Code2, LoaderCircle } from 'lucide-react';
import type { ApiClient } from '../../lib/api';
import type { DefinitionRecord, Organization, QueryContextModel } from '../../lib/types';
import { compileQuery, errorMessage } from './query';
import { queryToSQLDraft } from './ontologySql';
import type { QueryEditor } from './query';
import { VisualQueryPage } from './VisualQueryPage';
import type { VisualDraft } from './VisualQueryPage';
import { OntologySQLWorkspace } from './OntologySQLWorkspace';
import type { SQLDraftMemory, SQLWorkspaceHandle } from './OntologySQLWorkspace';
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
  const [visual, setVisual] = useState<{ model: QueryContextModel; editor: QueryEditor }>();
  const visualDraft = useRef<VisualDraft | undefined>(undefined);
  const sql = useRef<SQLWorkspaceHandle>(null);
  const [switching, setSwitching] = useState(false);
  const [message, setMessage] = useState('');
  const active = useRef(true);
  const modeRequest = useRef(0);
  useEffect(() => { active.current = true; return () => { active.current = false; modeRequest.current++; }; }, []);
  const onDraft = useCallback((value: SQLDraftMemory) => { setDraft(value); remember(props.categoryId, value); }, [props.categoryId, remember]);
  const onVisualDraft = useCallback((value: VisualDraft) => { visualDraft.current = value; }, []);

  async function switchMode(next: 'sql' | 'visual') {
    if (next === mode || switching) return;
    const request = ++modeRequest.current;
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
        if (current && (current.editor.select.length || current.editor.filter.children.length || current.editor.sort)) {
          const context = await props.api.queryContext({ schemaId: current.schema.definition.id, limit: 1 });
          if (!active.current || request !== modeRequest.current) return;
          const model = context.models.find((item) => item.schemaId === current.schema.definition.id);
          if (!model) throw new Error('当前模型不可查询，请检查授权后重试');
          const query = compileQuery(model.schemaId, model.fields, current.editor);
          onDraft(queryToSQLDraft(query, model));
          setMessage('已完整生成 OntologySQL，条件值放入独立参数面板。请检查后重新运行。');
        }
      }
      setMode(next);
    } catch (error) { if (active.current && request === modeRequest.current) setMessage(`无法切换：${errorMessage(error)}。当前查询已保留。`); }
    finally { if (active.current && request === modeRequest.current) setSwitching(false); }
  }

  // A mode owns its requests and results. Unmounting the mode invalidates every old cursor.
  const visualSchemas = visual ? props.schemas.map((schema) => schema.definition.id === visual.model.schemaId
    ? { ...schema, definition: { ...schema.definition, schema: { ...schema.definition.schema!, properties: schema.definition.schema!.properties.filter((field) => visual.model.fields.some((entry) => entry.id === field.id)) } } }
    : schema) : props.schemas;
  return <div className="ontology-query-page">
    <div className="ontology-mode-bar" role="tablist" aria-label="查询编写方式">
      <button type="button" role="tab" aria-selected={mode === 'sql'} className={mode === 'sql' ? 'active' : ''} disabled={switching} onClick={() => void switchMode('sql')}><Code2 size={16} />OntologySQL</button>
      <button type="button" role="tab" aria-selected={mode === 'visual'} className={mode === 'visual' ? 'active' : ''} disabled={switching} onClick={() => void switchMode('visual')}><Braces size={16} />可视化查询</button>
      {switching && <span className="muted"><LoaderCircle size={15} className="query-spin" />正在检查完整转换…</span>}
      <span className="muted ontology-mode-hint">{mode === 'sql' ? '使用业务名称查询，参数与数据分开管理' : '通过字段与条件配置查询'}</span>
    </div>
    {message && <div className={`notice ${message.startsWith('无法') ? 'notice-error' : ''}`} role={message.startsWith('无法') ? 'alert' : 'status'}>{message}</div>}
    {mode === 'sql' ? <OntologySQLWorkspace ref={sql} {...props} initialDraft={draft} onDraft={onDraft} />
      : <VisualQueryPage {...props} schemas={visualSchemas} initialSchemaId={visual?.model.schemaId ?? props.initialSchemaId} initialEditor={visual?.editor} onDraftChange={onVisualDraft} />}
  </div>;
}
