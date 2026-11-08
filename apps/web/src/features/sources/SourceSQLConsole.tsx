import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { AlertTriangle, CheckCheck, ChevronDown, Code2, Database, Eraser, LoaderCircle, Play, Sparkles } from 'lucide-react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceTableInfo } from '../../lib/types'
import { buildSourceSQL, sourceSQLPresets, type SourceSQLPreset } from '../../lib/source-sql'
import { SourceSQLEditor, type SourceSQLEditorHandle } from './SourceSQLEditor'
import './source-sql-console.css'

export type SourceSQLConsoleProps = {
  api: ApiClient
  source: DataSource
  table?: SourceTableInfo
  serverVersion?: string
  sql: string
  onSQLChange(text: string): void
  onRun(limit: number): void
  onLimitChange?(): void
  children?: ReactNode
  busy: boolean
  disabled: boolean
  onError(cause: unknown): void
}
type ToolStatus = { sql: string; limit: string; identity: string; message: string; invalid: boolean }

export function SourceSQLConsole(props: SourceSQLConsoleProps) {
  const { api, source, table, sql, disabled, busy } = props
  const [limitText, setLimitText] = useState('100')
  const [toolBusy, setToolBusy] = useState<'analyze' | 'format'>()
  const [toolStatus, setToolStatus] = useState<ToolStatus>()
  const [menuOpen, setMenuOpen] = useState(false), [menuIndex, setMenuIndex] = useState(0)
  const [isComposing, setComposing] = useState(false)
  const composing = useRef(false), mounted = useRef(false), generation = useRef(0)
  const request = useRef<AbortController | undefined>(undefined)
  const editor = useRef<SourceSQLEditorHandle>(null), menu = useRef<HTMLDivElement>(null), trigger = useRef<HTMLButtonElement>(null)
  const menuItems = useRef<(HTMLButtonElement | null)[]>([])
  const menuId = useId()
  const identity = JSON.stringify([source.tenantId, source.id, source.etag, source.driver, source.database, table?.namespace, table?.name])
  const latest = useRef({ ...props, identity, limitText }); latest.current = { ...props, identity, limitText }
  const limit = Number(limitText), validLimit = Number.isInteger(limit) && limit >= 1 && limit <= 100
  const actionBlocked = disabled || busy || isComposing
  const status = toolStatus?.sql === sql && toolStatus.limit === limitText && toolStatus.identity === identity ? toolStatus : undefined
  const dialect = source.driver === 'mysql' ? 'MySQL' : 'PostgreSQL'

  function cancelTool() {
    request.current?.abort(); request.current = undefined; generation.current++
    if (mounted.current) setToolBusy(undefined)
  }
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; cancelTool() } }, [])
  useEffect(() => {
    cancelTool()
    setToolStatus((value) => value && (value.sql !== sql || value.limit !== limitText || value.identity !== identity || disabled) ? undefined : value)
  }, [api, sql, limitText, identity, disabled, busy])
  useEffect(() => { setMenuOpen(false) }, [identity, disabled, busy])
  useEffect(() => { composing.current = false; setComposing(false) }, [identity])
  useEffect(() => {
    if (!menuOpen) return
    menuItems.current[menuIndex]?.focus()
    const outside = (event: PointerEvent) => { if (!menu.current?.contains(event.target as Node)) setMenuOpen(false) }
    document.addEventListener('pointerdown', outside)
    return () => document.removeEventListener('pointerdown', outside)
  }, [menuOpen, menuIndex])

  function changeSQL(text: string) { cancelTool(); setToolStatus(undefined); props.onSQLChange(text) }
  function run() { if (!actionBlocked && !composing.current && !toolBusy && validLimit && sql.trim()) props.onRun(limit) }
  function fill(preset: SourceSQLPreset) {
    if (actionBlocked || composing.current) return
    try { changeSQL(buildSourceSQL(source.driver, preset, table)); setMenuOpen(false); editor.current?.focus() }
    catch (cause) { setToolStatus({ sql, limit: limitText, identity, message: cause instanceof Error ? cause.message : '查询模板暂不可用。', invalid: true }) }
  }
  async function sourceTool(operation: 'analyze' | 'format') {
    if (actionBlocked || composing.current || toolBusy || !validLimit || !sql.trim()) return
    cancelTool()
    const controller = new AbortController(), sequence = generation.current
    request.current = controller; setToolBusy(operation); setToolStatus(undefined)
    const valid = () => mounted.current && !controller.signal.aborted && sequence === generation.current && latest.current.api === api && latest.current.identity === identity && latest.current.sql === sql && latest.current.limitText === limitText && !latest.current.disabled && !latest.current.busy && !composing.current
    try {
      const result = operation === 'format' ? await api.formatSourceSQL(source.id, sql, source.etag, controller.signal, limit) : await api.analyzeSourceSQL(source.id, sql, source.etag, controller.signal, limit)
      if (!valid()) return
      const next = operation === 'format' && result.valid ? result.sql : sql
      if (next !== sql) props.onSQLChange(next)
      setToolStatus({ sql: next, limit: limitText, identity, message: result.message || (result.valid ? operation === 'format' ? 'SQL 已格式化。' : '语法校验通过。' : 'SQL 未通过校验，请检查语句。'), invalid: !result.valid })
    } catch (cause) {
      if (!valid()) return
      if (cause instanceof ApiError && [401, 403, 412].includes(cause.status)) props.onError(cause)
      else setToolStatus({ sql, limit: limitText, identity, message: cause instanceof Error ? cause.message : 'SQL 工具暂不可用，请重试。', invalid: true })
    } finally { if (mounted.current && sequence === generation.current) { request.current = undefined; setToolBusy(undefined) } }
  }
  function openMenu(last = false) {
    if (actionBlocked) return
    setMenuIndex(last ? sourceSQLPresets.length - 1 : table ? 0 : 1); setMenuOpen(true)
  }

  return <section className="source-sql-console" aria-label="SQL 控制台">
    <div className="source-sql-warning"><AlertTriangle size={17} /><p>此控制台仅支持只读查询：仅允许单表 SELECT 和内置统计模板，禁止写入、DDL 和多语句。最多返回 100 行。</p></div>
    <div className="source-sql-dialect"><span><Code2 size={15} />当前 SQL 方言：<strong>{dialect}</strong>{props.serverVersion && <span>{props.serverVersion}</span>}</span><span>只读查询</span></div>
    <div className="source-sql-toolbar">
      <div className="source-sql-toolbar-actions">
        <button type="button" className="button primary" disabled={actionBlocked || !!toolBusy || !validLimit || !sql.trim()} onClick={run}>{busy ? <LoaderCircle size={15} className="spin" /> : <Play size={15} />}执行 SQL</button>
        <button type="button" className="button subtle" disabled={actionBlocked || !!toolBusy || !validLimit || !sql.trim()} onClick={() => void sourceTool('format')}>{toolBusy === 'format' ? <LoaderCircle size={15} className="spin" /> : <Sparkles size={15} />}一键格式化</button>
        <button type="button" className="button subtle" disabled={actionBlocked || !!toolBusy || !validLimit || !sql.trim()} onClick={() => void sourceTool('analyze')}>{toolBusy === 'analyze' ? <LoaderCircle size={15} className="spin" /> : <CheckCheck size={15} />}校验语法</button>
        <div className="source-sql-template" ref={menu} onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setMenuOpen(false) }}>
          <button type="button" className="button subtle" ref={trigger} disabled={actionBlocked} aria-haspopup="menu" aria-expanded={menuOpen} aria-controls={menuOpen ? menuId : undefined} onClick={() => menuOpen ? setMenuOpen(false) : openMenu()} onKeyDown={(event) => { if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); openMenu(event.key === 'ArrowUp') } }}>填入查询<ChevronDown size={14} /></button>
          {menuOpen && <div className="source-sql-template-menu" role="menu" aria-label="填入查询" id={menuId} onKeyDown={(event) => {
            const available = sourceSQLPresets.map((_, index) => index).filter((index) => index !== 0 || !!table)
            const current = available.indexOf(menuIndex)
            const next = event.key === 'ArrowDown' ? available[(current + 1) % available.length] : event.key === 'ArrowUp' ? available[(current - 1 + available.length) % available.length] : event.key === 'Home' ? available[0] : event.key === 'End' ? available.at(-1) : undefined
            if (next !== undefined) { event.preventDefault(); setMenuIndex(next) }
            else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setMenuOpen(false); trigger.current?.focus() }
            else if (event.key === 'Tab') setMenuOpen(false)
          }}>{sourceSQLPresets.map((preset, index) => <button key={preset.id} ref={(element) => { menuItems.current[index] = element }} type="button" role="menuitem" tabIndex={menuIndex === index ? 0 : -1} disabled={index === 0 && !table} onFocus={() => setMenuIndex(index)} onClick={() => fill(preset.id)}>{preset.label}</button>)}</div>}
        </div>
        <button type="button" className="button subtle" disabled={actionBlocked || !sql} onClick={() => { changeSQL(''); editor.current?.focus() }}><Eraser size={15} />清空</button>
      </div>
      <label className="source-sql-limit">查询行数上限<input className="field" aria-label="查询行数上限" type="number" min={1} max={100} step={1} disabled={disabled} value={limitText} onChange={(event) => { cancelTool(); setToolStatus(undefined); setLimitText(event.target.value); props.onLimitChange?.() }} /></label>
    </div>
    {!validLimit && <p className="source-sql-tool-status is-error" role="alert">查询行数上限须为 1–100 的整数。</p>}
    <SourceSQLEditor key={identity} ref={editor} value={sql} onChange={changeSQL} onRun={run} disabled={disabled} onCompositionChange={(value) => { composing.current = value; setComposing(value); if (value) cancelTool() }} />
    {status && <p className={`source-sql-tool-status${status.invalid ? ' is-error' : ''}`} role={status.invalid ? 'alert' : 'status'}>{status.message}</p>}
    <div className="source-sql-footer"><span><Database size={13} />当前数据源：{source.name}<span className="source-sql-footer-database">{source.database}</span></span><p>按 SQL LIMIT 与行数上限的较小值截取；表行数排行为估算值。</p></div>
    {props.children ?? <div className="source-sql-empty" role={busy ? 'status' : undefined}>{busy ? <LoaderCircle size={28} className="spin" /> : <Code2 size={30} />}<p>{busy ? '正在执行只读查询…' : '编写 SQL 并执行查询，结果将在这里展示'}</p></div>}
  </section>
}
