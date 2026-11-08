import { useEffect, useState } from 'react'
import type { ApiClient } from '../../lib/api'
import type { WriteCapabilities, WriteOperation } from '../../lib/types'

export function useSchemaWrites(api: ApiClient, schemaId: string, mapped: boolean) {
  const [capabilities, setCapabilities] = useState<WriteCapabilities>()
  const [checked, setChecked] = useState(false)
  useEffect(() => {
    setCapabilities(undefined); setChecked(false)
    // A consumer embedding an older client remains conservative for mappings.
    if (typeof api.writeCapabilities !== 'function') return
    const abort = new AbortController()
    api.writeCapabilities(schemaId, abort.signal).then((value) => {
      if (!abort.signal.aborted && value.schemaId === schemaId) setCapabilities(value)
    }).catch(() => { /* Actual writes remain closed until authority can be read. */ })
      .finally(() => { if (!abort.signal.aborted) setChecked(true) })
    return () => abort.abort()
  }, [api, schemaId])
  return { capabilities, allowed: (operation: WriteOperation) => capabilities
    ? capabilities.operations.some((entry) => entry.name === operation && entry.allowed)
    : !checked && typeof api.writeCapabilities !== 'function' && !mapped }
}
