import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ApiError, type ApiClient } from '../../lib/api'
import type { Definition, DefinitionRecord } from '../../lib/types'
import type { ModelEditorProps } from './ModelEditor'
import { LinkVisualEditor } from './LinkVisualEditor'

vi.mock('./RelationGraph', () => ({ RelationGraph: ({ onConnect, links }: { onConnect?: (source: string, target: string) => void; links: DefinitionRecord[] }) => <div aria-label="关系画布测试替身"><output>{links[links.length - 1]?.definition.link?.forwardName}</output>{onConnect && <button onClick={() => onConnect('tags', 'notes')}>测试拖线</button>}</div> }))
const category = { id: 'knowledge', tenantId: 'tenant', name: '知识库' }
const base = { tenantId: 'tenant', categoryId: 'knowledge', groupId: 'group' }
const schemas: DefinitionRecord[] = ['notes', 'tags'].map((id) => ({ status: 'active', etag: id, definition: { ...base, id, kind: 'schema', name: id === 'notes' ? '笔记' : '标签', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [{ id: `${id}-name`, name: '名称', dataType: 'string' }] } } }))
const current: DefinitionRecord = { status: 'active', etag: 'current-link', definition: { ...base, id: 'link', kind: 'link', name: '笔记标记标签', link: { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict' } } }
function fixture(initial: DefinitionRecord | undefined = current): { props: ModelEditorProps; api: Record<string, ReturnType<typeof vi.fn>> } {
  const api = {
    listGroups: vi.fn().mockResolvedValue([{ ...base, id: 'group', name: '知识组织' }]),
    createDefinition: vi.fn().mockImplementation(async (definition: Definition) => ({ definition: { ...definition, id: 'new-link', tenantId: 'tenant' }, status: 'draft', etag: 'created' })),
    saveDraft: vi.fn().mockImplementation(async (_id, definition: Definition) => ({ definition, status: 'draft', etag: 'draft-link' })),
    preview: vi.fn().mockResolvedValue({ status: 'ready', operationId: 'preview', statements: ['CREATE TABLE relation (...)'] }),
    publish: vi.fn().mockResolvedValue({ id: 'publication', tenantId: 'tenant', definitionId: 'link', status: 'succeeded' }),
    query: vi.fn().mockResolvedValue({ fields: [], data: [], hasMore: false }),
  }
  return { api, props: { api: api as unknown as ApiClient, categories: [category], categoryId: 'knowledge', entries: [...schemas, ...(initial ? [initial] : [])], currentEntries: [...schemas, current], initialKind: 'link', initialRecord: initial, onClose: vi.fn(), onOrganizations: vi.fn(), onSaved: vi.fn().mockResolvedValue(undefined), onPublished: vi.fn().mockResolvedValue(undefined), onPending: vi.fn() } }
}

describe('关系详情可视化编辑', () => {
  it('拖线与表单编辑共用新草稿，提交不带画布临时ID或布局信息', async () => {
    const { props, api } = fixture(undefined)
    delete props.initialRecord
    props.entries = schemas; props.currentEntries = schemas; props.initialLink = { sourceSchemaId: 'notes', targetSchemaId: 'tags' }
    render(<LinkVisualEditor {...props} />)
    expect((screen.getByLabelText('来源模型') as HTMLSelectElement).value).toBe('notes')
    fireEvent.click(screen.getByRole('button', { name: '测试拖线' }))
    expect((screen.getByLabelText('来源模型') as HTMLSelectElement).value).toBe('tags')
    expect((screen.getByLabelText('目标模型') as HTMLSelectElement).value).toBe('notes')
    fireEvent.change(screen.getByLabelText('业务名称'), { target: { value: '标签标记笔记' } })
    fireEvent.change(screen.getByLabelText('正向业务名称'), { target: { value: '标记笔记' } })
    fireEvent.change(screen.getByLabelText('反向业务名称'), { target: { value: '所属标签' } })
    await screen.findByRole('option', { name: '知识组织' })
    fireEvent.change(screen.getByLabelText('分组'), { target: { value: 'group' } })
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledTimes(1))
    expect(api.createDefinition.mock.calls[0][0]).toMatchObject({ id: '', tenantId: '', name: '标签标记笔记', link: { sourceSchemaId: 'tags', targetSchemaId: 'notes' } })
    expect(JSON.stringify(api.createDefinition.mock.calls[0][0])).not.toMatch(/position|unsaved-relation/)
  })

  it('已生效端点锁定，纯定义补映射并发布后绑定锁定，预览不写入', async () => {
    const { props, api } = fixture()
    render(<LinkVisualEditor {...props} />)
    expect((screen.getByLabelText('来源模型') as HTMLSelectElement).disabled).toBe(true)
    expect(screen.queryByRole('button', { name: '测试拖线' })).toBeNull()
    fireEvent.click(screen.getByRole('tab', { name: '数据映射' }))
    fireEvent.change(screen.getByLabelText('关系存储方式'), { target: { value: 'managed' } })
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalledWith('link', expect.objectContaining({ link: expect.objectContaining({ storageBinding: { mode: 'managed' } }) }), 'current-link'))
    fireEvent.click(screen.getByRole('button', { name: '预览计划' }))
    await screen.findByText('CREATE TABLE relation (...)')
    expect(api.publish).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '发布定义' }))
    await screen.findByText('发布成功，当前定义已生效。')
    expect((screen.getByLabelText('关系存储方式') as HTMLSelectElement).disabled).toBe(true)
  })

  it('并发冲突保留本地草稿，未知发布结果暂停编辑并用原ETag恢复', async () => {
    const { props, api } = fixture()
    api.saveDraft.mockRejectedValueOnce(new ApiError(412, 'etag_mismatch', '已有更新'))
    const view = render(<LinkVisualEditor {...props} embedded />)
    expect(screen.queryByLabelText('关系画布测试替身')).toBeNull()
    fireEvent.change(screen.getByLabelText('业务名称'), { target: { value: '本地名称' } })
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await screen.findByRole('alert')
    expect((screen.getByLabelText('业务名称') as HTMLInputElement).value).toBe('本地名称')
    expect((screen.getByRole('button', { name: '保存草稿' }) as HTMLButtonElement).disabled).toBe(true)
    view.unmount()
    const again = fixture()
    again.api.publish.mockRejectedValueOnce(new ApiError(0, 'network_error', '网络中断'))
    render(<LinkVisualEditor {...again.props} embedded />)
    fireEvent.click(screen.getByRole('button', { name: '确认生效状态' }))
    await screen.findByText('发布结果待确认')
    expect(screen.getByLabelText('业务名称').matches(':disabled')).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '继续恢复发布' }))
    await screen.findByText('发布成功，当前定义已生效。')
    expect(again.api.publish).toHaveBeenNthCalledWith(2, 'link', 'current-link')
  })

  it('数据预览使用授权目录当前关系，缺失端点时不展示物理映射', async () => {
    const { props, api } = fixture({ ...current, status: 'draft', hasCurrent: true, etag: 'draft', definition: { ...current.definition, name: '草稿关系', link: { ...current.definition.link!, forwardName: '草稿正向名' } } })
    props.currentEntries = [...schemas, { ...current, definition: { ...current.definition, link: { ...current.definition.link!, storageBinding: { mode: 'managed' } } } }]
    const view = render(<LinkVisualEditor {...props} embedded />)
    fireEvent.click(screen.getByRole('tab', { name: '数据预览' }))
    expect(screen.getByRole('option', { name: /正向：笔记 → 标记标签 → 标签/ })).toBeTruthy()
    expect(screen.queryByRole('option', { name: /草稿正向名/ })).toBeNull()
    expect(api.query).not.toHaveBeenCalled()
    view.rerender(<LinkVisualEditor {...props} currentEntries={[schemas[0], current]} embedded />)
    fireEvent.click(screen.getByRole('tab', { name: '数据映射' }))
    expect(screen.getByText(/两个可见的已生效对象模型/)).toBeTruthy()
    expect(screen.queryByLabelText('关系存储方式')).toBeNull()
  })
})
