import { useEffect, useRef, useState } from 'react'
import { Check, FlaskConical, LoaderCircle, Pencil, Plus, RefreshCw, Rocket, Save, ShieldCheck, Trash2 } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Organization, PermissionRule, PolicyRecord, Role, SimulationResult, User, UserGroup } from '../../lib/types'
import { errorMessage } from '../query/query'
import { canonicalPolicy, expressionSummary, fieldsSummary, operationName, operations, operationResources, resourceName } from './policy'
import { RuleDialog } from './RuleDialog'

const emptyGroups: UserGroup[] = []
function Simulation({ api, record, users, groups, definitions, categories, onForbidden }: { api: ApiClient; record: PolicyRecord; users: User[]; groups: UserGroup[]; definitions: DefinitionRecord[]; categories: Organization[]; onForbidden: (error: unknown) => void }) {
  const [userId, setUserId] = useState('')
  const [operation, setOperation] = useState('instance.query')
  const [resourceId, setResourceId] = useState('*')
  const [draft, setDraft] = useState(false)
  const [result, setResult] = useState<SimulationResult>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const request = useRef<AbortController | undefined>(undefined)
  const candidates = operationResources(operation, definitions)
  const properties = definitions.find((item) => item.definition.id === resourceId)?.definition.schema?.properties ?? []
  useEffect(() => () => request.current?.abort(), [])
  useEffect(() => {
    request.current?.abort(); setResult(undefined); setError(''); setBusy(false)
  }, [api, record.etag, record.state, users, groups, definitions])
  function change() { request.current?.abort(); setResult(undefined); setError(''); setBusy(false) }
  async function run() {
    if (!userId || busy) return
    request.current?.abort()
    const controller = new AbortController(); request.current = controller
    setBusy(true); setError(''); setResult(undefined)
    try {
      const result = await api.simulatePolicy({ userId, request: { operation, resourceId }, draft }, controller.signal)
      if (!controller.signal.aborted) setResult(result)
    } catch (reason) {
      if (!controller.signal.aborted) {
        if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) onForbidden(reason)
        else setError(errorMessage(reason))
      }
    } finally { if (!controller.signal.aborted) setBusy(false) }
  }
  return <section className="card admin-simulation"><div className="admin-section-heading"><div><FlaskConical size={18} /><h2>验证授权结果</h2></div><span className="muted">仅模拟，不执行实际业务操作</span></div>
    <div className="admin-panel-body"><div className="admin-simulation-controls">
      <label className="field">模拟用户<select aria-label="模拟用户" value={userId} onChange={(event) => { change(); setUserId(event.target.value) }}><option value="">选择用户</option>{users.map((user) => <option key={user.id} value={user.id}>{user.displayName}（{user.username}）{!user.enabled ? ' · 已停用' : ''}</option>)}</select></label>
      <label className="field">模拟操作<select aria-label="模拟操作" value={operation} onChange={(event) => { change(); setOperation(event.target.value); setResourceId('*') }}>{operations.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select></label>
      <label className="field">模拟资源<select aria-label="模拟资源" value={resourceId} onChange={(event) => { change(); setResourceId(event.target.value) }}><option value="*">租户内全部资源</option>{!operation.startsWith('organization.') && candidates.map((item) => <option key={item.definition.id} value={item.definition.id}>{resourceName(item.definition.id, definitions, categories)}</option>)}</select></label>
      <label className="field">使用策略<select aria-label="使用策略" value={draft ? 'draft' : 'current'} onChange={(event) => { change(); setDraft(event.target.value === 'draft') }}><option value="current">当前生效策略</option><option value="draft" disabled={!record.draft}>已保存草稿</option></select></label>
    </div><div className="admin-inline admin-simulation-run"><p className="muted">编辑区未保存的规则不参与模拟。</p><button type="button" className="button" disabled={busy || !userId} onClick={() => void run()}>{busy ? <LoaderCircle size={16} className="admin-spin" /> : <FlaskConical size={16} />}模拟授权</button></div>
      {error && <div className="notice notice-error" role="alert">{error}<p>当前不支持将多角色中不同的字段或行条件组合为一份授权，请调整角色规则后重试。</p></div>}
      {result && <div className={`admin-simulation-result ${result.decision.allow ? 'is-allowed' : 'is-denied'}`} role="status"><div className="admin-inline"><ShieldCheck size={19} /><strong>{result.decision.allow ? '允许此操作' : '拒绝此操作'}</strong><span>{result.reason}</span></div>
        {result.decision.allow && operation.startsWith('instance.') && <dl><div><dt>可读字段</dt><dd>{fieldsSummary(result.decision.readFields, properties)}</dd></div><div><dt>可写字段</dt><dd>{fieldsSummary(result.decision.writeFields, properties)}</dd></div><div><dt>读取 / 原行条件</dt><dd>{result.contextProtected ? '含动态上下文，实际取值不回显' : expressionSummary(result.decision.rowFilter, properties, definitions)}</dd></div><div><dt>新行条件</dt><dd>{result.contextProtected ? '含动态上下文，实际取值不回显' : result.decision.newRowFilter ? expressionSummary(result.decision.newRowFilter, properties, definitions) : '沿用原行条件'}</dd></div></dl>}
      </div>}
    </div>
  </section>
}

