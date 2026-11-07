import { useEffect, useRef, useState } from 'react'
import { ClipboardList, KeyRound, LoaderCircle, Pencil, Plus, RefreshCw, ShieldCheck, Users, UsersRound } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import type { AuditRecord, DefinitionRecord, Organization, PolicyRecord, Role, Session, User, UserGroup } from '../../lib/types'
import { errorMessage } from '../query/query'
import { RoleDialog, UserDialog } from './IdentityDialogs'
import { PolicyPanel } from './PolicyPanel'
import { groupAffectsUser, UserGroupDialog } from './UserGroupDialog'
import { operationName } from './policy'
import './admin.css'

type Props = { api: ApiClient; session: Session; categories: Organization[]; definitions: DefinitionRecord[]; onSessionChanged?: () => void }
const tabs = [{ id: 'users', name: '用户', icon: Users }, { id: 'groups', name: '用户组', icon: UsersRound }, { id: 'roles', name: '角色', icon: KeyRound }, { id: 'policy', name: '权限策略', icon: ShieldCheck }, { id: 'audit', name: '审计记录', icon: ClipboardList }] as const

export function AdminPage({ api, session, categories, definitions, onSessionChanged }: Props) {
  const [tab, setTab] = useState<(typeof tabs)[number]['id']>('users')
  const [users, setUsers] = useState<User[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [groups, setGroups] = useState<UserGroup[]>([])
  const [policy, setPolicy] = useState<PolicyRecord>()
  const [audit, setAudit] = useState<AuditRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [revision, setRevision] = useState(0)
  const [denied, setDenied] = useState(false)
  const [userDialog, setUserDialog] = useState<{ user?: User }>()
  const [roleDialog, setRoleDialog] = useState<{ role?: Role }>()
  const [groupDialog, setGroupDialog] = useState<{ group?: UserGroup }>()
  const request = useRef<AbortController | undefined>(undefined)
  function clear() { setUsers([]); setRoles([]); setGroups([]); setPolicy(undefined); setAudit([]); setUserDialog(undefined); setRoleDialog(undefined); setGroupDialog(undefined) }
  function forbidden(reason: unknown) {
    request.current?.abort(); clear(); setLoading(false); setDenied(true); setError(errorMessage(reason)); setMessage('')
  }
  useEffect(() => {
    clear(); setError(''); setDenied(false)
    if (!session.user.administrator) { setLoading(false); return }
    const controller = new AbortController(); request.current = controller
    setLoading(true)
    Promise.all([api.listUsers(controller.signal), api.listRoles(controller.signal), api.listUserGroups(controller.signal), api.getPolicy(controller.signal), api.listAudit(controller.signal)]).then(([users, roles, groups, policy, audit]) => {
      if (controller.signal.aborted) return
      setUsers(users ?? []); setRoles(roles ?? []); setGroups(groups ?? []); setPolicy(policy); setAudit(audit ?? [])
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) forbidden(reason)
      else setError(errorMessage(reason))
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
    // The session identity controls workspace lifetime; display-name refreshes do not discard edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, session.user.id, session.user.administrator, revision])

  function refresh() { setRevision((value) => value + 1); setMessage('') }
  if (!session.user.administrator) return <div className="card empty-state"><ShieldCheck size={32} /><h2>此页面仅向租户管理员开放</h2><p className="muted">请联系当前租户管理员管理账户与角色。</p></div>
  return <div className="admin-page"><header className="admin-page-heading"><div><div className="admin-eyebrow">ACCESS MANAGEMENT</div><h1>用户与权限</h1><p className="muted">管理本租户的用户、用户组、角色与业务访问规则。</p></div><span className="badge"><ShieldCheck size={14} />租户管理员</span></header>
    <div className="admin-tabs" role="tablist" aria-label="用户与权限模块">{tabs.map(({ id, name, icon: Icon }) => <button key={id} role="tab" type="button" aria-selected={tab === id} aria-controls={`admin-${id}`} id={`admin-tab-${id}`} className={tab === id ? 'is-active' : ''} onClick={() => { setTab(id); setMessage('') }}><Icon size={16} />{name}</button>)}</div>
    {error && <div className="notice notice-error" role="alert">{error}</div>}{message && <div className="notice notice-success" role="status">{message}</div>}
    <div className="admin-toolbar"><span className="muted">{loading ? '正在读取管理数据…' : denied ? '授权已拒绝，管理数据已清除' : !policy ? '管理数据未能完整读取，请重试' : tab === 'users' ? `当前租户 ${users.length} 位用户` : tab === 'groups' ? `${groups.length} 个用户组` : tab === 'roles' ? `${roles.length} 个角色` : tab === 'policy' ? '策略草稿与当前生效规则分别维护' : '最近的账户、用户组与策略操作'}</span><button type="button" className="button subtle" disabled={loading} onClick={refresh}><RefreshCw size={15} />重新读取</button></div>
    {loading ? <div className="card empty-state"><LoaderCircle size={28} className="admin-spin" /><p className="muted">正在读取用户、用户组、角色与策略…</p></div>
      : denied ? <div className="card empty-state"><ShieldCheck size={30} /><h2>当前身份无权读取管理数据</h2><p className="muted">重新读取将再次向服务端确认授权。</p></div>
        : !policy ? <div className="card empty-state"><RefreshCw size={28} /><h2>管理数据读取失败</h2><p className="muted">重新读取后可继续管理用户与权限。</p></div> : <>
          <section id="admin-users" role="tabpanel" aria-labelledby="admin-tab-users" hidden={tab !== 'users'} className="card"><div className="admin-section-heading"><div><Users size={18} /><h2>租户用户</h2></div><button type="button" className="button primary" onClick={() => setUserDialog({})}><Plus size={16} />创建用户</button></div>
            {!users.length ? <div className="empty-state"><Users size={30} /><h3>暂无可见用户</h3></div> : <div className="table-wrap"><table className="admin-table"><thead><tr><th>用户</th><th>账户状态</th><th>直接角色</th><th>用户组来源</th><th>管理身份</th><th>操作</th></tr></thead><tbody>{users.map((user) => {
              const memberships = groups.filter((group) => group.userIds.includes(user.id))
              return <tr key={user.id}><td><strong>{user.displayName}</strong><small>{user.username}{user.id === session.user.id ? ' · 我' : ''}</small></td><td><span className={`badge ${user.enabled ? 'admin-badge-allow' : 'admin-badge-deny'}`}>{user.enabled ? '已启用' : '已停用'}</span></td><td><div className="admin-inline">{user.roleIds?.length ? user.roleIds.map((id) => <span className="badge" key={id}>{roles.find((role) => role.id === id)?.name ?? '不可见角色'}</span>) : <span className="muted">未分配直接角色</span>}</div></td>
                <td><div className="admin-membership-list">{memberships.length ? memberships.map((group) => <div key={group.id}><span className="badge">{group.name}{!group.enabled ? ' · 已停用' : ''}</span><small>{!group.enabled ? '不继承组角色' : `组角色：${group.roleIds.map((id) => roles.find((role) => role.id === id)?.name ?? '不可见角色').join('、') || '未分配角色'}`}</small></div>) : <span className="muted">未加入用户组</span>}</div></td>
                <td>{user.administrator ? '租户管理员' : '普通用户'}</td><td><button type="button" className="button subtle" onClick={() => setUserDialog({ user })}><Pencil size={14} />编辑用户</button></td></tr>
            })}</tbody></table></div>}
          </section>
          <section id="admin-groups" role="tabpanel" aria-labelledby="admin-tab-groups" hidden={tab !== 'groups'} className="card"><div className="admin-section-heading"><div><UsersRound size={18} /><h2>租户用户组</h2></div><button type="button" className="button primary" onClick={() => setGroupDialog({})}><Plus size={16} />创建用户组</button></div>
            {!groups.length ? <div className="empty-state"><UsersRound size={30} /><h3>尚未创建用户组</h3><p className="muted">按团队维护成员与角色，启用组的成员会继承组角色。</p></div> : <div className="table-wrap"><table className="admin-table"><thead><tr><th>用户组</th><th>状态</th><th>分配角色</th><th>成员</th><th>操作</th></tr></thead><tbody>{groups.map((group) => <tr key={group.id}><td><strong>{group.name}</strong><small>{group.description || '暂无说明'}</small></td><td><span className={`badge ${group.enabled ? 'admin-badge-allow' : 'admin-badge-deny'}`}>{group.enabled ? '已启用' : '已停用'}</span></td><td><div className="admin-inline">{group.roleIds.length ? group.roleIds.map((id) => <span className="badge" key={id}>{roles.find((role) => role.id === id)?.name ?? '不可见角色'}</span>) : <span className="muted">未分配角色</span>}</div></td><td><span>{group.userIds.length} 位成员</span><small className="admin-group-members">{group.userIds.map((id) => users.find((user) => user.id === id)?.displayName ?? '不可见用户').join('、') || '暂无成员'}</small></td><td><button type="button" className="button subtle" onClick={() => setGroupDialog({ group })}><Pencil size={14} />编辑用户组</button></td></tr>)}</tbody></table></div>}
          </section>
          <section id="admin-roles" role="tabpanel" aria-labelledby="admin-tab-roles" hidden={tab !== 'roles'} className="card"><div className="admin-section-heading"><div><KeyRound size={18} /><h2>租户角色</h2></div><button type="button" className="button primary" onClick={() => setRoleDialog({})}><Plus size={16} />创建角色</button></div>
            {!roles.length ? <div className="empty-state"><KeyRound size={30} /><h3>尚未创建角色</h3><p className="muted">创建角色后，可分配用户并建立权限规则。</p></div> : <div className="admin-role-grid">{roles.map((role) => <article className="admin-role-card" key={role.id}><div className="admin-inline"><span className="admin-role-icon"><KeyRound size={19} /></span><h3>{role.name}</h3><button type="button" className="icon-button" aria-label={`编辑角色：${role.name}`} onClick={() => setRoleDialog({ role })}><Pencil size={15} /></button></div><p className="muted">{role.description || '暂无角色说明'}</p><small className="muted">直接分配：{users.filter((user) => user.roleIds?.includes(role.id)).length} 位用户 · {groups.filter((group) => group.roleIds.includes(role.id)).length} 个用户组（含停用组）</small></article>)}</div>}
          </section>
          <section id="admin-policy" role="tabpanel" aria-labelledby="admin-tab-policy" hidden={tab !== 'policy'}>{policy && <PolicyPanel key={`${policy.etag}:${policy.state}`} api={api} record={policy} roles={roles} users={users} groups={groups} definitions={definitions} categories={categories} onRecord={(record) => { setPolicy(record); setMessage(record.draft ? '策略草稿已保存；可先模拟验证，再使其生效。' : '权限策略已生效。') }} onReload={refresh} onForbidden={forbidden} />}</section>
          <section id="admin-audit" role="tabpanel" aria-labelledby="admin-tab-audit" hidden={tab !== 'audit'} className="card"><div className="admin-section-heading"><div><ClipboardList size={18} /><h2>最近审计记录</h2></div><span className="muted">仅展示最近 200 条，不包含密码或凭证</span></div>
            {!audit.length ? <div className="empty-state"><ClipboardList size={30} /><h3>暂无可见审计记录</h3></div> : <div className="table-wrap"><table className="admin-table"><thead><tr><th>操作时间</th><th>操作人</th><th>操作</th><th>目标</th></tr></thead><tbody>{audit.map((entry) => <tr key={entry.id}><td>{entry.createdAt}</td><td>{users.find((user) => user.id === entry.actorId)?.displayName ?? '不可见用户'}</td><td>{auditLabel(entry.operation)}</td><td>{users.find((user) => user.id === entry.targetId)?.displayName ?? roles.find((role) => role.id === entry.targetId)?.name ?? groups.find((group) => group.id === entry.targetId)?.name ?? (entry.targetId === session.user.tenantId ? '租户策略' : '管理资源')}</td></tr>)}</tbody></table></div>}
          </section>
        </>}
    {userDialog && <UserDialog api={api} user={userDialog.user} roles={roles} onClose={() => setUserDialog(undefined)} onForbidden={forbidden} onSaved={(user) => { setUsers((previous) => previous.some((item) => item.id === user.id) ? previous.map((item) => item.id === user.id ? user : item) : [...previous, user]); setUserDialog(undefined); setMessage('用户已保存。'); if (user.id === session.user.id) onSessionChanged?.() }} />}
    {roleDialog && <RoleDialog api={api} role={roleDialog.role} onClose={() => setRoleDialog(undefined)} onForbidden={forbidden} onSaved={(role) => { setRoles((previous) => previous.some((item) => item.id === role.id) ? previous.map((item) => item.id === role.id ? role : item) : [...previous, role]); setRoleDialog(undefined); setMessage('角色已保存。') }} />}
    {groupDialog && <UserGroupDialog api={api} group={groupDialog.group} roles={roles} users={users} onClose={() => setGroupDialog(undefined)} onForbidden={forbidden} onSaved={(group) => { const affectsCurrentUser = groupAffectsUser(groupDialog.group, group, session.user.id); setGroups((previous) => previous.some((item) => item.id === group.id) ? previous.map((item) => item.id === group.id ? group : item) : [...previous, group]); setGroupDialog(undefined); setMessage('用户组已保存。'); if (affectsCurrentUser) onSessionChanged?.() }} />}
  </div>
}

function auditLabel(operation: string) {
  const labels: Record<string, string> = { 'user.create': '创建用户', 'user.update': '更新用户', 'role.create': '创建角色', 'role.update': '更新角色', 'user_group.create': '创建用户组', 'user_group.update': '更新用户组', 'policy.save_draft': '保存策略草稿', 'policy.publish': '策略生效', 'policy.simulate': '模拟授权', 'identity.bootstrap': '初始化管理员', 'auth.login': '用户登录', 'auth.logout': '用户退出', 'auth.password': '修改密码' }
  return labels[operation] ?? operationName(operation)
}
