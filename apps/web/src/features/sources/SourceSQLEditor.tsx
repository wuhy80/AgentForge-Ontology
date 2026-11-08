import { forwardRef, useEffect, useId, useImperativeHandle, useRef } from 'react'
import { Annotation, Compartment, EditorState, Prec, Transaction } from '@codemirror/state'
import { EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, defaultHighlightStyle, indentUnit, StreamLanguage, syntaxHighlighting } from '@codemirror/language'

export type SourceSQLEditorHandle = { focus(): void }
export type SourceSQLEditorProps = {
  value: string
  onChange(text: string): void
  onRun(): void
  disabled?: boolean
  onCompositionChange?(composing: boolean): void
}

const externalChange = Annotation.define<boolean>()
// Highlighting helps author SQL. The server parser defines the supported grammar.
const sqlLanguage = StreamLanguage.define<{ comment: boolean; quote?: string }>({
  startState: () => ({ comment: false }),
  token(stream, state) {
    if (state.comment) {
      while (!stream.eol()) if (stream.next() === '*' && stream.eat('/')) { state.comment = false; break }
      return 'comment'
    }
    if (stream.eatSpace()) return null
    if (stream.match('--') || stream.match('#')) { stream.skipToEnd(); return 'comment' }
    if (stream.match('/*')) { state.comment = true; return 'comment' }
    const quote = state.quote ?? stream.peek()
    if (quote === '"' || quote === "'" || quote === '`') {
      if (!state.quote) stream.next()
      state.quote = quote
      while (!stream.eol()) {
        const char = stream.next()
        if (char === '\\') stream.next()
        else if (char === quote && !stream.eat(quote)) { state.quote = undefined; break }
      }
      return quote === "'" ? 'string' : 'variableName'
    }
    if (stream.match(/^\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/)) return 'number'
    if (stream.match(/^(?:SELECT|FROM|AS|WHERE|AND|OR|NOT|IS|NULL|IN|TRUE|FALSE|ORDER|BY|ASC|DESC|LIMIT|LIKE|OFFSET|GROUP|HAVING|DISTINCT|CASE|WHEN|THEN|ELSE|END)\b/i)) return 'keyword'
    if (stream.match(/^(?:COUNT|SUM|AVG|MIN|MAX|COALESCE|ROUND|DATABASE|CURRENT_DATABASE|PG_TOTAL_RELATION_SIZE|PG_RELATION_SIZE)\b/i)) return 'builtin'
    if (stream.match(/^[=<>!+*/%-]+/)) return 'operator'
    if (stream.match(/^[\p{L}\p{N}_.$]+/u)) return 'variableName'
    stream.next(); return null
  },
})

export const SourceSQLEditor = forwardRef<SourceSQLEditorHandle, SourceSQLEditorProps>(function SourceSQLEditor(props, ref) {
  const host = useRef<HTMLDivElement>(null), editor = useRef<EditorView | undefined>(undefined)
  const latest = useRef(props); latest.current = props
  const access = useRef(new Compartment())
  const composing = useRef(false), compositionCanceled = useRef(false)
  const pendingExternal = useRef<string | undefined>(undefined), synchronize = useRef<(() => void) | undefined>(undefined)
  const hintId = useId()
  useImperativeHandle(ref, () => ({ focus() { editor.current?.focus() } }), [])

  useEffect(() => {
    if (!host.current) return
    let alive = true
    function disabled() {
      if (latest.current.disabled) return true
      for (let parent = host.current?.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLFieldSetElement && parent.disabled) return true
      return false
    }
    function accessExtensions() {
      const blocked = disabled()
      return [EditorState.readOnly.of(blocked), EditorView.editable.of(!blocked), EditorView.contentAttributes.of({ role: 'textbox', 'aria-label': 'SQL 语句', 'aria-multiline': 'true', 'aria-describedby': hintId, 'aria-disabled': String(blocked), 'aria-readonly': String(blocked), tabindex: blocked ? '-1' : '0', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' })]
    }
    function syncExternal() {
      const value = pendingExternal.current
      if (!alive || value === undefined || !compositionCanceled.current && (composing.current || view.compositionStarted)) return
      pendingExternal.current = undefined
      if (value !== latest.current.value || view.state.doc.toString() === value) return
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value }, annotations: [externalChange.of(true), Transaction.addToHistory.of(false)] })
    }
    const view = new EditorView({ parent: host.current, state: EditorState.create({ doc: latest.current.value, extensions: [
      sqlLanguage, syntaxHighlighting(defaultHighlightStyle), lineNumbers(), drawSelection(), highlightActiveLine(), highlightActiveLineGutter(), bracketMatching(), indentUnit.of('  '), history(),
      placeholder('编写只读 SELECT 查询，或从“填入查询”选择模板…'),
      access.current.of(accessExtensions()),
      Prec.highest(keymap.of([{ key: 'Mod-Enter', run(current) { if (!current.state.readOnly && !composing.current && !current.composing) latest.current.onRun(); return true } }])),
      keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
      EditorView.domEventHandlers({
        compositionstart() { composing.current = true; compositionCanceled.current = false; latest.current.onCompositionChange?.(true); return false },
        compositionend() { queueMicrotask(() => { if (alive) { composing.current = false; syncExternal(); latest.current.onCompositionChange?.(false) } }); return false },
      }),
      EditorView.updateListener.of((update) => { if (update.docChanged && !update.state.readOnly && !update.transactions.some((transaction) => transaction.annotation(externalChange))) latest.current.onChange(update.state.doc.toString()) }),
    ] }) })
    editor.current = view; synchronize.current = syncExternal
    const observer = new MutationObserver(() => {
      if (!alive) return
      if (disabled()) { composing.current = false; compositionCanceled.current = true; latest.current.onCompositionChange?.(false) }
      view.dispatch({ effects: access.current.reconfigure(accessExtensions()) }); syncExternal()
    })
    for (let parent = host.current.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLFieldSetElement) observer.observe(parent, { attributes: true, attributeFilter: ['disabled'] })
    return () => { alive = false; observer.disconnect(); editor.current = undefined; synchronize.current = undefined; pendingExternal.current = undefined; composing.current = false; view.destroy() }
  }, [hintId])

  useEffect(() => {
    const view = editor.current
    if (!view) return
    pendingExternal.current = view.state.doc.toString() === props.value ? undefined : props.value
    synchronize.current?.()
  }, [props.value])
  useEffect(() => {
    const view = editor.current
    if (!view) return
    let blocked = !!props.disabled
    for (let parent = host.current?.parentElement; parent; parent = parent.parentElement) if (parent instanceof HTMLFieldSetElement && parent.disabled) blocked = true
    if (blocked) { composing.current = false; compositionCanceled.current = true; latest.current.onCompositionChange?.(false) }
    view.dispatch({ effects: access.current.reconfigure([EditorState.readOnly.of(blocked), EditorView.editable.of(!blocked), EditorView.contentAttributes.of({ role: 'textbox', 'aria-label': 'SQL 语句', 'aria-multiline': 'true', 'aria-describedby': hintId, 'aria-disabled': String(blocked), 'aria-readonly': String(blocked), tabindex: blocked ? '-1' : '0', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' })]) })
    if (blocked) synchronize.current?.()
  }, [props.disabled, hintId])
  return <div className="source-sql-editor"><div className="source-sql-editor-host" ref={host} /><span className="source-sql-editor-help" id={hintId}>Ctrl / ⌘ + Enter 执行查询 · Tab 缩进 · Esc 后按 Tab 移出编辑区</span></div>
})