export function PolicyPanel({ api, record, roles, users, groups = emptyGroups, definitions, categories, onRecord, onReload, onForbidden }: {
  api: ApiClient; record: PolicyRecord; roles: Role[]; users: User[]; groups?: UserGroup[]; definitions: DefinitionRecord[]; categories: Organization[]; onRecord: (record: PolicyRecord) => void; onReload: () => void; onForbidden: (error: unknown) => void
}) {
  const baseline = record.draft ?? record.current
  const [rules, setRules] = useState<PermissionRule[]>(() => baseline.rules ?? [])
  const [dialog, setDialog] = useState<{ rule?: PermissionRule }>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [conflict, setConflict] = useState(false)
  const [viewCurrent, setViewCurrent] = useState(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const dirty = canonicalPolicy(rules) !== canonicalPolicy(baseline.rules ?? [])
  const publishable = Boolean(record.draft) && canonicalPolicy(record.draft!.rules ?? []) !== canonicalPolicy(record.current.rules ?? [])
  const shown = viewCurrent ? record.current.rules ?? [] : rules
  async function commit(publish: boolean) {
    if (busy || conflict || publish && (!publishable || dirty) || !publish && !dirty) return
    setBusy(true); setError(''); setMessage('')
    try {
      const next = publish ? await api.publishPolicy(record.etag) : await api.savePolicy({ rules }, record.etag)
      if (!alive.current) return
      setMessage(publish ? '权限策略已生效。' : '草稿已保存，生效后才影响实际授权。')
      onRecord(next)
    } catch (reason) {
      if (!alive.current) return
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) { onForbidden(reason); return }
      setError(errorMessage(reason))
      if (reason instanceof ApiError && reason.status === 412) setConflict(true)
    } finally { if (alive.current) setBusy(false) }
  }
  return <div className="admin-policy-layout"><section className="card"><div className="admin-section-heading"><div><ShieldCheck size={18} /><h2>租户权限策略</h2></div><span className="badge">{record.draft ? '有已保存草稿' : '当前生效'}</span></div>
    <div className="admin-panel-body"><div className="admin-policy-toolbar"><div className="admin-segment"><button className={!viewCurrent ? 'is-active' : ''} type="button" onClick={() => setViewCurrent(false)}>编辑区{dirty ? ' · 未保存' : record.draft ? ' · 已保存草稿' : ''}</button><button className={viewCurrent ? 'is-active' : ''} type="button" onClick={() => setViewCurrent(true)}>当前生效</button></div>
      {!viewCurrent && <button className="button" type="button" disabled={busy || conflict || !roles.length} onClick={() => setDialog({})}><Plus size={16} />添加规则</button>}</div>
      {error && <div className="notice notice-error" role="alert">{error}{conflict ? <p>策略已被其他管理员修改。当前编辑内容已保留；重新读取后才能继续保存或生效。</p> : <p>多角色中不同的字段或行条件暂不支持合并，请调整规则后再验证。</p>}</div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      {conflict && <button type="button" className="button" onClick={onReload}><RefreshCw size={16} />重新读取策略（放弃本地修改）</button>}
      {!shown.length ? <div className="empty-state admin-rule-empty"><ShieldCheck size={30} /><h3>{viewCurrent ? '生效策略暂无规则' : '从一条角色规则开始'}</h3><p className="muted">没有匹配允许规则的业务请求将被拒绝。</p>{!roles.length && <p className="muted">请先到角色标签创建角色。</p>}</div>
        : <div className="admin-rules">{shown.map((rule) => {
          const properties = definitions.find((item) => item.definition.id === rule.resourceId)?.definition.schema?.properties ?? []
          return <article className="admin-rule-card" key={rule.id}><div className="admin-rule-top"><span className={`badge ${rule.effect === 'deny' ? 'admin-badge-deny' : 'admin-badge-allow'}`}>{rule.effect === 'deny' ? '拒绝' : '允许'}</span><strong>{roles.find((role) => role.id === rule.roleId)?.name ?? '不可见角色'}</strong><span className="muted">{operationName(rule.operation)}</span>
            {!viewCurrent && <div className="admin-inline"><button className="icon-button" type="button" aria-label={`编辑规则：${operationName(rule.operation)}`} disabled={busy || conflict} onClick={() => setDialog({ rule })}><Pencil size={15} /></button><button className="icon-button" type="button" aria-label={`移除规则：${operationName(rule.operation)}`} disabled={busy || conflict} onClick={() => setRules(rules.filter((item) => item.id !== rule.id))}><Trash2 size={15} /></button></div>}</div>
            <p className="admin-rule-resource">{resourceName(rule.resourceId, definitions, categories)}</p>
            {rule.effect === 'allow' && rule.operation.startsWith('instance.') && <dl><div><dt>可读 / 可写</dt><dd>{fieldsSummary(rule.readFields, properties)} / {fieldsSummary(rule.writeFields, properties)}</dd></div><div><dt>原行条件</dt><dd>{expressionSummary(rule.rowFilter, properties, definitions)}</dd></div><div><dt>新行条件</dt><dd>{rule.newRowFilter ? expressionSummary(rule.newRowFilter, properties, definitions) : '沿用原行条件'}</dd></div></dl>}
          </article>
        })}</div>}
      {!viewCurrent && <div className="admin-policy-actions"><span className="muted">{dirty ? '存在未保存修改，请先保存草稿。' : record.draft ? '已保存草稿可单独模拟，确认后使其生效。' : '修改规则并保存后，才可生效新策略。'}</span><div className="admin-inline"><button className="button" type="button" disabled={busy || !dirty || conflict} onClick={() => void commit(false)}>{busy ? <LoaderCircle size={16} className="admin-spin" /> : <Save size={16} />}保存策略草稿</button><button className="button primary" type="button" disabled={busy || dirty || !publishable || conflict} onClick={() => void commit(true)}><Rocket size={16} />使策略生效</button></div></div>}
    </div>
  </section><Simulation api={api} record={record} users={users} groups={groups} definitions={definitions} categories={categories} onForbidden={onForbidden} />
    {dialog && <RuleDialog rule={dialog.rule} roles={roles} definitions={definitions} categories={categories} onClose={() => setDialog(undefined)} onSave={(rule) => { setRules((previous) => dialog.rule ? previous.map((item) => item.id === rule.id ? rule : item) : [...previous, rule]); setDialog(undefined); setError('') }} />}
  </div>
}
