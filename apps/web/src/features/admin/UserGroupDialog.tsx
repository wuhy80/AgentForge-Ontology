import { useEffect, useRef, useState } from 'react'
import { LoaderCircle, Save, Search } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiClient, ApiError } from '../../lib/api'
import type { Role, User, UserGroup, UserGroupInput } from '../../lib/types'
import { errorMessage } from '../query/query'

function sameIDs(left: string[], right: string[]) { return JSON.stringify([...left].sort()) === JSON.stringify([...right].sort()) }
function searchKey(value: string) { return value.normalize('NFC').toLocaleLowerCase().trim() }

export function groupAffectsUser(previous: UserGroup | undefined, next: UserGroup, userId: string): boolean {
  if (!previous?.userIds.includes(userId) && !next.userIds.includes(userId)) return false
  return !previous || previous.enabled !== next.enabled || !sameIDs(previous.roleIds, next.roleIds) || !sameIDs(previous.userIds, next.userIds)
}

export function UserGroupDialog({ api, group, roles, users, onClose, onSaved, onForbidden }: {
  api: ApiClient; group?: UserGroup; roles: Role[]; users: User[]; onClose: () => void; onSaved: (group: UserGroup) => void; onForbidden: (error: unknown) => void
}) {
  const [input, setInput] = useState<UserGroupInput>(() => ({ name: group?.name ?? '', description: group?.description ?? '', enabled: group?.enabled ?? true, roleIds: [...group?.roleIds ?? []], userIds: [...group?.userIds ?? []] }))
  const [roleSearch, setRoleSearch] = useState('')
  const [userSearch, setUserSearch] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const changed = !group || input.name.trim() !== group.name || input.description !== group.description || input.enabled !== group.enabled || !sameIDs(input.roleIds, group.roleIds) || !sameIDs(input.userIds, group.userIds)
  const visibleRoles = roles.filter((role) => searchKey(`${role.name} ${role.description}`).includes(searchKey(roleSearch)))
  const visibleUsers = users.filter((user) => searchKey(`${user.displayName} ${user.username}`).includes(searchKey(userSearch)))
  function patch(value: Partial<UserGroupInput>) { setInput((previous) => ({ ...previous, ...value })); if (!conflict) setError('') }
  function toggle(key: 'roleIds' | 'userIds', id: string, checked: boolean) {
    const maximum = key === 'roleIds' ? 64 : 256
    if (checked && input[key].length >= maximum) { setError(key === 'roleIds' ? '每个用户组最多分配 64 个角色。' : '每个用户组最多包含 256 位成员。'); return }
    patch({ [key]: checked ? [...input[key], id] : input[key].filter((value) => value !== id) })
  }
  async function save() {
    if (busy || conflict || !changed) return
    if (!input.name.trim()) { setError('请填写用户组名称。'); return }
    if (input.roleIds.length > 64 || input.userIds.length > 256) { setError('每个用户组最多包含 64 个角色和 256 位成员。'); return }
    if (input.roleIds.some((id) => !roles.some((role) => role.id === id)) || input.userIds.some((id) => !users.some((user) => user.id === id))) { setError('部分角色或成员已不在当前目录中，请关闭后重新读取并核对。'); return }
    setBusy(true); setError('')
    try {
      const body: UserGroupInput = { ...input, name: input.name.trim() }
      const saved = group ? await api.updateUserGroup(group.id, body, group.etag) : await api.createUserGroup(body)
      if (alive.current) onSaved(saved)
    } catch (reason) {
      if (!alive.current) return
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) { onForbidden(reason); return }
      if (reason instanceof ApiError && reason.status === 412) {
        setConflict(true); setError(`${reason.message}。当前输入已保留，请关闭后重新读取，再基于最新用户组编辑。`)
      } else setError(errorMessage(reason))
    } finally { if (alive.current) setBusy(false) }
  }
  return <Dialog title={group ? `编辑用户组 · ${group.name}` : '创建用户组'} description="启用组的成员继承组角色；直接角色保持独立。用户组不授予管理员身份。" onClose={() => { if (!busy) onClose() }} wide>
    <form className="admin-form" onSubmit={(event) => { event.preventDefault(); void save() }}>
      {error && <div role="alert" className="notice notice-error">{error}</div>}
      <div className="form-grid"><label className="field">用户组名称<input value={input.name} maxLength={128} disabled={busy} onChange={(event) => patch({ name: event.target.value })} /></label>
        <label className="field">用户组说明<input value={input.description} disabled={busy} onChange={(event) => patch({ description: event.target.value })} /></label></div>
      <label className="admin-check"><input type="checkbox" checked={input.enabled} disabled={busy} onChange={(event) => patch({ enabled: event.target.checked })} />启用用户组</label>
      <p className="muted admin-help">调整成员、角色或启停状态后，受影响成员需重新登录。用户组独立于模型目录的分类与分组。</p>
      <div className="form-grid admin-group-pickers">
        <fieldset className="admin-fieldset"><legend>用户组角色 · 已选 {input.roleIds.length} / 64</legend>
          <label className="admin-search"><Search size={15} /><span className="admin-sr-only">搜索组角色</span><input aria-label="搜索组角色" type="search" value={roleSearch} disabled={busy} onChange={(event) => setRoleSearch(event.target.value)} placeholder="搜索角色名称" /></label>
          <div className="admin-choice-list">{visibleRoles.map((role) => <label className="admin-choice" key={role.id}><input type="checkbox" aria-label={role.name} checked={input.roleIds.includes(role.id)} disabled={busy || !input.roleIds.includes(role.id) && input.roleIds.length >= 64} onChange={(event) => toggle('roleIds', role.id, event.target.checked)} /><span><strong>{role.name}</strong>{role.description && <small>{role.description}</small>}</span></label>)}
            {!visibleRoles.length && <p className="muted admin-choice-empty">{roles.length ? '没有匹配的角色。' : '尚未创建角色。'}</p>}</div>
        </fieldset>
        <fieldset className="admin-fieldset"><legend>用户组成员 · 已选 {input.userIds.length} / 256</legend>
          <label className="admin-search"><Search size={15} /><span className="admin-sr-only">搜索组成员</span><input aria-label="搜索组成员" type="search" value={userSearch} disabled={busy} onChange={(event) => setUserSearch(event.target.value)} placeholder="搜索显示名称或用户名" /></label>
          <div className="admin-choice-list">{visibleUsers.map((user) => <label className="admin-choice" key={user.id}><input type="checkbox" aria-label={`${user.displayName}（${user.username}）`} checked={input.userIds.includes(user.id)} disabled={busy || !input.userIds.includes(user.id) && input.userIds.length >= 256} onChange={(event) => toggle('userIds', user.id, event.target.checked)} /><span><strong>{user.displayName}</strong><small>{user.username}{!user.enabled ? ' · 已停用' : ''}</small></span></label>)}
            {!visibleUsers.length && <p className="muted admin-choice-empty">{users.length ? '没有匹配的成员。' : '暂无可见用户。'}</p>}</div>
        </fieldset>
      </div>
      <div className="admin-footer"><button type="button" className="button" disabled={busy} onClick={onClose}>取消</button><button type="submit" className="button primary" disabled={busy || conflict || !changed}>{busy ? <LoaderCircle size={16} className="admin-spin" /> : <Save size={16} />}{group ? '保存用户组' : '创建用户组'}</button></div>
    </form>
  </Dialog>
}
