import { useEffect, useMemo, useRef, useState } from 'react'
import { ArrowLeft, ArrowRight, Check, LoaderCircle, ShieldCheck } from 'lucide-react'
import { ApiClient, ApiError } from '../../lib/api'
import { connectionScopes, oauthResultURL, parseOAuthAuthorization, validateOAuthPreview, type OAuthPreview } from '../../lib/external-auth'
import type { Session } from '../../lib/types'
import './connections.css'

export function OAuthConsentPage({ api, account, route, onDisconnect, disconnecting, onNavigate = (url) => window.location.assign(url) }: { api: ApiClient; account: Session; route: string; onDisconnect: () => Promise<void>; disconnecting: boolean; onNavigate?: (url: string) => void }) {
  const request = useMemo(() => { try { return { input: parseOAuthAuthorization(route) } } catch (cause) { return { error: cause instanceof Error ? cause.message : '授权链接无效。' } } }, [route])
  const [preview, setPreview] = useState<OAuthPreview>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [blocked, setBlocked] = useState(false)
  const operation = useRef<AbortController | undefined>(undefined)
  const generation = useRef(0)
  const activeContext = useRef({ api, route })
  activeContext.current = { api, route }
  useEffect(() => {
    const sequence = ++generation.current, controller = new AbortController()
    operation.current?.abort(); operation.current = undefined
    setPreview(undefined); setError(''); setLoading(true); setBusy(false); setBlocked(false)
    if (!request.input) { setLoading(false); return }
    void api.previewOAuth(request.input, controller.signal).then((result) => {
      if (controller.signal.aborted || sequence !== generation.current) return
      setPreview(validateOAuthPreview(request.input!, result))
    }).catch((cause) => {
      if (!controller.signal.aborted && sequence === generation.current) setError(cause instanceof Error ? cause.message : '无法核对应用授权请求。')
    }).finally(() => { if (!controller.signal.aborted && sequence === generation.current) setLoading(false) })
    return () => { ++generation.current; controller.abort(); operation.current?.abort(); operation.current = undefined }
  }, [api, request])

  async function decide(approve: boolean) {
    if (!preview || !request.input || operation.current || blocked) return
    const controller = new AbortController()
    operation.current = controller
    const current = () => operation.current === controller && !controller.signal.aborted && activeContext.current.api === api && activeContext.current.route === route
    setBusy(true); setError('')
    try {
      const result = await api.authorizeOAuth(request.input, approve, controller.signal)
      if (!current()) return
      const destination = oauthResultURL(request.input, result.redirectUri)
      setPreview(undefined); setBlocked(true)
      onNavigate(destination)
    } catch (cause) {
      if (!current()) return
      setError(cause instanceof Error ? cause.message : '授权处理失败，请从应用重新发起连接。')
      // A completed code issuance cannot be safely repeated after an uncertain
      // result. Require a fresh authorization request instead of auto-approving.
      if (!(cause instanceof ApiError) || cause.status !== 422) { setPreview(undefined); setBlocked(true) }
    } finally {
      if (current()) { operation.current = undefined; setBusy(false) }
    }
  }

  return <main className="oauth-page"><div className="oauth-brand">AgentForge <span>ONTOLOGY</span></div><section className="card oauth-card" aria-labelledby="oauth-title">
    <div className="oauth-emblem"><ShieldCheck size={27} /></div><span className="eyebrow">APPLICATION AUTHORIZATION</span><h1 id="oauth-title">授权外部应用</h1>
    <p className="muted">当前账号：<strong>{account.user.displayName || account.user.username}</strong><br />租户：{account.user.tenantId}</p>
    {(error || request.error) && <div className="notice notice-error" role="alert">{request.error || error}</div>}
    {loading ? <div className="empty-state" role="status"><LoaderCircle className="spin" size={25} /><p>正在核对应用和访问范围…</p></div> : preview && request.input ? <>
      <div className="oauth-client"><strong>{preview.client.name}</strong><span>希望连接你的本体工作空间</span></div>
      <dl className="oauth-destinations"><div><dt>应用标识</dt><dd>{preview.client.clientId}</dd></div><div><dt>授权返回地址</dt><dd>{request.input.redirect_uri}</dd></div><div><dt>访问服务</dt><dd>{preview.resource}</dd></div></dl>
      <h2>应用请求以下能力</h2><ul className="oauth-scope-list">{preview.scopes.map((scope) => { const item = connectionScopes.find((option) => option.value === scope)!; return <li key={scope}><Check size={17} /><div><strong>{item.name}</strong><p className="muted">{item.description}</p></div></li> })}</ul>
      <p className="oauth-boundary">应用获得的访问范围不会超过你的当前业务权限。你可以随时在“外部连接”撤销本次授权。</p>
      <div className="oauth-actions"><button type="button" className="button" disabled={busy || disconnecting} onClick={() => void decide(false)}>拒绝</button><button type="button" className="button primary" disabled={busy || disconnecting} onClick={() => void decide(true)}>{busy ? <LoaderCircle className="spin" size={16} /> : <ArrowRight size={16} />}允许连接</button></div>
    </> : <p className="muted">没有有效的待授权请求。请回到外部应用重新发起连接。</p>}
    <div className="oauth-footer"><a href="#connections"><ArrowLeft size={14} />返回我的连接</a><button type="button" className="button subtle" disabled={busy || disconnecting} onClick={() => void onDisconnect()}>切换账号</button></div>
  </section></main>
}
