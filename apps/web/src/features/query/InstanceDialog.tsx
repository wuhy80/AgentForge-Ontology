import { useEffect, useRef, useState } from 'react';
import { AlertTriangle, Check, LoaderCircle, Pencil, RefreshCw, Save, Trash2 } from 'lucide-react';
import { ApiClient, ApiError } from '../../lib/api';
import type { DefinitionRecord, Instance, Property, Values } from '../../lib/types';
import { Dialog } from '../../components/Dialog';
import { buildWriteValues, errorMessage, initialDrafts, valueText, writeIntentSignature } from './query';
import type { FieldDrafts } from './query';

type Props = {
  api: ApiClient;
  schema: DefinitionRecord;
  instanceId?: string;
  createIntents?: Map<string, string>;
  onClose: () => void;
  onChanged: (message: string) => void;
  onForbidden?: (message: string) => void;
};

export function ValueInput({ property, value, onChange, disabled, id }: {
  property: Property; value: string; onChange: (value: string) => void; disabled?: boolean; id: string;
}) {
  if (property.dataType === 'boolean') {
    return <select id={id} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled}>
      <option value="">请选择</option><option value="true">是（true）</option><option value="false">否（false）</option>
    </select>;
  }
  if (property.dataType === 'text' || property.dataType === 'longtext') return <textarea id={id} rows={property.dataType === 'longtext' ? 8 : 3} value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled}
    placeholder={property.dataType === 'longtext' ? '支持多行正文，最多 4 MiB（UTF-8 字节）' : undefined} />;
  return <input id={id} type="text" value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled}
    autoComplete="off" spellCheck={false} inputMode={property.dataType === 'decimal' || property.dataType === 'int64' ? 'text' : undefined}
    placeholder={property.dataType === 'timestamp' ? '2026-11-06T14:30:00.123456+08:00' : property.dataType === 'uuid' ? 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' : undefined} />;
}

