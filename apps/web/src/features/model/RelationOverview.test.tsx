import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { DefinitionRecord } from '../../lib/types'
import { RelationOverview, relationCatalog } from './RelationOverview'

vi.mock('./RelationGraph', () => ({ RelationGraph: ({ schemas, links, onConnect }: { schemas: DefinitionRecord[]; links: DefinitionRecord[]; onConnect: (source: string, target: string) => void }) => <div aria-label="测试画布"><span>{schemas.map((item) => item.definition.name).join('、')}</span><span>{links.map((item) => item.definition.name).join('、')}</span><button onClick={() => onConnect('note', 'tag')}>连接笔记和标签</button></div> }))

const categories = [{ id: 'knowledge', tenantId: 'tenant', name: '知识库' }, { id: 'other', tenantId: 'tenant', name: '其他分类' }]
const base = { tenantId: 'tenant', categoryId: 'knowledge', groupId: 'group' }
const schema = (id: string, name: string, categoryId = 'knowledge'): DefinitionRecord => ({ etag: id, status: 'active', definition: { ...base, categoryId, id, name, kind: 'schema', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } })
const link: DefinitionRecord = { status: 'active', etag: 'link-active', definition: { ...base, id: 'note-tag', name: '笔记标记标签', kind: 'link', link: { sourceSchemaId: 'note', targetSchemaId: 'tag', forwardName: '标记标签', reverseName: '用于笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'managed' } } } }
const current = [schema('note', '笔记'), schema('tag', '标签', 'other'), schema('unrelated', '无关对象', 'other'), link]
const draft: DefinitionRecord = { ...link, etag: 'link-draft', status: 'draft', hasCurrent: true, definition: { ...link.definition, name: '笔记标签待发布' } }

describe('分类关系总览', () => {
  it('默认按当前态建图，草稿按显式选择覆盖，跨租户目录不进入图', () => {
    const foreign = { ...link, definition: { ...link.definition, id: 'foreign', tenantId: 'elsewhere', name: '其他租户关系' } }
    expect(relationCatalog([draft, foreign], current, 'tenant', false)).toEqual(current)
    const visible = relationCatalog([draft, foreign], current, 'tenant', true)
    expect(visible.find((item) => item.definition.id === link.definition.id)).toBe(draft)
    expect(visible.some((item) => item.definition.tenantId !== 'tenant')).toBe(false)
  })

  it('跨分类仅补充关系端点，支持草稿筛选、选线及拖线端点回调', async () => {
    const select = vi.fn(), connect = vi.fn()
    render(<RelationOverview entries={[...current.filter((item) => item.definition.id !== link.definition.id), draft]} currentEntries={current} categories={categories} categoryId="knowledge" groups={[]} onSelectLink={select} onSelectSchema={vi.fn()} onCreateLink={connect} />)
    const canvas = screen.getByLabelText('测试画布')
    expect(within(canvas).getByText('笔记、标签')).toBeTruthy()
    expect(within(canvas).queryByText(/无关对象/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: '编辑关系：笔记标记标签' }))
    expect(select).toHaveBeenCalledWith(link)
    await userEvent.click(screen.getByRole('button', { name: '连接笔记和标签' }))
    expect(connect).toHaveBeenCalledWith('note', 'tag')
    await userEvent.click(screen.getByRole('checkbox', { name: '显示草稿' }))
    expect(screen.getByRole('button', { name: '编辑关系：笔记标签待发布' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '编辑关系：笔记标记标签' })).toBeNull()
    await userEvent.selectOptions(screen.getByLabelText('关系映射状态'), 'foreign_key')
    expect(screen.queryByRole('button', { name: '编辑关系：笔记标签待发布' })).toBeNull()
  })

  it('新的授权目录清除可见关系与节点名称', () => {
    const props = { categories, categoryId: 'knowledge', groups: [], onSelectLink: vi.fn(), onSelectSchema: vi.fn(), onCreateLink: vi.fn() }
    const view = render(<RelationOverview {...props} entries={current} currentEntries={current} />)
    expect(screen.getByRole('button', { name: '编辑关系：笔记标记标签' })).toBeTruthy()
    view.rerender(<RelationOverview {...props} entries={[]} currentEntries={[]} />)
    expect(screen.queryByRole('button', { name: '编辑关系：笔记标记标签' })).toBeNull()
    expect(screen.queryByText('笔记、标签')).toBeNull()
  })
})
