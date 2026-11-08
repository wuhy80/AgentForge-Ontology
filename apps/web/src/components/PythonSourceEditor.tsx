import { editorTheme } from './editor-theme'
import { useEffect, useId, useRef, useState } from 'react'
import { Annotation, Compartment, EditorState, Transaction } from '@codemirror/state'
import { EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { autocompletion, closeBrackets, closeBracketsKeymap, closeCompletion, completionKeymap, startCompletion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import { bracketMatching, indentOnInput, indentUnit, syntaxTree } from '@codemirror/language'
import { globalCompletion, localCompletionSource, python } from '@codemirror/lang-python'
import { javascript, localCompletionSource as javascriptLocalCompletion } from '@codemirror/lang-javascript'
import { go, localCompletionSource as goLocalCompletion } from '@codemirror/lang-go'
import { lintGutter, setDiagnostics } from '@codemirror/lint'
import type { ScriptLanguage, SourceDiagnostic, SourceToolResult } from '../lib/types'
import { checkPythonSource, formatPythonSource } from '../lib/python-source-tools'
import { checkJavascriptSource, formatJavascriptSource } from '../lib/javascript-source-tools'
import { analyzeGoSource } from '../lib/go-source-analysis'
import { languageLabels } from '../lib/runtime-language'
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
  language?: ScriptLanguage
  sourceTools?: { check: (source: string) => Promise<SourceToolResult>; format: (source: string) => Promise<SourceToolResult> }
}

const externalChange = Annotation.define<boolean>()

function languageExtensions(language: ScriptLanguage) {
  return [language === 'javascript' ? javascript() : language === 'go' ? go() : python(), indentUnit.of(language === 'javascript' ? '  ' : language === 'go' ? '\t' : '    ')]
}

export function scriptParameterCompletions(context: CompletionContext, language: ScriptLanguage, inputNames: readonly string[], outputNames: readonly string[]): CompletionResult | null {
  if (language === 'python') return pythonParameterCompletions(context, inputNames, outputNames)
  const line = context.state.doc.lineAt(context.pos)
  const before = context.state.sliceDoc(line.from, context.pos)
  let node = syntaxTree(context.state).resolveInner(context.pos, -1)
  for (let cursor: typeof node | null = node; cursor; cursor = cursor.parent) if (/Comment/.test(cursor.name)) return null
  const inputKey = /\binputs\s*\[\s*(["'])([^"'\\]*)$/.exec(before)
  if (inputKey && node.name === 'String' && node.parent?.name === (language === 'go' ? 'IndexExpr' : 'MemberExpression') && /^inputs\s*\[/.test(context.state.sliceDoc(node.parent.from, node.from)) && (language !== 'go' || inputKey[1] === '"')) return {
    from: context.pos - inputKey[2].length,
    options: inputNames.map((name) => ({ label: name, apply: escapeParameter(name, inputKey[1]), type: 'property', detail: '输入参数' })), validFor: /^[^"'\\\n]*$/,
  }
  const key = /(["'])([^"'\\]*)$/.exec(before)
  const end = context.pos - (key?.[0].length ?? 0)
  const prefix = context.state.sliceDoc(Math.max(0, end - 8192), end)
  let literal: typeof node | null = node
  while (literal && !['ObjectExpression', 'LiteralValue'].includes(literal.name)) literal = literal.parent
  const returnedObject = language === 'javascript'
    ? literal?.name === 'ObjectExpression' && literal.parent?.name === 'ReturnStatement'
    : literal?.name === 'LiteralValue' && literal.parent?.name === 'TypedLiteral' && literal.parent.parent?.name === 'ReturnStatement' && /^map\[string\](?:any|interface\{\})\s*$/.test(context.state.sliceDoc(literal.parent.from, literal.from))
  if (returnedObject && /[{,]\s*$/.test(prefix) && (language !== 'go' || !key || key[1] === '"')) return {
    from: context.pos - (key?.[2].length ?? 0),
    options: outputNames.map((name) => ({ label: name, apply: key ? escapeParameter(name, key[1]) : `${JSON.stringify(name)}: `, type: 'property', detail: '输出参数' })), validFor: /^[^"'\\\n]*$/,
  }
  node = syntaxTree(context.state).resolveInner(context.pos, -1)
  for (let cursor: typeof node | null = node; cursor; cursor = cursor.parent) if (/String|Comment/.test(cursor.name)) return null
  const word = context.matchBefore(/[\p{L}\p{N}_]+/u)
  if (!word && !context.explicit) return null
  const builtins = language === 'javascript' ? ['function', 'return', 'const', 'let', 'if', 'else', 'for', 'Object', 'Array', 'String', 'Number', 'Boolean', 'JSON', 'Math', 'BigInt'] : ['func', 'return', 'if', 'else', 'for', 'range', 'map', 'string', 'any', 'nil', 'true', 'false', 'len', 'append', 'make', 'new', 'delete', 'copy']
  return { from: word?.from ?? context.pos, options: [{ label: 'inputs', type: 'variable', detail: '输入参数对象' }, ...builtins.map((label) => ({ label, type: 'keyword' })), ...inputNames.map((name) => ({ label: `inputs[${JSON.stringify(name)}]`, type: 'property', detail: `读取输入：${name}` }))], validFor: /^[\p{L}\p{N}_]*$/u }
}

function diagnosticRange(state: EditorState, diagnostic: SourceDiagnostic) {
  const line = state.doc.line(Math.max(1, Math.min(state.doc.lines, diagnostic.line)))
  const end = state.doc.line(Math.max(1, Math.min(state.doc.lines, diagnostic.endLine ?? diagnostic.line)))
  // Source tools use one-based Unicode code points. CodeMirror uses
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
  const grammar = useRef(new Compartment())
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
  const [checking, setChecking] = useState(false)
  const [containerAnalysis, setContainerAnalysis] = useState<SourceToolResult>()
  const toolSequence = useRef(0)
  const hintId = useId()
  const language = props.language ?? 'python'
  const languageLabel = languageLabels[language]
  const label = props.label ?? `${languageLabel} 源码`
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
        EditorView.contentAttributes.of({ role: 'textbox', 'aria-label': latest.current.label ?? `${languageLabels[latest.current.language ?? 'python']} 源码`, 'aria-multiline': 'true', 'aria-describedby': hintId, 'aria-disabled': String(disabled), 'aria-readonly': String(disabled), spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off', tabindex: disabled ? '-1' : '0' }),
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
          grammar.current.of(languageExtensions(latest.current.language ?? 'python')), editorTheme,
          lineNumbers(), highlightActiveLine(), highlightActiveLineGutter(), drawSelection(), lintGutter(),
          history(), bracketMatching(), closeBrackets(), indentOnInput(),
          access.current.of(accessExtensions()),
          keymap.of([...closeBracketsKeymap, ...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
          autocompletion({
            activateOnTyping: true, activateOnTypingDelay: 120,
            override: [
              (context) => composing.current || view.composing || context.state.readOnly ? null : scriptParameterCompletions(context, latest.current.language ?? 'python', latest.current.inputNames ?? [], latest.current.outputNames ?? []),
              (context) => (latest.current.language ?? 'python') === 'python' ? localCompletionSource(context) : latest.current.language === 'javascript' ? javascriptLocalCompletion(context) : goLocalCompletion(context),
              (context) => (latest.current.language ?? 'python') === 'python' ? globalCompletion(context) : null,
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
            if (update.docChanged) { revision.current++; setSource(update.state.doc.toString()); setFormatMessage(undefined); setContainerAnalysis(undefined) }
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
    const view = editor.current
    if (!view) return
    revision.current++; closeCompletion(view)
    view.dispatch({ effects: grammar.current.reconfigure(languageExtensions(language)) })
    setAnalysis(undefined); setContainerAnalysis(undefined)
  }, [language])

  useEffect(() => {
    let active = true
    setToolError(undefined)
    const timer = setTimeout(() => {
      if (composing.current || editor.current?.compositionStarted) return
      const check = language === 'python' ? checkPythonSource(source) : language === 'javascript' ? checkJavascriptSource(source) : Promise.resolve(analyzeGoSource(source))
      check.then((diagnostics) => { if (active) setAnalysis({ source, diagnostics }) })
        .catch((error: unknown) => { if (active) setToolError(error instanceof Error ? error.message : `${languageLabel} 语法检查失败，请重试。`) })
    }, 450)
    return () => { active = false; clearTimeout(timer) }
  }, [source, retry, language, languageLabel])

  // An explicit container result takes precedence over a delayed editor hint.
  const currentAnalysis = language === 'go' && containerAnalysis?.source === source ? containerAnalysis : analysis
  const diagnostics = [...(currentAnalysis?.source === source ? currentAnalysis.diagnostics : []), ...(props.diagnostics ?? [])]
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
    if (!view || view.state.readOnly || composing.current || view.composing || formatting || checking) return
    const snapshot = view.state.doc.toString()
    const generation = revision.current
    const sequence = ++toolSequence.current
    setFormatting(true)
    setFormatMessage(undefined)
    try {
      let formatted: string
      if (language === 'go') {
        if (!props.sourceTools) throw new Error('请先保存草稿，再使用 Go 检查与格式化。')
        const result = await props.sourceTools.format(snapshot)
        if (generation === revision.current && sequence === toolSequence.current && mounted.current && editor.current === view) setContainerAnalysis({ source: snapshot, diagnostics: result.diagnostics })
        if (result.diagnostics.length) throw new Error('源码存在语法错误，请先修正标红位置再格式化。')
        formatted = result.source
      } else formatted = language === 'python' ? await formatPythonSource(snapshot) : await formatJavascriptSource(snapshot)
      if (!mounted.current || editor.current !== view) return
      if (generation !== revision.current || sequence !== toolSequence.current || view.state.readOnly || composing.current || view.composing) { setFormatMessage('源码或编辑状态已变化，已忽略本次格式化结果。'); return }
      if (formatted !== snapshot) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: formatted }, userEvent: 'input.format', annotations: Transaction.addToHistory.of(true) })
      if (language === 'go') setContainerAnalysis({ source: formatted, diagnostics: [] })
      setFormatMessage(formatted === snapshot ? '代码格式已符合规范。' : '已格式化，可撤销。')
    } catch (error: unknown) {
      if (mounted.current && editor.current === view && generation === revision.current) setFormatMessage(error instanceof Error ? error.message : '格式化失败，请重试。')
    } finally { if (mounted.current) setFormatting(false) }
  }

  async function checkContainer() {
    const view = editor.current
    if (!view || !props.sourceTools || view.state.readOnly || composing.current || view.composing || checking || formatting) return
    const snapshot = view.state.doc.toString(), generation = revision.current
    const sequence = ++toolSequence.current
    setChecking(true); setToolError(undefined)
    try {
      const result = await props.sourceTools.check(snapshot)
      if (!mounted.current || editor.current !== view || generation !== revision.current || sequence !== toolSequence.current || composing.current || view.composing || view.state.readOnly) return
      setContainerAnalysis({ source: snapshot, diagnostics: result.diagnostics })
    } catch (error) { if (mounted.current && generation === revision.current) setToolError(error instanceof Error ? error.message : 'Go 语法检查失败，请重试。') }
    finally { if (mounted.current) setChecking(false) }
  }

  return <div className="python-source-editor">
    <div className="python-source-toolbar"><span>{label}</span><div className="python-source-actions">{language === 'go' && <button type="button" className="button subtle" disabled={props.disabled || checking || formatting || !props.sourceTools} onClick={() => void checkContainer()}>{checking ? '检查中…' : '检查语法'}</button>}<button type="button" className="button subtle" disabled={props.disabled || formatting || checking || language === 'go' && !props.sourceTools} onClick={() => void format()}>{formatting ? '格式化中…' : '格式化代码'}</button><button type="button" className="button subtle" disabled={props.disabled} onClick={() => {
      const view = editor.current
      if (!view || view.state.readOnly || composing.current || view.composing) return
      view.focus(); startCompletion(view)
    }}>自动补全</button></div></div>
    <div className="python-source-host" ref={host} />
    <p id={hintId} className="python-source-hint muted">Ctrl+Space 补全 · Tab 缩进 · Esc 后按 Tab 移出编辑区。支持 {languageLabel} 语法、局部变量及已声明的中文参数补全。</p>
    {language === 'go' && !props.sourceTools && <p className="python-source-hint muted">请先保存草稿，再使用 Go 检查与格式化。</p>}
    {diagnostics.length > 0 && <ul className="python-source-diagnostics" aria-label="源码诊断">{diagnostics.map((diagnostic, index) => <li key={`${diagnostic.code}-${diagnostic.line}-${index}`}><button type="button" className="button subtle" onClick={() => jump(diagnostic)}>第 {diagnostic.line} 行{diagnostic.column ? `，第 ${diagnostic.column} 列` : ''}：{diagnostic.message}</button></li>)}</ul>}
    {toolError ? <p className="python-source-hint" role="status">{toolError} <button type="button" className="button subtle" disabled={props.disabled || checking || formatting} onClick={() => language === 'go' && props.sourceTools ? void checkContainer() : setRetry((value) => value + 1)}>重试语法检查</button></p> : currentAnalysis?.source === source && currentAnalysis.diagnostics.length === 0 ? <p className="python-source-hint muted" role="status">{language === 'go' ? containerAnalysis?.source === source ? 'Go 容器语法检查通过；编译、类型和入口签名以试运行结果为准。' : 'Go 编辑器语法提示通过；完整语法请点击「检查语法」，编译、类型和入口签名以试运行结果为准。' : `本地语法检查通过；实际执行以 ${language === 'python' ? 'Python 3.12' : 'JavaScript'} 试运行结果为准。`}</p> : !diagnostics.length && <p className="python-source-hint muted" role="status">正在检查 {languageLabel} 语法…</p>}
    {formatMessage && <p className="python-source-hint" role="status">{formatMessage}</p>}
  </div>
}
