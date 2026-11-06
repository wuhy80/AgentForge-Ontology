import { useEffect, useState } from 'react'
import { Check, FolderPlus, Layers2, LoaderCircle, Plus } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import type { ApiClient } from '../../lib/api'
import type { Organization } from '../../lib/types'

export function OrganizationsDialog({ api, categories, onClose, onRefresh }: {
  api: ApiClient
  categories: Organization[]
  onClose: () => void
  onRefresh: () => Promise<void>
}) {
  const [localCategories, setLocalCategories] = useState(categories)
  const [categoryId, setCategoryId] = useState(categories[0]?.id ?? '')
  const [categoryName, setCategoryName] = useState('')
  const [groupName, setGroupName] = useState('')
  const [groups, setGroups] = useState<Organization[]>([])
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    if (!categoryId) return
    const controller = new AbortController()
    setGroups([]); setLoading(true); setError('')
    api.listGroups(categoryId, controller.signal).then(setGroups).catch((cause: unknown) => {
      if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : '分组读取失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [api, categoryId, revision])
  async function create(kind: 'category' | 'group') {
    const name = (kind === 'category' ? categoryName : groupName).trim()
    if (!name) { setError('请填写业务名称。'); return }
    setBusy(true); setError(''); setMessage('')
    try {
      if (kind === 'category') {
        const category = await api.createCategory(name)
        setLocalCategories((value) => [...value, category]); setCategoryId(category.id); setCategoryName('')
      } else {
        await api.createGroup(categoryId, name); setGroupName(''); setRevision((value) => value + 1)
      }
      setMessage(kind === 'category' ? '分类已创建，接下来为它添加分组。' : '分组已创建，可以在其中建立业务定义。')
      try { await onRefresh() } catch { setMessage('组织已创建，目录刷新失败；可稍后刷新目录。') }
    } catch (cause) { setError(cause instanceof Error ? cause.message : '创建失败，请稍后重试。') } finally { setBusy(false) }
  }
  return <Dialog title="管理分类与分组" description="用分类确定业务名称空间，再以分组组织五类定义。" onClose={() => { if (!busy) onClose() }}>
    <div className="model-organization-editor">
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
      <section><h3><FolderPlus size={18} />创建分类</h3><form className="model-inline-create" onSubmit={(event) => { event.preventDefault(); void create('category') }}><label className="model-grow">分类名称<input className="field" maxLength={128} value={categoryName} disabled={busy} onChange={(event) => setCategoryName(event.target.value)} placeholder="例如：生产管理" /></label><button className="button" disabled={busy || !categoryName.trim()} type="submit"><Plus size={16} />创建分类</button></form></section>
      <section><h3><Layers2 size={18} />分类下的分组</h3><label>选择分类<select className="field" value={categoryId} disabled={busy} onChange={(event) => setCategoryId(event.target.value)}><option value="">选择分类</option>{localCategories.map((category) => <option key={category.id} value={category.id}>{category.name}</option>)}</select></label>
        {loading ? <p className="muted"><LoaderCircle size={16} className="model-spin" />正在读取分组…</p> : groups.length ? <div className="model-group-list">{groups.map((group) => <span className="badge" key={group.id}>{group.name}</span>)}</div> : <p className="model-inline-empty">{categoryId ? '当前分类还没有分组。' : '先创建或选择一个分类。'}</p>}
        <form className="model-inline-create" onSubmit={(event) => { event.preventDefault(); void create('group') }}><label className="model-grow">新分组名称<input className="field" disabled={busy || !categoryId} maxLength={128} value={groupName} onChange={(event) => setGroupName(event.target.value)} placeholder="例如：基础资料" /></label><button className="button primary" disabled={busy || !categoryId || !groupName.trim()} type="submit"><Plus size={16} />创建分组</button></form>
      </section>
      <div className="model-editor-footer"><span className="muted">分类内五类定义共享业务名称。</span><button className="button subtle" type="button" disabled={busy} onClick={onClose}>完成</button></div>
    </div>
  </Dialog>
}
