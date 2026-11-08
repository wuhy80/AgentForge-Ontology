import { editorTheme } from '../../components/editor-theme'
import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react';
import { Annotation, EditorState, Prec } from '@codemirror/state';
import { EditorView, drawSelection, highlightActiveLine, keymap, lineNumbers } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { autocompletion, closeCompletion, completionKeymap, startCompletion } from '@codemirror/autocomplete';
import { StreamLanguage } from '@codemirror/language';
import { lintGutter, setDiagnostics } from '@codemirror/lint';
import type { OntologySQLCompletions, SQLDiagnostic, SQLRange } from '../../lib/types';

export type SQLTextEdit = { text: string; from: number; to: number };
export type SQLEditorHandle = {
  insert: (text: string) => void;
  getSelection: () => { from: number; to: number } | undefined;
  applyEdit: (edit: SQLTextEdit) => void;
  locate: (range: SQLRange) => void; complete: () => void; focus: () => void;
};
type Props = {
  text: string; diagnostics: SQLDiagnostic[]; onChange: (text: string) => void; onRun: () => void;
  onComposition: (composing: boolean) => void;
  onComplete: (text: string, cursor: number, signal: AbortSignal) => Promise<OntologySQLCompletions | undefined>;
};

const external = Annotation.define<boolean>();
const language = StreamLanguage.define<{ comment: boolean }>({
  startState: () => ({ comment: false }),
  token(stream, state) {
    if (state.comment) {
      while (!stream.eol()) if (stream.next() === '*' && stream.eat('/')) { state.comment = false; break; }
      return 'comment';
    }
    if (stream.eatSpace()) return null;
    if (stream.match('--')) { stream.skipToEnd(); return 'comment'; }
    if (stream.match('/*')) { state.comment = true; return 'comment'; }
    const quote = stream.peek();
    if (quote === '"' || quote === "'") {
      stream.next();
      while (!stream.eol()) if (stream.next() === quote && !stream.eat(quote)) break;
      return quote === '"' ? 'variableName' : 'string';
    }
    if (stream.match(/^-?\d+(?:\.\d+)?/)) return 'number';
    if (stream.eat(':')) { stream.eatWhile(/[\p{L}\p{N}_]/u); return 'typeName'; }
    if (stream.match(/^(?:SELECT|FROM|AS|WHERE|AND|OR|NOT|IS|NULL|IN|TRUE|FALSE|ORDER|BY|ASC|DESC|LIMIT|STARTS_WITH|JOIN|LINK|VIA|DIRECTION|OUT)\b/i)) return 'keyword';
    if (stream.match(/^[=<>!]+/)) return 'operator';
    stream.next(); return null;
  },
});

