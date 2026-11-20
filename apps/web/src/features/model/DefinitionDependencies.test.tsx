import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { DefinitionDependencies } from './DefinitionDependencies'
import type { ApiClient } from '../../lib/api'
import type { DefinitionDependencyImpact, DefinitionRecord } from '../../lib/types'

const record: DefinitionRecord = { status: 'draft', hasCurrent: true, etag: 'draft-etag', definition: {
  id: 'schema', tenantId: 'tenant', kind: 'schema', categoryId: 'category', groupId: 'group', name: '工单',
} }
function report(view: 'current' | 'draft' = 'draft'): DefinitionDependencyImpact {
  const root = { id: 'schema', name: '工单', kind: 'schema' as const, categoryId: 'category', groupId: 'group', etag: `${view}-etag`, view }
  return { scope: 'direct-definition-references', definition: root,
    dependencies: [{ definition: { ...root, id: 'enum', kind: 'enum', name: '状态', view: 'current' }, reasons: ['enum'] }],
    dependents: [{ definition: { ...root, id: 'link', kind: 'link', name: '工单关系', view: 'current' }, reasons: ['link-source', 'link-target'] },
      { definition: { ...root, id: 'action', kind: 'action', name: '跨分类动作草稿', categoryId: 'other', view: 'draft' }, reasons: ['data-write', 'action-target'] }],
  }
}
function fixture() {
  const api = { definitionDependencies: vi.fn().mockImplementation(async (_id, view) => report(view)) }
  const props = { api: api as unknown as ApiClient, record, dirty: false }
  return { api, props }
}
describe('定义依赖影响', () => {
  it('按需读取已保存视图，分别显示引用原因、草稿与跨分类，并明确覆盖范围', async () => {
    const { api, props } = fixture()
    render(<DefinitionDependencies {...props} />)
    expect(api.definitionDependencies).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    expect(await screen.findByRole('link', { name: '状态' })).toBeTruthy()
    expect(api.definitionDependencies).toHaveBeenCalledWith('schema', 'draft', expect.any(AbortSignal))
    const incoming = screen.getByRole('region', { name: '被谁引用' })
    expect(within(incoming).getByText('关系来源、关系目标')).toBeTruthy()
    expect(within(incoming).getByText('跨分类动作草稿')).toBeTruthy()
    expect(screen.queryByRole('link', { name: '跨分类动作草稿' })).toBeNull()
    expect(screen.getByText(/空列表不能作为停用或删除依据/)).toBeTruthy()
    await userEvent.selectOptions(screen.getByLabelText('查看内容'), 'current')
    await waitFor(() => expect(api.definitionDependencies).toHaveBeenLastCalledWith('schema', 'current', expect.any(AbortSignal)))
  })
  it('刷新遇到撤权时清理旧结果，失败不显示空列表', async () => {
    const { api, props } = fixture()
    render(<DefinitionDependencies {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    await screen.findByText('跨分类动作草稿')
    api.definitionDependencies.mockRejectedValueOnce(new Error('依赖可见范围已变化'))
    await userEvent.click(screen.getByRole('button', { name: '刷新依赖' }))
    expect((await screen.findByRole('alert')).textContent).toContain('依赖可见范围已变化')
    expect(screen.queryByText('跨分类动作草稿')).toBeNull()
    expect(screen.queryByText('暂无可展示的直接定义引用。')).toBeNull()
  })
  it('未保存编辑和离开页面中止在途请求，迟到响应不会恢复旧结果', async () => {
    const { api, props } = fixture()
    let resolve!: (value: DefinitionDependencyImpact) => void
    api.definitionDependencies.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const { rerender } = render(<DefinitionDependencies {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    const signal = api.definitionDependencies.mock.calls[0][2] as AbortSignal
    rerender(<DefinitionDependencies {...props} dirty />)
    expect(signal.aborted).toBe(true)
    resolve(report())
    await waitFor(() => expect(screen.queryByText('跨分类动作草稿')).toBeNull())
    expect(screen.getByText(/存在未保存修改/)).toBeTruthy()
    rerender(<DefinitionDependencies {...props} active={false} />)
    expect(screen.queryByRole('region', { name: '定义依赖影响' })).toBeNull()
  })
  it('保存内容变化后重新读取；服务器摘要不匹配拒绝旧报告', async () => {
    const { api, props } = fixture()
    const { rerender } = render(<DefinitionDependencies {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    await screen.findByText('跨分类动作草稿')
    rerender(<DefinitionDependencies {...props} record={{ ...record, etag: 'new-draft' }} />)
    expect((await screen.findByRole('alert')).textContent).toContain('定义内容已变化')
    expect(screen.queryByText('跨分类动作草稿')).toBeNull()
    expect(api.definitionDependencies).toHaveBeenCalledTimes(2)
  })
  it('收起后不保留结果，空列表仍显示资格范围', async () => {
    const { api, props } = fixture()
    api.definitionDependencies.mockResolvedValueOnce({ ...report(), dependencies: [], dependents: [] })
    render(<DefinitionDependencies {...props} />)
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    expect(await screen.findAllByText('暂无可展示的直接定义引用。')).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: '收起定义依赖' }))
    expect(screen.queryByText('暂无可展示的直接定义引用。')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '查看定义依赖' }))
    await screen.findByText('跨分类动作草稿')
    expect(api.definitionDependencies).toHaveBeenCalledTimes(2)
  })
})
