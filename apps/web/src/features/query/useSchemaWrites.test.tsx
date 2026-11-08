import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import type { ApiClient } from '../../lib/api'
import type { WriteCapabilities } from '../../lib/types'
import { useSchemaWrites } from './useSchemaWrites'

afterEach(cleanup)
const caps = (id = 'mapped'): WriteCapabilities => ({ schemaId: id, operations: [{ name: 'create', allowed: true, writeFields: [] }, { name: 'update', allowed: true, writeFields: [] }, { name: 'delete', allowed: false, writeFields: [] }],
  fields: [], matchModes: [], uniqueKeys: [], overwriteAllowed: false, maxBulkItems: 100, maxRequestBytes: 1024, maxResponseBytes: 1024 })
describe('数据探索写入能力', () => {
  it('默认关闭写入，映射就绪后分别采用服务端三种操作授权', async () => {
    let finish!: (value: WriteCapabilities) => void
    const api = { writeCapabilities: vi.fn().mockReturnValue(new Promise((resolve) => { finish = resolve })) } as unknown as ApiClient
    const hook = renderHook(() => useSchemaWrites(api, 'mapped', true))
    expect(hook.result.current.allowed('create')).toBe(false)
    await act(async () => finish(caps()))
    expect(hook.result.current.allowed('create')).toBe(true)
    expect(hook.result.current.allowed('update')).toBe(true)
    expect(hook.result.current.allowed('delete')).toBe(false)
  })
  it('切换对象时拒绝旧能力，读取失败保持关闭', async () => {
    let finish!: (value: WriteCapabilities) => void
    const api = { writeCapabilities: vi.fn().mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockRejectedValueOnce(new Error('forbidden')) } as unknown as ApiClient
    const hook = renderHook(({ id }) => useSchemaWrites(api, id, true), { initialProps: { id: 'mapped' } })
    hook.rerender({ id: 'other' })
    await act(async () => finish(caps()))
    await waitFor(() => expect(hook.result.current.allowed('update')).toBe(false))
    expect(hook.result.current.capabilities).toBeUndefined()
  })
})
