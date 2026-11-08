import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// jsdom does not lay out text ranges. Supply only missing geometry methods so
// CodeMirror can schedule measurements; actual layout is verified in a browser.
if (!Range.prototype.getClientRects) {
  Range.prototype.getClientRects = () => Object.assign([] as DOMRect[], { item: () => null })
}
if (!Range.prototype.getBoundingClientRect) {
  Range.prototype.getBoundingClientRect = () => new DOMRect()
}

afterEach(() => cleanup())
