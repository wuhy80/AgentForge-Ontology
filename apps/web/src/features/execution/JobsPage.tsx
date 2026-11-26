import { useCallback, useEffect, useRef, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { Dialog } from '../../components/Dialog'
import { ApiError, type ApiClient } from '../../lib/api'
import type { Invocation, InvocationSummary } from '../../lib/types'
import { errorMessage } from '../query/query'
import '../connections/connections.css'

const labels: Record<Invocation['status'], string> = {
  queued: '排队中', running: '运行中', succeeded: '成功', failed: '失败',
  timed_out: '超时', canceled: '已取消', interrupted: '中断', committing: '确认提交中',
}

export function JobsPage({ api }: { api: ApiClient }) {
  const [items, setItems] = useState<InvocationSummary[]>([])
  const [cursor, setCursor] = useState<string>()
  const [busy, setBusy] = useState(true)
  const [visible, setVisible] = useState(() => document.visibilityState === 'visible')
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<Invocation>()
  const [cancel, setCancel] = useState<InvocationSummary>()
  const current = useRef<AbortController | undefined>(undefined)
  const poll = useRef<AbortController | undefined>(undefined)

  const perform = useCallback(async (action: (signal: AbortSignal) => Promise<void>) => {
    current.current?.abort()
    poll.current?.abort()
    const request = new AbortController()
    current.current = request
    setBusy(true)
    setError('')
    try {
      await action(request.signal)
    } catch (cause) {
      if (request.signal.aborted) return
      if (cause instanceof ApiError && cause.status === 401) {
        setItems([])
        setCursor(undefined)
      }
      setSelected(undefined)
      setError(errorMessage(cause))
    } finally {
      if (!request.signal.aborted) setBusy(false)
    }
  }, [])

  const list = useCallback(async (signal: AbortSignal, after?: string) => {
    const page = await api.listInvocations(after, signal)
    if (signal.aborted) return
    setItems((previous) => after ? [...previous, ...page.data.filter((item) => !previous.some((old) => old.id === item.id))] : page.data)
    setCursor(page.nextCursor)
  }, [api])

  useEffect(() => {
    setItems([])
    setSelected(undefined)
    setCancel(undefined)
    setCursor(undefined)
    void perform((signal) => list(signal))
    return () => current.current?.abort()
  }, [list, perform])

  useEffect(() => {
    const update = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  const hasPending = items.some(({ status }) => status === 'queued' || status === 'running')
  useEffect(() => {
    if (!visible || !hasPending || busy || cancel) return
    const request = new AbortController()
    poll.current = request
    let inFlight = false
    const timer = window.setInterval(async () => {
      if (inFlight) return
      inFlight = true
      try {
        const page = await api.listInvocations(undefined, request.signal)
        const result = selected && ['queued', 'running'].includes(selected.status)
          ? await api.getInvocation(selected.id, request.signal) : undefined
        if (request.signal.aborted) return
        setItems((previous) => {
          const latest = new Map(page.data.map((item) => [item.id, item]))
          return previous.map((item) => latest.get(item.id) ?? item)
        })
        if (result) setSelected(result)
      } catch (cause) {
        if (request.signal.aborted) return
        if (cause instanceof ApiError && [401, 403, 404, 409].includes(cause.status)) {
          setSelected(undefined)
          if (cause.status === 401) { setItems([]); setCursor(undefined) }
        }
        setError(errorMessage(cause))
      } finally {
        inFlight = false
      }
    }, 5000)
    return () => { request.abort(); window.clearInterval(timer) }
  }, [api, busy, cancel, hasPending, selected, visible])

  function load(id: string) {
    setSelected(undefined)
    void perform(async (signal) => {
      const result = await api.getInvocation(id, signal)
      if (!signal.aborted) setSelected(result)
    })
  }

  function cancelJob(item: InvocationSummary) {
    void perform(async (signal) => {
      const result = await api.cancelInvocation(item.id, signal)
      if (signal.aborted) return
      setCancel(undefined)
      setItems((previous) => previous.map((row) => row.id === item.id ? { ...row, status: result.status, finishedAt: result.finishedAt } : row))
      if (selected?.id === item.id) setSelected(result)
    })
  }

  return <section className="connections-page" aria-label="我的任务">
    {error && <p className="notice notice-error" role="alert">{error}</p>}
    {busy && <p className="muted" role="status">正在读取或更新任务…</p>}
    <section className="card connections-list" aria-labelledby="job-records">
      <div className="connections-section-heading"><h2 id="job-records">任务记录</h2><button type="button" className="button" disabled={busy} onClick={() => void perform((signal) => list(signal))}><RefreshCw size={15} />刷新任务</button></div>
      {!items.length && !busy && !error && <p className="empty-state">提交后台运行后，任务会显示在这里。</p>}
      <ul>{items.map((item) => <li key={item.id}>
        <div className="connections-item-title"><strong>{item.definitionName}</strong><span className="badge">{labels[item.status]}</span></div>
        <div className="connections-item-detail"><span>提交于 {new Date(item.createdAt).toLocaleString()}</span><span>尝试 {item.attempt} 次</span></div>
        <div className="connections-inline">
          <button type="button" className="button subtle" disabled={busy} onClick={() => load(item.id)}>查看详情</button>
          {['queued', 'running'].includes(item.status) && <button type="button" className="button subtle" disabled={busy} onClick={() => setCancel(item)}>取消任务</button>}
        </div>
      </li>)}</ul>
      {cursor && <button type="button" className="button" disabled={busy} onClick={() => void perform((signal) => list(signal, cursor))}>加载更多</button>}
    </section>
    {selected && <Dialog title={selected.definitionName} description={labels[selected.status]} onClose={() => setSelected(undefined)}>
      <p>尝试 {selected.attempt ?? 0} 次</p>
      {selected.error && <p role="status">{selected.status === 'queued' ? '等待重试。' : ''}{selected.error.message}</p>}
      {selected.status === 'succeeded' && <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(selected.output ?? {}, null, 2)}</pre>}
      {['queued', 'running'].includes(selected.status) && <button type="button" className="button" onClick={() => load(selected.id)}>核对状态</button>}
    </Dialog>}
    {cancel && <Dialog title="取消任务" description={`取消“${cancel.definitionName}”的本次执行？`} onClose={() => setCancel(undefined)} closeDisabled={busy}>
      {error && <p className="notice notice-error" role="alert">{error}</p>}
      <div className="dialog-actions">
        <button type="button" className="button" disabled={busy} onClick={() => setCancel(undefined)}>返回</button>
        <button type="button" className="button danger" disabled={busy} onClick={() => cancelJob(cancel)}>确认取消任务</button>
      </div>
    </Dialog>}
  </section>
}
