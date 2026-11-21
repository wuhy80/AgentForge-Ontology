import { useEffect, useId, useState } from 'react';
import type { ApiClient } from '../../lib/api';
import type { QuerySpec, SchemaLink } from '../../lib/types';
import { errorMessage } from './query';

const linkKey = (link: SchemaLink) => `${link.linkRef.id}:${link.direction}`;
export const isManagedRelation = (link: SchemaLink) => link.storageMode === 'managed' && link.hasStorageBinding !== false;

export function RelationFilterEditor({ api, schemaId, value, onChange }: {
  api: ApiClient;
  schemaId: string;
  value: QuerySpec['relation'];
  onChange: (value: QuerySpec['relation']) => void;
}) {
  const id = useId();
  const [links, setLinks] = useState<SchemaLink[]>();
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    api.schemaLinks(schemaId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setLinks(items.filter(isManagedRelation));
    }).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(cause));
    });
    return () => controller.abort();
  }, [api, schemaId]);

  if (links?.length === 0 && !value) return null;
  const selected = value ? `${value.linkId}:${value.direction}` : '';
  const unavailable = !!selected && !links?.some((link) => linkKey(link) === selected);
  return <div className="form-grid">
    <div className="field">
      <label htmlFor={id}>关联筛选</label>
      <select id={id} value={selected} disabled={!links && !value} onChange={(event) => {
        const link = links?.find((item) => linkKey(item) === event.target.value);
        onChange(link ? { linkId: link.linkRef.id, direction: link.direction, operator: value?.operator ?? 'exists' } : undefined);
      }}>
        <option value="">{links || error ? '不限关联' : '正在读取关联…'}</option>
        {unavailable && <option value={selected} disabled>{links || error ? '当前关联不可用' : '正在读取当前关联…'}</option>}
        {links?.map((link) => <option key={linkKey(link)} value={linkKey(link)}>{link.name}（{link.direction === 'out' ? '正向' : '反向'}）</option>)}
      </select>
    </div>
    {value && <div className="field">
      <label htmlFor={`${id}-operator`}>匹配方式</label>
      <select id={`${id}-operator`} value={value.operator} onChange={(event) => {
        onChange({ ...value, operator: event.target.value === 'not_exists' ? 'not_exists' : 'exists' });
      }}>
        <option value="exists">有可见关联</option>
        <option value="not_exists">无可见关联</option>
      </select>
    </div>}
    {error && <p role="alert" className="notice notice-error">关联读取失败：{error}</p>}
  </div>;
}
