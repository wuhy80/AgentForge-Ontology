import { useEffect, useRef, useState } from 'react'
import { LoaderCircle, Save } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiClient, ApiError } from '../../lib/api'
import type { Role, RoleInput, User, UserInput } from '../../lib/types'
import { errorMessage } from '../query/query'

function editError(error: unknown): string {
  return error instanceof ApiError && error.status === 412
    ? `${error.message}。记录已被修改；当前输入已保留，请关闭后重新读取，再基于最新记录编辑。`
    : errorMessage(error)
}

export function UserDialog({ api, user, roles, onClose, onSaved, onForbidden }: { api: ApiClient; user?: User; roles: Role[]; onClose: () => void; onSaved: (user: User) => void; onForbidden: (error: unknown) => void }) {
  const [input, setInput] = useState<UserInput>(() => ({ username: user?.username ?? '', displayName: user?.displayName ?? '', password: '', enabled: user?.enabled ?? true, administrator: user?.administrator ?? false, roleIds: user?.roleIds ?? [] }))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  function patch(value: Partial<UserInput>) { setInput((previous) => ({ ...previous, ...value })); if (!conflict) setError('') }
  const changed = !user || input.displayName !== user.displayName || input.enabled !== user.enabled || input.administrator !== user.administrator || Boolean(input.password) || JSON.stringify([...input.roleIds].sort()) !== JSON.stringify([...user.roleIds].sort())
  async function save() {
    if (!input.username.trim() || !input.displayName.trim() || !user && !input.password) { setError('请填写用户名、显示名称及新用户密码。'); return }
    if (input.password && (Array.from(input.password).length < 15 || new TextEncoder().encode(input.password).length > 72)) { setError('密码至少15个字符，且不能超过72个UTF-8字节。'); return }
    if (busy || conflict || !changed) return
    setBusy(true); setError('')
    try {
      const body: UserInput = { ...input, displayName: input.displayName.trim(), username: input.username.trim() }
      if (!body.password) delete body.password
      const saved = user ? await api.updateUser(user.id, body, user.etag) : await api.createUser(body)
      if (alive.current) onSaved(saved)
    } catch (reason) {
      if (!alive.current) return
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) { onForbidden(reason); return }
      setError(editError(reason)); if (reason instanceof ApiError && reason.status === 412) setConflict(true)
    } finally { if (alive.current) setBusy(false) }
  }
  return <Dialog title={user ? `编辑用户 · ${user.displayName}` : '创建用户'} description="账户仅属于当前租户。密码、角色或账户状态调整后，该用户需要重新登录。" onClose={() => { if (!busy) onClose() }}>
    <form className="admin-form" onSubmit={(event) => { event.preventDefault(); void save() }}>
      {error && <div role="alert" className="notice notice-error">{error}</div>}
      <div className="form-grid"><label className="field">用户名<input value={input.username} onChange={(event) => patch({ username: event.target.value })} disabled={busy || Boolean(user)} autoComplete="off" maxLength={128} /></label>
        <label className="field">显示名称<input value={input.displayName} onChange={(event) => patch({ displayName: event.target.value })} disabled={busy} maxLength={128} /></label></div>
      <label className="field">{user ? '重置密码（留空则保留原密码）' : '初始密码'}<input type="password" value={input.password} onChange={(event) => patch({ password: event.target.value })} disabled={busy} autoComplete="new-password" minLength={15} maxLength={72} /></label>
      <small className="muted">密码至少15个字符，最多72个UTF-8字节；建议使用较长的口令短语。</small>
      <div className="admin-inline"><label className="admin-check"><input type="checkbox" checked={input.enabled} disabled={busy} onChange={(event) => patch({ enabled: event.target.checked })} />启用账户</label>
        <label className="admin-check"><input type="checkbox" checked={input.administrator} disabled={busy} onChange={(event) => patch({ administrator: event.target.checked })} />租户管理员</label></div>
      <fieldset className="admin-fieldset"><legend>分配角色</legend>{roles.length ? <div className="admin-field-checks">{roles.map((role) => <label className="admin-check" key={role.id}><input type="checkbox" checked={input.roleIds.includes(role.id)} disabled={busy} onChange={(event) => patch({ roleIds: event.target.checked ? [...input.roleIds, role.id] : input.roleIds.filter((id) => id !== role.id) })} />{role.name}</label>)}</div> : <p className="muted">尚未创建角色，可稍后分配。</p>}</fieldset>
      {input.administrator && <p className="notice">管理员可管理本租户账户与策略。业务访问仍由服务端执行权限判断。</p>}
      <div className="admin-footer"><button type="button" className="button" disabled={busy} onClick={onClose}>取消</button><button type="submit" className="button primary" disabled={busy || conflict || !changed}>{busy ? <LoaderCircle className="admin-spin" size={16} /> : <Save size={16} />}{user ? '保存用户' : '创建用户'}</button></div>
    </form>
  </Dialog>
}

export function RoleDialog({ api, role, onClose, onSaved, onForbidden }: { api: ApiClient; role?: Role; onClose: () => void; onSaved: (role: Role) => void; onForbidden: (error: unknown) => void }) {
  const [input, setInput] = useState<RoleInput>({ name: role?.name ?? '', description: role?.description ?? '' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const changed = !role || input.name.trim() !== role.name || input.description !== role.description
  async function save() {
    if (!input.name.trim()) { setError('请填写角色名称。'); return }
    if (busy || conflict || !changed) return
    setBusy(true); setError('')
    try {
      const body = { ...input, name: input.name.trim() }
      const saved = role ? await api.updateRole(role.id, body, role.etag) : await api.createRole(body)
      if (alive.current) onSaved(saved)
    } catch (reason) {
      if (!alive.current) return
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) { onForbidden(reason); return }
      setError(editError(reason)); if (reason instanceof ApiError && reason.status === 412) setConflict(true)
    } finally { if (alive.current) setBusy(false) }
  }
  return <Dialog title={role ? '编辑角色' : '创建角色'} description="角色名称用于管理与分配，权限规则绑定稳定角色身份。" onClose={() => { if (!busy) onClose() }}>
    <form className="admin-form" onSubmit={(event) => { event.preventDefault(); void save() }}>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <label className="field">角色名称<input value={input.name} maxLength={128} disabled={busy} onChange={(event) => { setInput({ ...input, name: event.target.value }); if (!conflict) setError('') }} /></label>
      <label className="field">角色说明<textarea rows={3} value={input.description} disabled={busy} onChange={(event) => { setInput({ ...input, description: event.target.value }); if (!conflict) setError('') }} /></label>
      <div className="admin-footer"><button type="button" className="button" disabled={busy} onClick={onClose}>取消</button><button type="submit" className="button primary" disabled={busy || conflict || !changed}>{busy ? <LoaderCircle size={16} className="admin-spin" /> : <Save size={16} />}{role ? '保存角色' : '创建角色'}</button></div>
    </form>
  </Dialog>
}
