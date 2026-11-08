import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Check, Copy, KeyRound, Link2, LoaderCircle, Plus, RefreshCw, ShieldCheck, Unplug, X } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import { connectionScopes, scopeName, type ConnectionDirectory, type ConnectionScope, type CreatedConnection, type ExternalConnection } from '../../lib/external-auth'
import { Dialog } from '../../components/Dialog'
import './connections.css'

const messageOf = (cause: unknown) => cause instanceof Error ? cause.message : '连接管理失败，请稍后重试。'
const denied = (cause: unknown) => cause instanceof ApiError && (cause.status === 401 || cause.status === 403)
const dateOf = (seconds: number) => Number.isFinite(seconds) ? new Date(seconds * 1000).toLocaleString('zh-CN', { hour12: false }) : '未知'

export function ConnectionsPage({ api }: { api: ApiClient }) {
  const [directory, setDirectory] = useState<ConnectionDirectory>()
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<ConnectionScope[]>(['ontology:discover', 'ontology:query'])
  const [lifetime, setLifetime] = useState(604800)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [created, setCreated] = useState<CreatedConnection>()
  const [revoking, setRevoking] = useState<ExternalConnection>()
  const [copied, setCopied] = useState(false)
  const generation = useRef(0)
  const activeAPI = useRef(api)
  activeAPI.current = api
  const operation = useRef<AbortController | undefined>(undefined)
  const reading = useRef<AbortController | undefined>(undefined)
  const clipboardGeneration = useRef(0)

  const refresh = useCallback(async () => {
    const sequence = ++generation.current
    reading.current?.abort()
    const controller = new AbortController()
    reading.current = controller
    setLoading(true); setError(''); setMessage(''); setCreated(undefined); setRevoking(undefined)
    try {
      const result = await api.listConnections(controller.signal)
      if (sequence !== generation.current || controller.signal.aborted || activeAPI.current !== api) return
      if (!Array.isArray(result.data) || typeof result.resource !== 'string') throw new Error('连接目录响应无效，请刷新重试。')
      setDirectory(result)
    } catch (cause) {
      if (sequence !== generation.current || controller.signal.aborted || activeAPI.current !== api) return
      setDirectory(undefined); setCreated(undefined); setError(messageOf(cause))
    } finally {
      if (sequence === generation.current && !controller.signal.aborted && activeAPI.current === api) setLoading(false)
    }
  }, [api])

  useEffect(() => {
    setDirectory(undefined); setCreated(undefined); setRevoking(undefined); setName(''); setBusy(false)
    operation.current?.abort(); operation.current = undefined
    void refresh()
    return () => { ++generation.current; ++clipboardGeneration.current; reading.current?.abort(); operation.current?.abort(); operation.current = undefined }
  }, [refresh])

  async function mutate(kind: 'create' | 'revoke', event?: FormEvent) {
    event?.preventDefault()
    if (operation.current || loading || !directory || (kind === 'create' && (!name.trim() || !scopes.length)) || (kind === 'revoke' && !revoking)) return
    const controller = new AbortController()
    operation.current = controller
    const current = () => operation.current === controller && !controller.signal.aborted && activeAPI.current === api
    setBusy(true); setError(''); setMessage(''); setCreated(undefined); setCopied(false); ++clipboardGeneration.current
    try {
      if (kind === 'create') {
        const result = await api.createConnection({ name: name.trim(), scopes, expiresInSeconds: lifetime }, controller.signal)
        if (!current()) return
        if (!result.connection?.id || typeof result.accessToken !== 'string' || !result.accessToken || result.accessToken.length > 4096) throw new Error('连接可能已创建，但凭据响应无效，请刷新目录核对后撤销该连接。')
        setDirectory((value) => value && { ...value, data: [...value.data, result.connection] })
        setCreated(result); setName(''); setMessage('连接已创建。请保存本次显示的凭据。')
      } else if (revoking) {
        const id = revoking.id
        await api.revokeConnection(id, controller.signal)
        if (!current()) return
        setDirectory((value) => value && { ...value, data: value.data.map((item) => item.id === id ? { ...item, revokedAt: Math.floor(Date.now() / 1000) } : item) })
        setRevoking(undefined); setMessage('连接已撤销，后续访问将被拒绝。')
      }
    } catch (cause) {
      if (!current()) return
      setError(messageOf(cause)); setCreated(undefined)
      if (denied(cause)) { setDirectory(undefined); setRevoking(undefined); setMessage('') }
    } finally {
      if (current()) { operation.current = undefined; setBusy(false) }
    }
  }

  async function copyToken() {
    if (!created) return
    const sequence = ++clipboardGeneration.current
    try {
      await navigator.clipboard.writeText(created.accessToken)
      if (sequence === clipboardGeneration.current) { setCopied(true); setError('') }
    } catch {
      if (sequence === clipboardGeneration.current) setError('无法访问剪贴板，请手动选择并复制凭据。')
    }
  }

  const config = directory ? `- id: mcp-ontology\n  name: '@deepseek-ai/dsh-mcp-client'\n  config:\n    serverName: ontology\n    transport: streamable-http\n    url: ${JSON.stringify(directory.resource)}\n    expandTemplates: true\n    headers:\n      Authorization: 'Bearer \${credential:ONTOLOGY_MCP_TOKEN}'` : ''

  return <div className="connections-page">
    <header className="connections-heading"><div><span className="eyebrow">CONNECTED APPLICATIONS</span><h1>外部连接</h1><p className="muted">让 DeepSeek Harness 等应用，在你的权限范围内使用当前租户的本体。</p></div><button type="button" className="button" disabled={busy || loading} onClick={() => void refresh()}><RefreshCw size={15} />刷新连接</button></header>
    {error && <div className="notice notice-error" role="alert">{error}</div>}
    {message && <div className="notice notice-success" role="status"><Check size={16} />{message}</div>}
    {loading ? <div className="card empty-state" role="status"><LoaderCircle className="spin" size={26} /><p>正在读取你的连接…</p></div> : !directory ? <div className="card empty-state"><ShieldCheck size={30} /><h2>连接管理暂不可用</h2><p className="muted">刷新后会重新检查当前登录状态和访问权限。</p></div> : <>
      {created && <section className="card connections-secret" aria-labelledby="new-connection-secret">
        <div className="connections-section-heading"><div><KeyRound size={19} /><h2 id="new-connection-secret">保存连接凭据</h2></div><button type="button" className="icon-button" aria-label="关闭凭据显示" onClick={() => { setCreated(undefined); ++clipboardGeneration.current; setCopied(false) }}><X size={18} /></button></div>
        <p>此凭据只显示一次。将它保存到外部应用的安全凭据存储；关闭或离开页面后无法再次读取。</p>
        <label className="field">新连接凭据<textarea readOnly rows={2} spellCheck={false} value={created.accessToken} onFocus={(event) => event.target.select()} /></label>
        <div className="connections-inline"><button type="button" className="button" onClick={() => void copyToken()}>{copied ? <Check size={15} /> : <Copy size={15} />}{copied ? '已复制' : '复制凭据'}</button><span className="muted">到期：{dateOf(created.connection.expiresAt)}</span></div>
      </section>}
      <div className="connections-layout">
        <section className="card connections-create" aria-labelledby="create-connection-title"><div className="connections-section-heading"><div><Plus size={19} /><h2 id="create-connection-title">创建连接</h2></div></div>
          <p className="muted">连接范围只是能力上限，实际访问仍受你的当前业务权限控制。</p>
          <form onSubmit={(event) => void mutate('create', event)}>
            <label className="field">连接名称<input value={name} onChange={(event) => setName(event.target.value)} maxLength={80} placeholder="例如：我的 DeepSeek Harness" required disabled={busy} /></label>
            <fieldset className="connections-scopes" disabled={busy}><legend>允许的能力</legend>{connectionScopes.map((scope) => <label key={scope.value}><input type="checkbox" disabled={scope.value === 'ontology:discover'} checked={scopes.includes(scope.value)} onChange={(event) => setScopes((previous) => event.target.checked ? [...previous, scope.value] : previous.filter((value) => value !== scope.value))} /><span><strong>{scope.name}</strong><small>{scope.description}</small></span></label>)}</fieldset>
            <label className="field">有效期<select value={lifetime} onChange={(event) => setLifetime(Number(event.target.value))} disabled={busy}><option value={3600}>1 小时</option><option value={86400}>1 天</option><option value={604800}>7 天</option><option value={2592000}>30 天</option></select></label>
            <button type="submit" className="button primary" disabled={busy || !name.trim() || !scopes.length}>{busy ? <LoaderCircle className="spin" size={16} /> : <KeyRound size={16} />}创建连接凭据</button>
          </form>
        </section>
        <section className="card connections-guide" aria-labelledby="connection-guide-title"><div className="connections-section-heading"><div><Link2 size={19} /><h2 id="connection-guide-title">连接 DeepSeek Harness</h2></div></div>
          <ol><li>创建连接，并在 dsh 的凭据存储中将凭据保存为 <code>ONTOLOGY_MCP_TOKEN</code>。</li><li>使用已有 MCP 客户端添加下方配置，填写当前可访问的服务地址。</li><li>让智能体先发现可用模型，再查询数据或调用已授权的计算。</li></ol>
          <label className="field">MCP 服务地址<input readOnly value={directory.resource} /></label>
          <pre aria-label="dsh MCP 配置示例">{config}</pre><p className="field-hint">示例仅引用凭据名称，不包含你的密钥。支持浏览器授权的客户端也可从客户端发起 OAuth 连接。</p>
        </section>
      </div>
      <section className="card connections-list" aria-labelledby="my-connections-title"><div className="connections-section-heading"><div><Unplug size={19} /><h2 id="my-connections-title">我的连接</h2></div><span className="badge">{directory.data.length} 个</span></div>
        {!directory.data.length ? <div className="empty-state"><Link2 size={29} /><h3>尚无外部连接</h3><p className="muted">创建连接后，可以随时在这里撤销访问。</p></div> : <ul>{directory.data.map((connection) => {
          const expired = connection.expiresAt <= Math.floor(Date.now() / 1000)
          const status = connection.revokedAt ? '已撤销' : expired ? '已到期' : '有效'
          return <li key={connection.id}><div className="connections-item-title"><strong>{connection.name}</strong><span className={`badge ${status === '有效' ? 'badge-green' : ''}`}>{status}</span></div><div className="connections-item-detail"><span>{connection.kind === 'oauth' ? '浏览器授权' : '连接凭据'}</span><span>{connection.scopes.map(scopeName).join('、')}</span></div><small className="muted">创建于 {dateOf(connection.createdAt)} · 到期 {dateOf(connection.expiresAt)}</small><button type="button" className="button subtle" disabled={busy || !!connection.revokedAt || expired} onClick={() => setRevoking(connection)} aria-label={`撤销连接：${connection.name}`}><Unplug size={14} />撤销连接</button></li>
        })}</ul>}
      </section>
    </>}
    {revoking && <Dialog title="撤销外部连接" description={`撤销“${revoking.name}”后，此连接将无法继续访问当前租户。`} onClose={() => setRevoking(undefined)} closeDisabled={busy}><div className="dialog-actions"><button type="button" className="button" disabled={busy} onClick={() => setRevoking(undefined)}>取消</button><button type="button" className="button danger" disabled={busy} onClick={() => void mutate('revoke')}>{busy && <LoaderCircle className="spin" size={15} />}确认撤销</button></div></Dialog>}
  </div>
}