export const OntologySQLEditor = forwardRef<SQLEditorHandle, Props>(function OntologySQLEditor(props, ref) {
  const host = useRef<HTMLDivElement>(null);
  const editor = useRef<EditorView | undefined>(undefined);
  const latest = useRef(props); latest.current = props;
  const composing = useRef(false);
  const alive = useRef(false);
  useImperativeHandle(ref, () => ({
    insert(text) {
      const view = editor.current;
      if (!view || composing.current || view.composing) return;
      view.dispatch(view.state.replaceSelection(text)); view.focus();
    },
    getSelection() {
      const selection = editor.current?.state.selection.main;
      return selection && { from: selection.from, to: selection.to };
    },
    applyEdit(edit) {
      const view = editor.current;
      if (!view || composing.current || view.composing) return;
      closeCompletion(view);
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: edit.text },
        selection: { anchor: edit.from, head: edit.to }, scrollIntoView: true,
        userEvent: 'input',
      });
      view.focus();
    },
    locate(range) {
      const view = editor.current;
      if (!view) return;
      const from = Math.max(0, Math.min(view.state.doc.length, range.from));
      const to = Math.max(from, Math.min(view.state.doc.length, range.to));
      view.dispatch({ selection: { anchor: from, head: to }, scrollIntoView: true }); view.focus();
    },
    complete() { const view = editor.current; if (view && !composing.current && !view.composing) { view.focus(); startCompletion(view); } },
    focus() { editor.current?.focus(); },
  }), []);
  useEffect(() => {
    if (!host.current) return;
    alive.current = true;
    const view = new EditorView({
      parent: host.current,
      state: EditorState.create({ doc: latest.current.text, extensions: [
        lineNumbers(), highlightActiveLine(), drawSelection(), history(), lintGutter(), language,
        editorTheme,
        EditorView.contentAttributes.of({ 'aria-label': 'OntologySQL 编辑器', 'aria-multiline': 'true', spellcheck: 'false', autocapitalize: 'off' }),
        Prec.highest(keymap.of([{ key: 'Mod-Enter', run(current) { if (composing.current || current.composing) return false; latest.current.onRun(); return true; } }])),
        keymap.of([...completionKeymap, ...defaultKeymap, ...historyKeymap, indentWithTab]),
        EditorView.domEventHandlers({
          compositionstart() { composing.current = true; closeCompletion(view); latest.current.onComposition(true); return false; },
          compositionend() {
            // Let CodeMirror consume the final composition transaction first.
            setTimeout(() => { if (alive.current) { composing.current = false; latest.current.onComposition(false); } }, 0);
            return false;
          },
        }),
        autocompletion({ activateOnTyping: true, activateOnTypingDelay: 250, interactionDelay: 150, override: [async (context) => {
          if (composing.current || view.composing) return null;
          const controller = new AbortController();
          context.addEventListener('abort', () => controller.abort(), { onDocChange: true });
          const text = context.state.doc.toString();
          const result = await latest.current.onComplete(text, context.pos, controller.signal);
          if (!result || controller.signal.aborted || context.aborted || view.state.doc.toString() !== text || composing.current) return null;
          return {
            from: context.pos, to: context.pos, filter: false,
            options: result.items.map((item) => ({
              label: item.label, detail: item.detail, type: item.kind === 'model' ? 'class' : item.kind === 'field' ? 'property' : 'keyword',
              apply(current: EditorView) {
                if (composing.current || current.composing || current.state.doc.toString() !== text) return;
                const { from, to } = item.range;
                if (from < 0 || to < from || to > current.state.doc.length) return;
                current.dispatch({ changes: { from, to, insert: item.insertText }, selection: { anchor: from + item.insertText.length } });
              },
            })),
          };
        }] }),
        EditorView.updateListener.of((update) => {
          if (update.docChanged && !update.transactions.some((transaction) => transaction.annotation(external))) latest.current.onChange(update.state.doc.toString());
        }),
        EditorView.theme({
          '&': { fontSize: '13px', minHeight: '265px', maxHeight: '520px', backgroundColor: 'var(--editor)' },
          '.cm-content': { fontFamily: 'Consolas, "Microsoft YaHei", monospace', padding: '14px 0', minHeight: '265px' },
          '.cm-scroller': { overflow: 'auto', lineHeight: '1.8' },
          '.cm-gutters': { backgroundColor: 'var(--editor)', color: 'var(--muted)', border: 'none' },
          '.cm-activeLine': { backgroundColor: 'var(--editor-active)' },
          '.cm-tooltip-autocomplete': { maxWidth: 'min(420px, 85vw)' },
          '&.cm-focused': { outline: 'none' },
        }),
      ] }),
    });
    editor.current = view;
    return () => { alive.current = false; composing.current = false; editor.current = undefined; view.destroy(); };
  }, []);
  useEffect(() => {
    const view = editor.current;
    if (view && view.state.doc.toString() !== props.text && !composing.current) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: props.text }, annotations: external.of(true) });
    }
  }, [props.text]);
  useEffect(() => {
    const view = editor.current;
    if (!view || composing.current || view.state.doc.toString() !== props.text) return;
    view.dispatch(setDiagnostics(view.state, props.diagnostics.map((diagnostic) => ({
      from: Math.max(0, Math.min(view.state.doc.length, diagnostic.range.from)),
      to: Math.max(0, Math.min(view.state.doc.length, diagnostic.range.to)),
      message: diagnostic.message, severity: diagnostic.severity,
    }))));
  }, [props.diagnostics, props.text]);
  return <div className="ontology-sql-editor" ref={host} />;
});

export default OntologySQLEditor;
