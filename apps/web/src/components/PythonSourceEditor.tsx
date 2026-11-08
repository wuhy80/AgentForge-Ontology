import { useEffect, useId, useRef, useState } from 'react'
import { Annotation, Compartment, EditorState, Transaction } from '@codemirror/state'
import { EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { autocompletion, closeBrackets, closeBracketsKeymap, closeCompletion, completionKeymap, startCompletion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import { bracketMatching, defaultHighlightStyle, indentOnInput, indentUnit, syntaxHighlighting, syntaxTree } from '@codemirror/language'
import { globalCompletion, localCompletionSource, python } from '@codemirror/lang-python'
import { lintGutter, setDiagnostics } from '@codemirror/lint'
import type { SourceDiagnostic } from '../lib/types'
import { checkPythonSource, formatPythonSource } from '../lib/python-source-tools'
import './PythonSourceEditor.css'

export type PythonSourceEditorProps = {
  value: string
  onChange: (value: string) => void
  inputNames?: string[]
  outputNames?: string[]
  disabled?: boolean
  label?: string
  diagnostics?: SourceDiagnostic[]
  diagnosticFocus?: SourceDiagnostic
}

const externalChange = Annotation.define<boolean>()

function diagnosticRange(state: EditorState, diagnostic: SourceDiagnostic) {
  const line = state.doc.line(Math.max(1, Math.min(state.doc.lines, diagnostic.line)))
  const end = state.doc.line(Math.max(1, Math.min(state.doc.lines, diagnostic.endLine ?? diagnostic.line)))
  // Both CPython and Ruff use one-based Unicode code points. CodeMirror uses
  // UTF-16 offsets, so an emoji before the error must count as two code units.
  const from = line.from + Array.from(line.text).slice(0, Math.max(0, (diagnostic.column ?? 1) - 1)).join('').length
  const to = Math.max(from, end.from + Array.from(end.text).slice(0, Math.max(0, (diagnostic.endColumn ?? (diagnostic.column ?? 1) + 1) - 1)).join('').length)
  return { from, to }
}

function escapeParameter(name: string, quote: string) {
  const escaped = JSON.stringify(name).slice(1, -1)
  return quote === '"' ? escaped : escaped.replace(/\\"/g, '"').replace(/'/g, "\\'")
}

// Parameter completions augment the Python parser's local names and built-ins.
// Only declared input keys and keys in a directly returned dictionary are offered.
export function pythonParameterCompletions(context: CompletionContext, inputNames: readonly string[], outputNames: readonly string[]): CompletionResult | null {
  const line = context.state.doc.lineAt(context.pos)
  const before = context.state.sliceDoc(line.from, context.pos)
  let node = syntaxTree(context.state).resolveInner(context.pos, -1)
  if (node.name === 'Comment') return null

  const inputKey = /\binputs\s*\[\s*(["'])([^"'\\]*)$/.exec(before)
  if (inputKey && node.name === 'String' && node.parent?.name === 'MemberExpression') {
    return {
      from: context.pos - inputKey[2].length,
      options: inputNames.map((name) => ({ label: name, apply: escapeParameter(name, inputKey[1]), type: 'property', detail: '输入参数' })),
      validFor: /^[^"'\\\n]*$/,
    }
  }

  const stringKey = /(["'])([^"'\\]*)$/.exec(before)
  const prefixEnd = context.pos - (stringKey?.[0].length ?? 0)
  const prefix = context.state.sliceDoc(Math.max(0, prefixEnd - 8192), prefixEnd)
  if (/[{,]\s*$/.test(prefix)) {
    // An unfinished first key is parsed as a set until its colon is typed.
    const dictionaries = ['DictionaryExpression', 'SetExpression', 'SetComprehensionExpression', 'DictionaryComprehensionExpression']
    while (node.parent && !dictionaries.includes(node.name)) node = node.parent
    if (dictionaries.includes(node.name) && /\breturn\s*$/.test(context.state.sliceDoc(Math.max(0, node.from - 40), node.from))) {
      return {
        from: context.pos - (stringKey?.[2].length ?? 0),
        options: outputNames.map((name) => ({ label: name, apply: stringKey ? escapeParameter(name, stringKey[1]) : `${JSON.stringify(name)}: `, type: 'property', detail: '输出参数' })),
        validFor: /^[^"'\\\n]*$/,
      }
    }
  }

  node = syntaxTree(context.state).resolveInner(context.pos, -1)
  let cursor: typeof node | null = node
  while (cursor) {
    if (['String', 'FormatString', 'PropertyName', 'Comment'].includes(cursor.name)) return null
    cursor = cursor.parent
  }
  const word = context.matchBefore(/[\p{L}\p{N}_]+/u)
  if (!word && !context.explicit) return null
  return {
    from: word?.from ?? context.pos,
    options: [
      { label: 'inputs', type: 'variable', detail: '输入参数字典' },
      ...inputNames.map((name) => ({ label: `inputs[${JSON.stringify(name)}]`, type: 'property', detail: `读取输入：${name}` })),
    ],
    validFor: /^[\p{L}\p{N}_]*$/u,
  }
}

export function PythonSourceEditor(props: PythonSourceEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const editor = useRef<EditorView | undefined>(undefined)
  const latest = useRef(props)
  latest.current = props
  const access = useRef(new Compartment())
  const composing = useRef(false)
  const compositionCanceled = useRef(false)
  const pendingExternal = useRef<string | undefined>(undefined)
  const synchronize = useRef<(() => void) | undefined>(undefined)
  const mounted = useRef(false)
  const revision = useRef(0)
  const [source, setSource] = useState(props.value)
  const [analysis, setAnalysis] = useState<{ source: string; diagnostics: SourceDiagnostic[] }>()
  const [toolError, setToolError] = useState<string>()
  const [retry, setRetry] = useState(0)
  const [formatting, setFormatting] = useState(false)
  const [formatMessage, setFormatMessage] = useState<string>()
  const hintId = useId()
  const label = props.label ?? 'Python 源码'
  const parameterSignature = JSON.stringify([props.inputNames ?? [], props.outputNames ?? []])

  useEffect(() => {
    if (!host.current) return
    mounted.current = true
    let alive = true
    function isDisabled() {
      if (latest.current.disabled) return true
      for (let parent = host.current?.parentElement; parent; parent = parent.parentElement) {
        if (parent instanceof HTMLFieldSetElement && parent.disabled) return true
      }
      return false
    }
    function accessExtensions() {
      const disabled = isDisabled()
      return [
        EditorState.readOnly.of(disabled), EditorView.editable.of(!disabled),
        EditorView.contentAttributes.of({ role: 'textbox', 'aria-label': latest.current.label ?? 'Python 源码', 'aria-multiline': 'true', 'aria-describedby': hintId, 'aria-disabled': String(disabled), 'aria-readonly': String(disabled), spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off', tabindex: disabled ? '-1' : '0' }),
      ]
    }
    function synchronizeExternal() {
      const value = pendingExternal.current
      if (!alive || value === undefined || !compositionCanceled.current && (composing.current || view.compositionStarted)) return
      pendingExternal.current = undefined
      if (value !== latest.current.value || view.state.doc.toString() === value) return
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: value },
        annotations: [externalChange.of(true), Transaction.addToHistory.of(false)],
      })
    }
    const view: EditorView = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: latest.current.value,
        extensions: [
          python(), syntaxHighlighting(defaultHighlightStyle), indentUnit.of('    '),
          lineNumbers(), highlightActiveLine(), highlightActiveLineGutter(), drawSelection(), lintGutter(),
          history(), bracketMatching(), closeBrackets(), indentOnInput(),
          access.current.of(accessExtensions()),
          keymap.of([...closeBracketsKeymap, ...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
          autocompletion({
            activateOnTyping: true, activateOnTypingDelay: 120,
            override: [
              (context) => composing.current || view.composing || context.state.readOnly ? null : pythonParameterCompletions(context, latest.current.inputNames ?? [], latest.current.outputNames ?? []),
              localCompletionSource, globalCompletion,
            ],
          }),
          EditorView.domEventHandlers({
            compositionstart() { revision.current++; composing.current = true; compositionCanceled.current = false; closeCompletion(view); return false },
            compositionend() {
              // CodeMirror flushes its final DOM mutation first. Then apply any
              // external reload deferred during IME, using only the latest value.
              queueMicrotask(() => { if (alive) { composing.current = false; synchronizeExternal(); setRetry((value) => value + 1) } })
              return false
            },
          }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) { revision.current++; setSource(update.state.doc.toString()); setFormatMessage(undefined) }
            if (update.docChanged && !update.state.readOnly && !update.transactions.some((transaction) => transaction.annotation(externalChange))) latest.current.onChange(update.state.doc.toString())
          }),
        ],
      }),
    })
    editor.current = view
    synchronize.current = synchronizeExternal
    const observer = new MutationObserver(() => {
      if (alive) {
        const disabled = isDisabled()
        if (disabled) { revision.current++; composing.current = false; compositionCanceled.current = true }
        closeCompletion(view)
        view.dispatch({ effects: access.current.reconfigure(accessExtensions()) })
        if (disabled) synchronizeExternal()
      }
    })
    for (let parent = host.current.parentElement; parent; parent = parent.parentElement) {
      if (parent instanceof HTMLFieldSetElement) observer.observe(parent, { attributes: true, attributeFilter: ['disabled'] })
    }
    return () => { alive = false; mounted.current = false; revision.current++; composing.current = false; compositionCanceled.current = false; pendingExternal.current = undefined; synchronize.current = undefined; observer.disconnect(); editor.current = undefined; view.destroy() }
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
    let disabled = !!props.disabled
    for (let parent = host.current?.parentElement; parent; parent = parent.parentElement) {
      if (parent instanceof HTMLFieldSetElement && parent.disabled) disabled = true
    }
    revision.current++
    if (disabled) { composing.current = false; compositionCanceled.current = true }
    closeCompletion(view)
    view.dispatch({ effects: access.current.reconfigure([
      EditorState.readOnly.of(disabled), EditorView.editable.of(!disabled),
      EditorView.contentAttributes.of({ role: 'textbox', 'aria-label': label, 'aria-multiline': 'true', 'aria-describedby': hintId, 'aria-disabled': String(disabled), 'aria-readonly': String(disabled), spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off', tabindex: disabled ? '-1' : '0' }),
    ]) })
    if (disabled) synchronize.current?.()
  }, [props.disabled, label, hintId])

  useEffect(() => { if (editor.current) closeCompletion(editor.current) }, [parameterSignature])

  useEffect(() => {
    let active = true
    setToolError(undefined)
    const timer = setTimeout(() => {
      if (composing.current || editor.current?.compositionStarted) return
      checkPythonSource(source).then((diagnostics) => { if (active) setAnalysis({ source, diagnostics }) })
        .catch((error: unknown) => { if (active) setToolError(error instanceof Error ? error.message : 'Python 语法检查失败，请重试。') })
    }, 450)
    return () => { active = false; clearTimeout(timer) }
  }, [source, retry])

  const diagnostics = [...(analysis?.source === source ? analysis.diagnostics : []), ...(props.diagnostics ?? [])]
  const diagnosticSignature = JSON.stringify(diagnostics)
  useEffect(() => {
    const view = editor.current
    if (!view || composing.current || view.composing) return
    const entries = JSON.parse(diagnosticSignature) as SourceDiagnostic[]
    view.dispatch(setDiagnostics(view.state, entries.map((diagnostic) => ({ ...diagnosticRange(view.state, diagnostic), severity: 'error', message: diagnostic.message }))))
  }, [diagnosticSignature, source, retry])

  function jump(diagnostic: SourceDiagnostic) {
    const view = editor.current
    if (!view || composing.current || view.composing) return
    const { from } = diagnosticRange(view.state, diagnostic)
    view.dispatch({ selection: { anchor: from }, effects: EditorView.scrollIntoView(from, { y: 'center' }) })
    view.focus()
  }

  useEffect(() => { if (props.diagnosticFocus) jump(props.diagnosticFocus) }, [props.diagnosticFocus])

  async function format() {
    const view = editor.current
    if (!view || view.state.readOnly || composing.current || view.composing || formatting) return
    const snapshot = view.state.doc.toString()
    const generation = revision.current
    setFormatting(true)
    setFormatMessage(undefined)
    try {
      const formatted = await formatPythonSource(snapshot)
      if (!mounted.current || editor.current !== view) return
      if (generation !== revision.current || view.state.readOnly || composing.current || view.composing) { setFormatMessage('源码或编辑状态已变化，已忽略本次格式化结果。'); return }
      if (formatted !== snapshot) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: formatted }, userEvent: 'input.format', annotations: Transaction.addToHistory.of(true) })
      setFormatMessage(formatted === snapshot ? '代码格式已符合规范。' : '已格式化，可撤销。')
    } catch (error: unknown) {
      if (mounted.current && editor.current === view && generation === revision.current) setFormatMessage(error instanceof Error ? error.message : '格式化失败，请重试。')
    } finally { if (mounted.current) setFormatting(false) }
  }

  return <div className="python-source-editor">
    <div className="python-source-toolbar"><span>{label}</span><div className="python-source-actions"><button type="button" className="button subtle" disabled={props.disabled || formatting} onClick={() => void format()}>{formatting ? '格式化中…' : '格式化代码'}</button><button type="button" className="button subtle" disabled={props.disabled} onClick={() => {
      const view = editor.current
      if (!view || view.state.readOnly || composing.current || view.composing) return
      view.focus(); startCompletion(view)
    }}>自动补全</button></div></div>
    <div className="python-source-host" ref={host} />
    <p id={hintId} className="python-source-hint muted">Ctrl+Space 补全 · Tab 缩进 · Esc 后按 Tab 移出编辑区。支持 Python 语法、局部变量及已声明的中文参数补全。</p>
    {diagnostics.length > 0 && <ul className="python-source-diagnostics" aria-label="源码诊断">{diagnostics.map((diagnostic, index) => <li key={`${diagnostic.code}-${diagnostic.line}-${index}`}><button type="button" className="button subtle" onClick={() => jump(diagnostic)}>第 {diagnostic.line} 行{diagnostic.column ? `，第 ${diagnostic.column} 列` : ''}：{diagnostic.message}</button></li>)}</ul>}
    {toolError ? <p className="python-source-hint" role="status">{toolError} <button type="button" className="button subtle" disabled={props.disabled} onClick={() => setRetry((value) => value + 1)}>重试语法检查</button></p> : analysis?.source === source && analysis.diagnostics.length === 0 ? <p className="python-source-hint muted" role="status">本地语法检查通过；实际执行以 Python 3.12 试运行结果为准。</p> : !diagnostics.length && <p className="python-source-hint muted" role="status">正在检查 Python 语法…</p>}
    {formatMessage && <p className="python-source-hint" role="status">{formatMessage}</p>}
  </div>
}
