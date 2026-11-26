import { expect, it } from 'vitest'
import type { DefinitionRecord } from '../../lib/types'
import { collectCallableTargets } from './panorama-references'

const target = '02926da2-bfcd-4bb7-85de-00aec1bc85fb'

it.each([
  ['python', `# ctx.data.create("00000000-0000-0000-0000-000000000000", {})
ctx.data.create("${target}", {})`],
  ['javascript', `// ctx.data.create("00000000-0000-0000-0000-000000000000", {})
ctx.data.bulkCreate("${target}", rows)`],
  ['go', `/* ctx.Data.Create("00000000-0000-0000-0000-000000000000", nil) */
ctx.Data.Create("${target}", nil, "row")`],
] as const)('recognizes a literal SDK target in %s', (language, source) => {
  const record: DefinitionRecord = { status: 'active', etag: 'a', definition: {
    id: 'a', tenantId: 'tenant', categoryId: 'category', groupId: 'group', name: '计算', kind: 'function',
    function: { sideEffects: 'dataWrite', runtime: { language, source, entrypoint: 'main', timeoutSeconds: 2 } },
  } }
  expect(collectCallableTargets(record)).toEqual([{ targetId: target, label: '源码写入', kind: 'target' }])
})
