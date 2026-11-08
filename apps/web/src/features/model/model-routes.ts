import type { DefinitionRecord, Kind } from '../../lib/types'

export function modelListRoute(kind: Kind) {
  return kind === 'schema' ? 'models' : `models?kind=${kind}`
}

export function modelRouteKind(route = '', entries: DefinitionRecord[] = []): Kind {
  const [path, query] = route.split('?')
  const params = new URLSearchParams(query)
  if (path.startsWith('models/') && path !== 'models/new') {
    try {
      const record = entries.find((entry) => entry.definition.id === decodeURIComponent(path.slice(7)))
      if (record) return record.definition.kind
    } catch { /* Invalid addresses are reported by the model page. */ }
  }
  const requested = params.get('kind')
  if (requested === 'schema' || requested === 'enum' || requested === 'link' || requested === 'action' || requested === 'function') return requested
  return params.get('view') === 'graph' ? 'link' : 'schema'
}
