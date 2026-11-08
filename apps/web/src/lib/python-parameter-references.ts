import { pythonLanguage } from '@codemirror/lang-python'
import { javascriptLanguage } from '@codemirror/lang-javascript'
import { goLanguage } from '@codemirror/lang-go'
import type { ScriptLanguage } from './types'

// This is a reminder for literal keys, not a Python data flow analysis. Strings
// are read from the parser, so a comment or a quoted code example is not matched.
export function parameterReferenceLines(source: string, name: string, language: ScriptLanguage = 'python'): number[] {
  if (source.length > 65536 || !name) return []
  const lines = new Set<number>()
  const tree = (language === 'go' ? goLanguage : language === 'javascript' ? javascriptLanguage : pythonLanguage).parser.parse(source)
  tree.iterate({ enter(node) {
    if (node.name !== 'String') return
    const parent = node.node.parent
    const literal = source.slice(node.from, node.to)
    const match = /^([rRuU]*)(["'])(?!\2\2)([\s\S]*)\2$/.exec(literal)
    if (!match) return
    let value = match[3]
    if (!/r/i.test(match[1])) value = value.replace(/\\(?:([\\"'])|x([\da-fA-F]{2})|u([\da-fA-F]{4})|U([\da-fA-F]{8})|([nrtbfva]))/g, (sequence, quote, hex, unicode, wide, control) => {
      if (quote) return quote
      const point = hex ?? unicode ?? wide
      if (point) { const code = parseInt(point, 16); return code <= 0x10ffff ? String.fromCodePoint(code) : sequence }
      return ({ n: '\n', r: '\r', t: '\t', b: '\b', f: '\f', v: '\v', a: '\x07' } as Record<string, string>)[control]
    })
    if (value !== name) return
    const context = parent ? source.slice(parent.from, parent.to) : ''
    // Inputs can use indexing or get(); output keys can be in any dictionary.
    const isInput = (parent?.name === 'MemberExpression' || parent?.name === 'IndexExpr') && /^inputs\s*\[/.test(context)
      || parent?.name === 'ArgList' && /\binputs\.get\s*$/.test(source.slice(Math.max(0, parent.from - 40), parent.from))
    const isKey = parent?.name === 'DictionaryExpression' && /^\s*:/.test(source.slice(node.to, parent.to))
      || parent?.name === 'Property' && /^\s*:/.test(source.slice(node.to, parent.to))
      || parent?.name === 'Key' && /^\s*:/.test(source.slice(node.to, node.to + 20))
    if (isInput || isKey) lines.add(source.slice(0, node.from).split('\n').length)
  } })
  return [...lines]
}
