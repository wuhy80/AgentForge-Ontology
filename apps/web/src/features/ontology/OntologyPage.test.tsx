import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiClient, ApiError } from '../../lib/api'
import type { DefinitionRecord, Organization } from '../../lib/types'
import { OntologyPage } from './OntologyPage'

vi.mock('./OntologyGraph', () => ({ OntologyGraph: ({ records, rootIds, onSelect }: { records: DefinitionRecord[]; rootIds: string[]; onSelect: (id: string) => void }) => <div aria-label="测试本体画布"><span>{rootIds.join(',')}</span>{records.filter((item) => rootIds.includes(item.definition.id)).map((item) => <button key={item.definition.id} onClick={() => onSelect(item.definition.id)}>{item.definition.name}节点</button>)}</div> }))

const categories: Organization[] = [{ id: 'knowledge', tenantId: 'tenant', name: '知识库' }, { id: 'other', tenantId: 'tenant', name: '通用定义' }]
const base = { tenantId: 'tenant', categoryId: 'knowledge', groupId: 'content' }
const entries: DefinitionRecord[] = [
  { etag: 'note', status: 'active', definition: { ...base, id: 'notes', kind: 'schema', name: '笔记', description: '保存知识内容', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: 'status-field', name: '状态', dataType: 'string', enumId: 'status' }] } } },
  { etag: 'tags', status: 'active', definition: { ...base, id: 'tags', kind: 'schema', name: '标签', groupId: 'taxonomy', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } },
  { etag: 'link', status: 'active', definition: { ...base, id: 'note-tags', kind: 'link', name: '笔记标记标签', link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记', reverseName: '用于', cardinality: 'N:M', onDelete: 'restrict' } } },
  { etag: 'enum', status: 'active', definition: { ...base, id: 'status', kind: 'enum', name: '笔记状态', enum: { values: [{ code: 'ready', label: '已整理' }] } } },
  { etag: 'action', status: 'active', definition: { ...base, id: 'summarize', kind: 'action', name: '提取摘要', action: { targetSchemaIds: ['notes'], functionId: 'summary', sideEffects: 'none' } } },
]
const external: DefinitionRecord = { etag: 'fn', status: 'active', definition: { ...base, id: 'summary', categoryId: 'other', kind: 'function', name: '摘要计算', function: { input: [], output: [], sideEffects: 'none', runtime: { language: 'python', source: 'hidden source', entrypoint: 'main', timeoutSeconds: 1 } } } }
function fixture() {
  const api = { listDefinitions: vi.fn().mockResolvedValue(entries), listGroups: vi.fn().mockResolvedValue([{ ...base, id: 'content', name: '内容' }, { ...base, id: 'taxonomy', name: '分类标签' }]), getCurrent: vi.fn().mockResolvedValue(external), query: vi.fn(), invoke: vi.fn() } as unknown as ApiClient
  const props = { api, categoryId: 'knowledge', categories, layoutScope: 'tenant:reader', onOpenDefinition: vi.fn(), onQuery: vi.fn() }
  return { api, props }
}
beforeEach(() => { vi.restoreAllMocks(); localStorage.clear() })

describe('本体全景浏览', () => {
  it('读取当前态、展示五类目录，枚举与函数引用可定位并保留精确成员信息', async () => {
    const { api, props } = fixture()
    render(<OntologyPage {...props} />)
    const directory = await screen.findByRole('complementary', { name: '本体目录' })
    expect(vi.mocked(api.listDefinitions).mock.calls[0][0]).toEqual({ categoryId: 'knowledge', view: 'current' })
    fireEvent.click(within(directory).getByRole('button', { name: '查看对象模型：笔记' }))
    const detail = screen.getByRole('article', { name: '本体资源详情' })
    expect(within(detail).getByRole('heading', { name: '笔记' })).toBeTruthy()
    fireEvent.click(within(detail).getAllByRole('button', { name: '查看本体资源：笔记状态' })[0])
    expect(screen.getByRole('tab', { name: '定义依赖图' }).getAttribute('aria-selected')).toBe('true')
    expect(screen.getByRole('table', { name: '枚举成员' }).textContent).toContain('ready已整理')
    fireEvent.click(within(directory).getByRole('button', { name: '查看动作：提取摘要' }))
    fireEvent.click(within(screen.getByRole('article', { name: '本体资源详情' })).getAllByRole('button', { name: /摘要计算/ })[0])
    expect(screen.getByRole('article', { name: '本体资源详情' }).textContent).toContain('通用定义')
    expect(screen.queryByText('hidden source')).toBeNull()
  })

  it('筛选目录与画布根节点，定义/数据跳转只回调且不执行实例查询', async () => {
    const { api, props } = fixture()
    render(<OntologyPage {...props} />)
    const directory = await screen.findByRole('complementary', { name: '本体目录' })
    fireEvent.change(screen.getByLabelText('本体分组'), { target: { value: 'taxonomy' } })
    expect(within(directory).queryByRole('button', { name: '查看对象模型：笔记' })).toBeNull()
    expect(screen.getByLabelText('测试本体画布').textContent).toContain('tags')
    fireEvent.change(screen.getByLabelText('本体分组'), { target: { value: '' } })
    fireEvent.change(screen.getByLabelText('搜索本体定义'), { target: { value: '保存知识' } })
    fireEvent.click(within(directory).getByRole('button', { name: '查看对象模型：笔记' }))
    fireEvent.click(screen.getByRole('button', { name: '查看数据' }))
    expect(props.onQuery).toHaveBeenCalledWith('notes', 'knowledge')
    fireEvent.click(screen.getByRole('button', { name: '查看定义' }))
    expect(props.onOpenDefinition).toHaveBeenCalledWith('notes', 'knowledge')
    expect(api.query).not.toHaveBeenCalled(); expect(api.invoke).not.toHaveBeenCalled()
  })

  it('刷新遇到403清除目录、画布和详情，可显式重新读取当前权限恢复', async () => {
    const { api, props } = fixture()
    render(<OntologyPage {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: '查看对象模型：笔记' }))
    localStorage.setItem('agentforge:ontology-layout:tenant:reader:knowledge:relations', '{"version":1,"positions":{}}')
    localStorage.setItem('agentforge:ontology-layout:tenant:other-reader:knowledge:relations', 'unrelated')
    vi.mocked(api.listDefinitions).mockRejectedValueOnce(new ApiError(403, 'forbidden', '当前定义读取权限已撤销'))
    fireEvent.click(screen.getByRole('button', { name: '刷新本体' }))
    await screen.findByRole('alert')
    expect(screen.queryByRole('complementary', { name: '本体目录' })).toBeNull()
    expect(screen.queryByRole('article', { name: '本体资源详情' })).toBeNull()
    expect(screen.queryByLabelText('测试本体画布')).toBeNull()
    expect(localStorage.getItem('agentforge:ontology-layout:tenant:reader:knowledge:relations')).toBeNull()
    expect(localStorage.getItem('agentforge:ontology-layout:tenant:other-reader:knowledge:relations')).toBe('unrelated')
    fireEvent.click(screen.getByRole('button', { name: '刷新本体' }))
    await screen.findByRole('button', { name: '查看对象模型：笔记' })
    expect(screen.queryByRole('article', { name: '本体资源详情' })).toBeNull()
  })

  it('分类切换取消旧读取，迟到响应不覆盖新分类目录', async () => {
    const { api, props } = fixture()
    let resolve!: (value: DefinitionRecord[]) => void
    vi.mocked(api.listDefinitions).mockImplementationOnce(() => new Promise((yes) => { resolve = yes })).mockResolvedValueOnce([external])
    const view = render(<OntologyPage {...props} />)
    await waitFor(() => expect(api.listDefinitions).toHaveBeenCalledTimes(1))
    const oldSignal = vi.mocked(api.listDefinitions).mock.calls[0][1]!
    view.rerender(<OntologyPage {...props} categoryId="other" />)
    await screen.findByRole('button', { name: '查看函数：摘要计算' })
    expect(oldSignal.aborted).toBe(true)
    await act(async () => resolve(entries))
    expect(screen.queryByRole('button', { name: '查看对象模型：笔记' })).toBeNull()
    expect(screen.getByRole('button', { name: '查看函数：摘要计算' })).toBeTruthy()
  })

  it('同一稳定ID改名后使用刷新定义，不沿用旧名称或详情', async () => {
    const { api, props } = fixture()
    render(<OntologyPage {...props} />)
    fireEvent.click(await screen.findByRole('button', { name: '查看对象模型：笔记' }))
    vi.mocked(api.listDefinitions).mockResolvedValue(entries.map((item) => item.definition.id === 'notes' ? { ...item, definition: { ...item.definition, name: '知识笔记' } } : item))
    fireEvent.click(screen.getByRole('button', { name: '刷新本体' }))
    await screen.findByRole('button', { name: '查看对象模型：知识笔记' })
    expect(within(screen.getByRole('article', { name: '本体资源详情' })).getByRole('heading', { name: '知识笔记' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '查看对象模型：笔记' })).toBeNull()
  })

  it('没有分类时不发目录请求，也不把空分类当成所有分类', async () => {
    const { api, props } = fixture()
    render(<OntologyPage {...props} categoryId="" />)
    await screen.findByRole('heading', { name: '先选择当前分类' })
    expect(api.listDefinitions).not.toHaveBeenCalled()
  })
})