export function InstanceDialog({ api, schema, instanceId, createIntents, onClose, onChanged, onForbidden }: Props) {
  const properties = schema.definition.schema?.properties ?? [];
  const readOnly = !!schema.definition.schema?.storageBinding;
  const [record, setRecord] = useState<Instance>();
  const [drafts, setDrafts] = useState<FieldDrafts>(() => initialDrafts(properties));
  const [editing, setEditing] = useState(!instanceId && !readOnly);
  const [loading, setLoading] = useState(Boolean(instanceId));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [reload, setReload] = useState(0);
  const alive = useRef(true);
  const localIntents = useRef(new Map<string, string>());
  const intents = createIntents ?? localIntents.current;
  const schemaId = schema.definition.id;
  const currentId = record?.id ?? instanceId;

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useEffect(() => {
    if (!instanceId) return;
    const controller = new AbortController();
    setLoading(true);
    setError('');
    setRecord(undefined);
    setConfirmDelete(false);
    setEditing(false);
    api.getInstance(schemaId, instanceId, controller.signal).then((instance) => {
      if (controller.signal.aborted) return;
      setRecord(instance);
      setDrafts(initialDrafts(properties, instance.values));
    }).catch((reason: unknown) => {
      if (!controller.signal.aborted) {
        setError(errorMessage(reason));
        if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) onForbidden?.(reason.message);
      }
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
    // Properties are immutable within the parent workspace, which is keyed by definition ETag.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, schemaId, instanceId, reload]);

  function updateDraft(id: string, patch: Partial<FieldDrafts[string]>) {
    setDrafts((previous) => ({ ...previous, [id]: { ...previous[id], ...patch } }));
    setError('');
    setSuccess('');
  }

  async function save() {
    if (busy || loading) return;
    let values: Values;
    try {
      values = buildWriteValues(properties, drafts, record?.values);
      if (record && !Object.keys(values).length) throw new Error('尚未修改字段值');
    } catch (reason) {
      setError(errorMessage(reason));
      return;
    }
    const creating = !record;
    const signature = writeIntentSignature(values);
    let idempotencyKey = intents.get(signature);
    if (creating && !idempotencyKey) {
      idempotencyKey = crypto.randomUUID();
      // Keep uncertain attempts for the lifetime of this schema and signed-in session.
      intents.set(signature, idempotencyKey);
    }
    setBusy(true);
    setError('');
    setSuccess('');
    try {
      const saved = record
        ? await api.patchInstance(schemaId, record.id, values, record.etag)
        : await api.createInstance(schemaId, values, idempotencyKey!);
      if (creating) intents.delete(signature);
      if (!alive.current) return;
      setRecord(saved);
      setDrafts(initialDrafts(properties, saved.values));
      setEditing(false);
      setUncertain(false);
      const message = creating ? '实例已创建' : '实例已保存';
      setSuccess(message);
      onChanged(message);
    } catch (reason) {
      if (!alive.current) return;
      setError(errorMessage(reason));
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) {
        setRecord(undefined);
        onForbidden?.(reason.message);
      }
      if (creating) setUncertain(!(reason instanceof ApiError) || reason.status === 0 || reason.status === 408 || reason.status >= 500);
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  async function remove() {
    if (!record || busy) return;
    setBusy(true);
    setError('');
    try {
      await api.deleteInstance(schemaId, record.id, record.etag);
      if (!alive.current) return;
      onChanged('实例已删除');
      onClose();
    } catch (reason) {
      if (alive.current) {
        setError(errorMessage(reason));
        if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) {
          setRecord(undefined);
          onForbidden?.(reason.message);
        }
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  function resetEdit() {
    setDrafts(initialDrafts(properties, record?.values));
    setEditing(false);
    setError('');
    setConfirmDelete(false);
  }

  return <Dialog title={record || instanceId ? '实例详情' : '新建实例'}
    description={`${schema.definition.name} · 字段读写权限与行条件由服务端校验。`}
    onClose={() => { if (!busy) onClose(); }} wide>
    {currentId && <div className="query-instance-id"><span className="muted">实例 ID</span><code>{currentId}</code></div>}
    {error && <div role="alert" className="notice notice-error">{error}
      {record && <p>如记录已被其他人修改，请关闭后重新打开，再基于最新值编辑。</p>}
    </div>}
    {success && <div role="status" className="notice notice-success"><Check size={16} />{success}</div>}
    {uncertain && <div className="notice query-warning"><AlertTriangle size={16} />
      <span>创建结果尚未确认。本次会话内，关闭窗口、切换模型或刷新定义后填写相同内容重试，仍会复用原幂等键；更改内容后提交会作为新的创建操作。</span>
    </div>}
    {loading ? <div className="empty-state" role="status"><LoaderCircle className="query-spin" size={24} /><p>正在读取实例…</p></div>
      : instanceId && !record ? <div className="empty-state"><p>未能读取此实例。</p><button type="button" className="button" onClick={() => setReload((value) => value + 1)}><RefreshCw size={16} />重新读取</button></div>
        : <form onSubmit={(event) => { event.preventDefault(); void save(); }}>
          <div className="query-instance-fields">
            {properties.map((property) => {
              const draft = drafts[property.id] ?? { mode: 'unchanged', text: '' };
              const inputId = `instance-${property.id}`;
              const original = record?.values[property.id];
              return <div className="query-instance-field" key={property.id}>
                <div className="query-field-heading"><label htmlFor={editing && !property.readOnly ? inputId : undefined}>{property.name}</label>
                  <span className="query-field-tags"><span className="badge">{property.dataType === 'decimal' ? `decimal(${property.precision}, ${property.scale})` : property.dataType}</span>
                    {property.readOnly && <span className="badge">只读</span>}{property.required && <span className="badge">必填</span>}</span>
                </div>
                {!editing || property.readOnly ? <div className={`query-value ${original == null || original === '' ? 'query-value-special' : ''}`}>
                  {record ? valueText(original) : Object.hasOwn(property, 'default') ? `默认值：${valueText(property.default)}` : '由服务端填充'}
                </div> : <div className="field">
                  {record && <span className="muted query-original">当前值：{valueText(original)}</span>}
                  <select aria-label={`${property.name}的写入方式`} value={draft.mode} disabled={busy}
                    onChange={(event) => updateDraft(property.id, { mode: event.target.value as FieldDrafts[string]['mode'] })}>
                    {record ? <option value="unchanged">保留原值</option> : <option value="omit">{Object.hasOwn(property, 'default') ? '使用默认值' : '不提交此字段'}</option>}
                    <option value="value">填写值</option>
                    {property.nullable && <option value="null">设为空值 NULL</option>}
                  </select>
                  {draft.mode === 'value' && <ValueInput property={property} value={draft.text} disabled={busy} id={inputId}
                    onChange={(text) => updateDraft(property.id, { text })} />}
                  {draft.mode === 'omit' && Object.hasOwn(property, 'default') && <small className="muted">默认值：{valueText(property.default)}</small>}
                  {draft.mode === 'value' && property.dataType === 'timestamp' && <small className="muted">使用带时区的 RFC 3339 原文，支持微秒；不会自动转换为本地时间。</small>}
                  {draft.mode === 'value' && ['int64', 'decimal'].includes(property.dataType) && <small className="muted">以字符串提交，保留完整精度。</small>}
                </div>}
              </div>;
            })}
          </div>
          {confirmDelete && record && <div className="notice notice-error query-delete-confirm" role="alert">
            <p>确认删除此实例？删除后无法在此页面恢复。</p>
            <div className="query-actions"><button className="button query-danger" type="button" disabled={busy} onClick={() => void remove()}><Trash2 size={16} />确认删除</button>
              <button className="button subtle" type="button" disabled={busy} onClick={() => setConfirmDelete(false)}>保留实例</button></div>
          </div>}
          <div className="query-dialog-footer">
            <div>{record && !readOnly && !editing && !confirmDelete && <button type="button" className="button subtle query-danger-text" disabled={busy} onClick={() => { setConfirmDelete(true); setSuccess(''); }}><Trash2 size={16} />删除实例</button>}{readOnly && <span className="muted">映射实例仅供读取</span>}</div>
            <div className="query-actions">
              {record && editing ? <button type="button" className="button" onClick={resetEdit} disabled={busy}>放弃修改</button>
                : <button type="button" className="button" onClick={onClose} disabled={busy}>关闭</button>}
              {editing ? <button className="button primary" type="submit" disabled={busy}>
                {busy ? <LoaderCircle className="query-spin" size={16} /> : <Save size={16} />}{busy ? '正在提交…' : record ? '保存修改' : '创建实例'}
              </button> : record && !readOnly && !confirmDelete && <button className="button primary" type="button" disabled={busy} onClick={() => { setEditing(true); setDrafts(initialDrafts(properties, record.values)); setSuccess(''); }}><Pencil size={16} />编辑实例</button>}
            </div>
          </div>
        </form>}
  </Dialog>;
}
