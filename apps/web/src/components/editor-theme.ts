import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorView } from '@codemirror/view'
import { tags } from '@lezer/highlight'

// CSS variables update the existing editor, preserving selection and undo history.
export const editorTheme = [
  syntaxHighlighting(HighlightStyle.define([
    { tag: [tags.keyword, tags.modifier, tags.controlKeyword, tags.operatorKeyword], color: 'var(--syntax-keyword)' },
    { tag: [tags.string, tags.regexp], color: 'var(--syntax-string)' },
    { tag: [tags.number, tags.bool, tags.null, tags.atom], color: 'var(--syntax-number)' },
    { tag: [tags.typeName, tags.className, tags.function(tags.variableName), tags.standard(tags.variableName)], color: 'var(--syntax-name)' },
    { tag: [tags.variableName, tags.propertyName, tags.operator, tags.punctuation], color: 'var(--ink)' },
    { tag: [tags.comment, tags.meta], color: 'var(--syntax-comment)' },
    { tag: tags.invalid, color: 'var(--danger)' },
  ])),
  EditorView.theme({
    '&': { color: 'var(--ink)', backgroundColor: 'var(--editor)' },
    '.cm-content': { caretColor: 'var(--primary)', fontFamily: '"Cascadia Code", Consolas, "Microsoft YaHei", monospace' },
    '.cm-gutters': { color: 'var(--muted)', backgroundColor: 'var(--editor)', border: 'none' },
    '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--editor-active)' },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'var(--editor-selection)' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--primary)' },
    '.cm-tooltip, .cm-panels': { color: 'var(--ink)', backgroundColor: 'var(--surface-raised)', border: '1px solid var(--line)' },
    '.cm-tooltip-autocomplete ul li[aria-selected]': { backgroundColor: 'var(--primary-soft)', color: 'var(--primary)' },
    '.cm-completionDetail, .cm-placeholder': { color: 'var(--muted)' },
    '.cm-matchingBracket': { backgroundColor: 'var(--primary-soft)', color: 'var(--primary)' },
    '.cm-diagnostic-error': { borderLeftColor: 'var(--danger)' },
    '.cm-diagnostic-warning': { borderLeftColor: 'var(--warning)' },
  }),
]
